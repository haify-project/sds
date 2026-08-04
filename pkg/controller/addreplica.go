package controller

import (
	"context"
	"fmt"
	"strings"

	"go.uber.org/zap"

	"github.com/liliang-cn/sds/pkg/database"
	"github.com/liliang-cn/sds/pkg/deployment"
	"github.com/liliang-cn/sds/pkg/wanproxy"
)

// AddReplica adds a diskful local replica to a resource that is already running.
//
// SDS could grow a resource sideways — a diskless client, a quorum tiebreaker,
// an off-site DR — but not the thing an operator most often wants: one more real
// copy, here, on a node that was not in the cluster when the resource was made.
// The only route was to destroy the resource and rebuild it, which for anything
// holding data means an outage and a restore.
//
// The new node joins the synchronous mesh as a peer of every existing replica.
// On a resource that also has an off-site DR it additionally gets its own WAN
// leg, because DRBD 9 is a full mesh: a replica the DR cannot reach is a replica
// that silently ends replication the moment it is promoted.
func (rm *ResourceManager) AddReplica(ctx context.Context, resource, node string) error {
	if rm.deployment == nil {
		return fmt.Errorf("deployment client not set")
	}
	if rm.controller.db == nil {
		return fmt.Errorf("database not available")
	}

	dbRes, err := rm.controller.db.GetResource(ctx, resource)
	if err != nil || dbRes == nil {
		return fmt.Errorf("resource %q not found", resource)
	}

	node = strings.TrimSpace(node)
	if node == "" {
		return fmt.Errorf("a node is required")
	}
	if rm.controller.nodes.GetNodeAddressByName(node) == "" {
		return fmt.Errorf("node %q is not a registered node", node)
	}

	existing := splitCSV(dbRes.Nodes)
	for _, n := range existing {
		if n == node {
			return fmt.Errorf("node %q already holds a replica of %q", node, resource)
		}
	}
	// A diskless member could in principle be upgraded in place, but that is a
	// different operation with different risks (it has metadata but no data), so
	// refuse rather than half-do it.
	for _, n := range splitCSV(dbRes.DisklessNodes) {
		if n == node {
			return fmt.Errorf("node %q is the quorum tiebreaker of %q; remove it with `ha set-tiebreaker` before making it a replica",
				node, resource)
		}
	}
	for _, n := range splitCSV(dbRes.DisklessClients) {
		if n == node {
			return fmt.Errorf("node %q is a diskless client of %q; detach it before making it a replica", node, resource)
		}
	}

	// The DR is a member of Nodes but not a primary-site replica; keep them apart.
	primaries := existing
	if dbRes.WANMode && dbRes.DRNode != "" {
		primaries = make([]string, 0, len(existing))
		for _, n := range existing {
			if n != dbRes.DRNode {
				primaries = append(primaries, n)
			}
		}
	}
	if len(primaries) == 0 {
		return fmt.Errorf("resource %q has no primary-site replica to copy from", resource)
	}

	newAddr := rm.controller.ResolveHost(node)
	primaryAddrs := make([]string, 0, len(primaries))
	for _, n := range primaries {
		primaryAddrs = append(primaryAddrs, rm.controller.ResolveHost(n))
	}

	rm.controller.logger.Info("Adding replica to running resource",
		zap.String("resource", resource),
		zap.String("node", node),
		zap.Strings("existing", primaries),
		zap.Bool("wan", dbRes.WANMode))

	hosts, err := rm.resourceHosts(ctx, resource)
	if err != nil {
		return err
	}
	if len(hosts) == 0 {
		return fmt.Errorf("resource %q has no reachable hosts", resource)
	}
	resPath := fmt.Sprintf("/etc/drbd.d/%s.res", resource)
	catRes, err := rm.deployment.Exec(ctx, []string{hosts[0]}, "cat "+resPath)
	if err != nil {
		return fmt.Errorf("read resource config: %w", err)
	}
	liveConfig, readOK := "", false
	for _, r := range catRes.Hosts {
		liveConfig, readOK = r.Output, r.Success
		break
	}
	if !readOK || strings.TrimSpace(liveConfig) == "" {
		return fmt.Errorf("read resource config for %q: %s", resource, catRes.FailureDetails())
	}

	if err := rm.assertMinorsFreeOn(ctx, newAddr, resource, resourceMinors(liveConfig)); err != nil {
		return err
	}
	// A diskful peer needs a bitmap slot on every existing replica, and slots are
	// allocated once, at create-md time. Check before provisioning anything.
	if err := rm.assertBitmapSlotFree(ctx, primaryAddrs, primaries, resource); err != nil {
		return err
	}

	dbVols, err := rm.controller.db.ListVolumes(ctx, resource)
	if err != nil {
		return fmt.Errorf("list volumes of %q: %w", resource, err)
	}
	if len(dbVols) == 0 {
		return fmt.Errorf("resource %q has no recorded volumes to replicate", resource)
	}
	// Size from what the existing replicas actually export, not from the recorded
	// request: metadata is carved out of the same device, so a volume that was
	// extended exports more than its record implies, and DRBD refuses a peer that
	// is even one sector short.
	backingSizes, err := rm.primaryBackingSizes(ctx, primaryAddrs, resource, len(dbVols))
	if err != nil {
		return err
	}

	tiebreakers := splitCSV(dbRes.DisklessNodes)
	lanHosts := append([]string{}, primaryAddrs...)
	for _, n := range tiebreakers {
		lanHosts = append(lanHosts, rm.controller.ResolveHost(n))
	}
	allHosts := append(append([]string{}, lanHosts...), newAddr)
	if dbRes.WANMode && dbRes.DRNode != "" {
		allHosts = append(allHosts, rm.controller.ResolveHost(dbRes.DRNode))
	}

	succeeded := false
	defer func() {
		if succeeded {
			return
		}
		rm.undoAddReplica(context.WithoutCancel(ctx), resource, resPath, liveConfig,
			lanHosts, newAddr, dbVols)
	}()

	for i, v := range dbVols {
		if cerr := rm.createBackingVolumeOn(ctx, newAddr, v.Pool, v.VolumeName, backingSizes[i]); cerr != nil {
			return fmt.Errorf("create backing volume %s/%s on %q: %w", v.Pool, v.VolumeName, node, cerr)
		}
	}

	// On a two-site resource the newcomer needs its own tunnel to the DR before
	// DRBD is told about the connection. Existing legs keep their index, so their
	// ports do not move; they are rewritten with identical content and restart,
	// which the async DR rides out by reconnecting.
	newBind := 0
	if dbRes.WANMode && dbRes.DRNode != "" {
		withNew := append(append([]string{}, primaryAddrs...), newAddr)
		multi := wanproxy.MultiSpec{
			Resource:          resource,
			PrimaryNodeAddrs:  withNew,
			DRNodeAddr:        rm.controller.ResolveHost(dbRes.DRNode),
			DRPublicEndpoint:  dbRes.DREndpoint,
			BaseWANPort:       dbRes.WANPort,
			BaseDRBDPort:      dbRes.Port,
			PrimaryEgressAddr: dbRes.WANEgressAddress,
			BinaryFor: rm.wanproxyBinaryResolver(ctx,
				append(append([]string{}, withNew...), rm.controller.ResolveHost(dbRes.DRNode))),
		}
		if err := wanproxy.ProvisionMulti(ctx, rm.wanproxyDeployClient(), multi); err != nil {
			return fmt.Errorf("provision WAN leg for %q: %w", node, err)
		}
		// After the push, not before: the controller may well have supplied the
		// binary itself, and checking first would reject a case that works.
		if err := rm.assertWANProxyBinary(ctx, []string{newAddr}, []string{node}); err != nil {
			return err
		}
		ports, perr := rm.pickWANBindPorts(ctx, []string{newAddr}, dbRes.Port)
		if perr != nil {
			return perr
		}
		newBind = ports[0]
	}

	newConfig, err := rm.addReplicaToConfig(liveConfig, resource, node, newAddr, dbVols,
		dbRes.Port, len(primaries), newBind, dbRes.WANMode && dbRes.DRNode != "", dbRes.DRNode)
	if err != nil {
		return fmt.Errorf("rewrite config for new replica: %w", err)
	}
	// Growing the primary site moves its majority; recompute rather than leaving
	// a stale number that no longer describes the site.
	if dbRes.WANMode && dbRes.DRNode != "" {
		newConfig = setLocalSiteQuorum(newConfig, len(primaries)+1+len(tiebreakers))
	}

	if _, err := rm.deployment.DistributeConfig(ctx, allHosts, newConfig, resPath); err != nil {
		return fmt.Errorf("distribute config with new replica: %w", err)
	}

	if err := rm.execAllSuccess(ctx, []string{newAddr},
		fmt.Sprintf("sudo drbdadm create-md --max-peers=%d --force %s", deployment.DefaultMaxPeers, resource),
		"create metadata on the new replica"); err != nil {
		return err
	}
	if err := rm.execAllSuccess(ctx, allHosts,
		fmt.Sprintf("sudo drbdadm adjust %s", resource),
		"adjust peers for the new replica"); err != nil {
		return err
	}
	upRes, err := rm.deployment.Exec(ctx, []string{newAddr}, fmt.Sprintf("sudo drbdadm up %s", resource))
	if err != nil {
		return fmt.Errorf("bring up the new replica: %w", err)
	}
	if !upRes.AllSuccess() {
		detail := upRes.FailureDetails()
		if !strings.Contains(detail, "exists already") && !strings.Contains(detail, "already? still?") {
			return fmt.Errorf("bring up replica on %q: %s", node, detail)
		}
	}

	succeeded = true

	// Record it last, so a failure above leaves the row describing reality. The
	// new node goes before the DR, which by convention is the final entry.
	updated := append([]string{}, primaries...)
	updated = append(updated, node)
	if dbRes.WANMode && dbRes.DRNode != "" {
		updated = append(updated, dbRes.DRNode)
	}
	dbRes.Nodes = strings.Join(updated, ",")
	if err := rm.controller.db.SaveResource(ctx, dbRes); err != nil {
		return fmt.Errorf("record the new replica: %w", err)
	}

	rm.controller.logger.Info("Replica added; initial sync runs in the background",
		zap.String("resource", resource), zap.String("node", node))
	return nil
}

