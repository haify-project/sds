package controller

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	pb "github.com/haify-project/haify/api/proto/v1"
	"go.uber.org/zap"
)

// DR failover had no way back. After `resource dr-failover` the DR node is
// Primary and the primary site, once it returns, holds a copy that went its
// own way after the link was cut. Returning meant, on each primary-site node,
// disconnecting, demoting and reconnecting with --discard-my-data, restarting
// its tunnel, waiting out a resync across the WAN, and only then swapping the
// roles back — by hand, in that order, and with the one irreversible step (the
// discard) easy to aim at the wrong side.
//
// DRFailback does it in phases and can be run again at any point: each run
// reads where the resource stands and carries on from there.

const (
	failbackResyncing = "resyncing"
	failbackDone      = "done"
)

// failbackPollInterval is how often the resync is checked while waiting.
var failbackPollInterval = 5 * time.Second

func (s *Server) DRFailback(ctx context.Context, req *pb.DRFailbackRequest) (*pb.DRFailbackResponse, error) {
	resp := &pb.DRFailbackResponse{}
	fail := func(format string, args ...any) (*pb.DRFailbackResponse, error) {
		resp.Success, resp.Message = false, fmt.Sprintf(format, args...)
		return resp, nil
	}
	step := func(format string, args ...any) { resp.Steps = append(resp.Steps, fmt.Sprintf(format, args...)) }

	name := strings.TrimSpace(req.GetName())
	if name == "" {
		return fail("resource name is required")
	}
	if s.ctrl.db == nil {
		return fail("database not available")
	}
	dbRes, err := s.ctrl.db.GetResource(ctx, name)
	if err != nil || dbRes == nil {
		return fail("resource %q not found", name)
	}
	if !dbRes.WANMode || dbRes.DRNode == "" {
		return fail("%q is not a WAN resource with a DR node", name)
	}
	primaries := without(splitCSV(dbRes.Nodes), dbRes.DRNode)
	if len(primaries) == 0 {
		return fail("%q has no primary-site node to fail back to", name)
	}
	target := strings.TrimSpace(req.GetNode())
	if target == "" {
		target = primaries[0]
	}
	if !containsString(primaries, target) {
		return fail("%s is not a primary-site node of %s (those are %s)", target, name, strings.Join(primaries, ", "))
	}

	rm := s.resources
	dr := dbRes.DRNode
	drAddr := s.ctrl.ResolveHost(dr)

	drState, err := rm.readDrbdStatus(ctx, drAddr, name)
	if err != nil {
		return fail("read %s on the DR node %s: %v", name, dr, err)
	}
	if drState.Role != "Primary" {
		for _, p := range primaries {
			if st, err := rm.readDrbdStatus(ctx, s.ctrl.ResolveHost(p), name); err == nil && st.Role == "Primary" {
				resp.Success, resp.Phase = true, failbackDone
				resp.Message = fmt.Sprintf("%s is already Primary on the primary site (%s)", name, p)
				return resp, nil
			}
		}
	}

	// Phase 1: every primary-site node that is not a connected, in-sync peer
	// of the DR gives up what it wrote after the cut and rejoins.
	var rejoined []string
	for _, p := range primaries {
		if drPeerInSync(drState, rm.controller.nodes.GetDRBDNameByRef(p)) {
			continue
		}
		if connected(drState, rm.controller.nodes.GetDRBDNameByRef(p)) {
			continue // connected and resyncing; nothing to redo
		}
		addr := s.ctrl.ResolveHost(p)
		if open, err := rm.deviceOpen(ctx, addr, name); err != nil {
			return fail("check %s on %s: %v", name, p, err)
		} else if open {
			return fail("%s is in use on %s (mounted or held open): stop the service and unmount it there first — "+
				"what %s wrote after the failover is discarded", name, p, p)
		}
		script := fmt.Sprintf(`drbdadm disconnect --force %[1]s 2>/dev/null
drbdadm secondary %[1]s 2>/dev/null
drbdadm up %[1]s 2>/dev/null
drbdadm disconnect --force %[1]s 2>/dev/null
drbdadm connect --discard-my-data %[1]s`, name)
		if err := rm.execAllSuccess(ctx, []string{addr}, "echo "+base64Std(script)+" | base64 -d | sudo /bin/bash",
			fmt.Sprintf("rejoin %s on %s", name, p)); err != nil {
			return fail("%v", err)
		}
		rejoined = append(rejoined, p)
		step("%s: discarded its writes since the failover and reconnected", p)
	}
	if len(rejoined) > 0 {
		rep := s.repairWanLegs(ctx, dbRes, false, false)
		if !rep.Success {
			return fail("tunnels to the DR: %s", rep.Message)
		}
		step("tunnels rebuilt")
		if err := rm.execAllSuccess(ctx, []string{drAddr}, "sudo drbdadm connect "+name+" 2>/dev/null; true",
			"reconnect the DR"); err != nil {
			return fail("%v", err)
		}
		step("%s: reconnected to the primary site", dr)
	}

	// Phase 2: wait for the primary site to catch up with the DR.
	deadline := time.Now().Add(time.Duration(req.GetWaitSeconds()) * time.Second)
	for {
		drState, err = rm.readDrbdStatus(ctx, drAddr, name)
		if err != nil {
			return fail("read %s on the DR node %s: %v", name, dr, err)
		}
		lagging := laggingPeers(drState, primaries, rm.controller.nodes.GetDRBDNameByRef)
		if len(lagging) == 0 {
			break
		}
		if !time.Now().Before(deadline) {
			resp.Success, resp.Phase = true, failbackResyncing
			resp.Message = fmt.Sprintf("the primary site is resyncing from the DR (%s); run dr-failback again to finish",
				strings.Join(lagging, ", "))
			return resp, nil
		}
		select {
		case <-ctx.Done():
			return fail("%v", ctx.Err())
		case <-time.After(failbackPollInterval):
		}
	}
	step("primary site in sync with the DR")

	// Phase 3: swap the roles back. The DR must not be in use: demoting under
	// a mounted filesystem fails, and the service would lose its disk.
	if drState.Role == "Primary" {
		if open, err := rm.deviceOpen(ctx, drAddr, name); err != nil {
			return fail("check %s on %s: %v", name, dr, err)
		} else if open {
			resp.Success, resp.Phase = false, failbackResyncing
			resp.Message = fmt.Sprintf("the primary site is in sync, but %s is still in use on the DR node %s: "+
				"stop the service and unmount it there, then run dr-failback again", name, dr)
			return resp, nil
		}
		if err := rm.execAllSuccess(ctx, []string{drAddr}, "sudo drbdadm secondary "+name, "demote the DR"); err != nil {
			return fail("%v", err)
		}
		step("%s: Secondary", dr)
	}
	if err := rm.SetPrimary(ctx, name, target, false); err != nil {
		return fail("promote %s on %s: %v", name, target, err)
	}
	step("%s: Primary", target)
	s.ctrl.logger.Info("DR failback complete", zap.String("resource", name), zap.String("primary", target))
	resp.Success, resp.Phase = true, failbackDone
	resp.Message = fmt.Sprintf("%s is Primary on %s again; mount it and start the service there", name, target)
	return resp, nil
}