// addReplicaToConfig adds a diskful node to a live resource config.
func (rm *ResourceManager) addReplicaToConfig(content, resource, node, addr string,
	vols []*database.Volume, port, legIndex, bindPort int, wan bool, drNode string) (string, error) {

	name := rm.controller.nodes.GetDRBDNameByRef(node)
	blocks := parseOnBlocks(content)
	for _, b := range blocks {
		if b.name == name {
			return "", fmt.Errorf("node %q is already in the config of %q", node, resource)
		}
	}
	nextID := 0
	for _, b := range blocks {
		if b.nodeID >= nextID {
			nextID = b.nodeID + 1
		}
	}

	var stanza strings.Builder
	fmt.Fprintf(&stanza, "\n    on %s {\n", name)
	fmt.Fprintf(&stanza, "        address   %s:%d;\n", addr, port)
	fmt.Fprintf(&stanza, "        node-id   %d;\n", nextID)
	for _, v := range vols {
		fmt.Fprintf(&stanza, "        volume %d {\n", v.VolumeID)
		fmt.Fprintf(&stanza, "            device    minor %d;\n", minorForVolume(content, v.VolumeID))
		fmt.Fprintf(&stanza, "            disk      /dev/%s/%s;\n", v.Pool, v.VolumeName)
		fmt.Fprintf(&stanza, "            meta-disk internal;\n")
		fmt.Fprintf(&stanza, "        }\n")
	}
	fmt.Fprintf(&stanza, "    }\n")

	out, err := insertBeforeResourceClose(stripConnectionMesh(content), stanza.String())
	if err != nil {
		return "", err
	}

	// Rebuild the mesh over the LAN hosts, which now include the newcomer. A
	// WAN-attached host stays out: a mesh entry would pair it with every other
	// host on that host's LAN address, which is unroutable from the other site.
	mesh := lanHostNames(parseOnBlocks(out))
	if len(mesh) > 1 {
		out, err = insertBeforeResourceClose(out, buildConnectionMesh(mesh))
		if err != nil {
			return "", err
		}
	}

	// Two-site resource: the newcomer also needs an explicit leg to the DR, on
	// the same port layout the other legs use.
	if wan && drNode != "" {
		legPort := port + legIndex
		var leg strings.Builder
		fmt.Fprintf(&leg, "\n    connection {\n")
		fmt.Fprintf(&leg, "        host %s address 127.0.0.1:%d;\n", name, bindPort)
		fmt.Fprintf(&leg, "        host %s address 127.0.0.1:%d;\n",
			rm.controller.nodes.GetDRBDNameByRef(drNode), legPort)
		fmt.Fprintf(&leg, "        net {\n")
		fmt.Fprintf(&leg, "            protocol A;\n")
		fmt.Fprintf(&leg, "            on-congestion pull-ahead;\n")
		fmt.Fprintf(&leg, "            congestion-fill 400M;\n")
		fmt.Fprintf(&leg, "        }\n")
		fmt.Fprintf(&leg, "    }\n")
		out, err = insertBeforeResourceClose(out, leg.String())
		if err != nil {
			return "", err
		}
	}
	return out, nil
}

// undoAddReplica returns the cluster to its previous shape after a failed add.
//
// The surviving members go back on the old config first, so they stop trying to
// reach a peer that is about to disappear; only then is the newcomer dismantled.
// Every step is best-effort and logged — the caller is already returning the
// failure that got us here, and a rollback that aborts halfway is worse than one
// that keeps going.
func (rm *ResourceManager) undoAddReplica(ctx context.Context, resource, resPath, originalConfig string,
	lanHosts []string, newAddr string, vols []*database.Volume) {

	log := rm.controller.logger.With(zap.String("resource", resource), zap.String("node", newAddr))
	log.Warn("Rolling back replica addition")

	if len(lanHosts) > 0 && strings.TrimSpace(originalConfig) != "" {
		if _, err := rm.deployment.DistributeConfig(ctx, lanHosts, originalConfig, resPath); err != nil {
			log.Error("Rollback: failed to restore the previous config", zap.Error(err))
		} else if _, err := rm.deployment.Exec(ctx, lanHosts,
			fmt.Sprintf("sudo drbdadm adjust %s", resource)); err != nil {
			log.Error("Rollback: failed to re-adjust the surviving members", zap.Error(err))
		}
	}

	if _, err := rm.deployment.Exec(ctx, []string{newAddr},
		fmt.Sprintf("sudo drbdadm down %s 2>/dev/null; sudo rm -f %s", resource, resPath)); err != nil {
		log.Error("Rollback: failed to tear down the new replica", zap.Error(err))
	}
	for _, v := range vols {
		if _, err := rm.deployment.Exec(ctx, []string{newAddr},
			fmt.Sprintf("sudo lvremove -f %s/%s", v.Pool, v.VolumeName)); err != nil {
			log.Error("Rollback: failed to remove a backing volume",
				zap.String("volume", v.Pool+"/"+v.VolumeName), zap.Error(err))
		}
	}
}