// readDrbdStatus reads one node's view of a resource.
func (rm *ResourceManager) readDrbdStatus(ctx context.Context, address, resource string) (*drbdsetupStatus, error) {
	res, err := rm.deployment.DRBDStatusJSON(ctx, []string{address}, resource)
	if err != nil {
		return nil, err
	}
	for _, hr := range res.Hosts {
		if !hr.Success {
			return nil, fmt.Errorf("%s", hostFailure(hr))
		}
		var all []drbdsetupStatus
		if err := json.Unmarshal([]byte(strings.TrimSpace(hr.Output)), &all); err != nil {
			return nil, fmt.Errorf("decode drbdsetup status: %w", err)
		}
		if len(all) == 0 {
			return nil, fmt.Errorf("%s is not up there", resource)
		}
		return &all[0], nil
	}
	return nil, fmt.Errorf("no answer from %s", address)
}

// deviceOpen reports whether anything holds the resource's device open on the
// node — a mounted filesystem, a VM, a database with the raw device.
func (rm *ResourceManager) deviceOpen(ctx context.Context, address, resource string) (bool, error) {
	res, err := rm.deployment.Exec(ctx, []string{address}, "sudo drbdsetup status "+resource+" --verbose")
	if err != nil {
		return false, err
	}
	for _, hr := range res.Hosts {
		if !hr.Success {
			return false, fmt.Errorf("%s", hostFailure(hr))
		}
		return strings.Contains(hr.Output, "open:yes"), nil
	}
	return false, fmt.Errorf("no answer from %s", address)
}

// connected reports whether the DR's connection to the named peer is up.
func connected(st *drbdsetupStatus, peer string) bool {
	for _, c := range st.Connections {
		if c.Name == peer {
			return c.ConnectionState == "Connected"
		}
	}
	return false
}

// drPeerInSync reports whether the DR sees the named peer connected with every
// volume UpToDate.
func drPeerInSync(st *drbdsetupStatus, peer string) bool {
	for _, c := range st.Connections {
		if c.Name != peer {
			continue
		}
		if c.ConnectionState != "Connected" || len(c.PeerDevices) == 0 {
			return false
		}
		for _, pd := range c.PeerDevices {
			if pd.PeerDiskState != "UpToDate" {
				return false
			}
		}
		return true
	}
	return false
}

// laggingPeers names the primary-site nodes not yet in sync with the DR, with
// resync progress where DRBD reports it.
func laggingPeers(st *drbdsetupStatus, primaries []string, drbdName func(string) string) []string {
	var out []string
	for _, p := range primaries {
		name := drbdName(p)
		if drPeerInSync(st, name) {
			continue
		}
		desc := p
		for _, c := range st.Connections {
			if c.Name != name {
				continue
			}
			desc = fmt.Sprintf("%s %s", p, c.ConnectionState)
			for _, pd := range c.PeerDevices {
				if pd.Done != nil {
					desc = fmt.Sprintf("%s %.0f%%", p, *pd.Done)
				}
			}
		}
		out = append(out, desc)
	}
	return out
}