// wanproxyBinaryProbe reports whether the proxy binary is present and runnable.
const wanproxyBinaryProbe = `test -x /usr/local/bin/sds-proxy && echo present || echo missing`

// assertWANProxyBinary fails unless every node that will run a proxy leg has the
// binary.
//
// Skipping the push when the controller has no local copy is a reasonable
// default — most fleets stage sds-proxy with their image — but "assume it is
// pre-staged" is a bad thing to assume silently. A node without it gets a
// systemd unit that crash-loops with 203/EXEC, while DRBD reports only
// "Connecting". Nothing in that picture points at a missing file, and the two
// halves are on different machines: the unit fails at home, the symptom is a
// connection that never forms.
//
// A node that has never carried a WAN leg is exactly the node likely to be
// missing it — which is to say, every node this code path is about.
func (rm *ResourceManager) assertWANProxyBinary(ctx context.Context, hosts, nodes []string) error {
	for i, host := range hosts {
		res, err := rm.deployment.Exec(ctx, []string{host}, wanproxyBinaryProbe)
		if err != nil {
			return fmt.Errorf("check for the sds-proxy binary on %q: %w", nodes[i], err)
		}
		out := ""
		for _, r := range res.Hosts {
			out = strings.TrimSpace(r.Output)
			break
		}
		if strings.Contains(out, "missing") {
			return fmt.Errorf(
				"node %q has no executable %s, so its WAN leg would start and immediately fail "+
					"(systemd 203/EXEC, while DRBD reports only \"Connecting\"). Stage the binary there, "+
					"or put one on the controller at the same path so it can be pushed automatically",
				nodes[i], wanproxyLocalBinaryPath)
		}
	}
	return nil
}
