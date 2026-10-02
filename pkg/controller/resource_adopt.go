package controller

import (
	"context"
	"fmt"
	"strings"

	"github.com/haify-project/sds/pkg/database"
	"go.uber.org/zap"
)

// AdoptResult summarizes what AdoptResource recorded, so callers can display it.
type AdoptResult struct {
	Name     string
	Nodes    []string
	Port     uint32
	Protocol string
	Volumes  int
}

// AdoptResource imports an already-existing (foreign) DRBD resource — one
// created outside SDS, e.g. a hand-configured resource — into SDS management by
// writing its metadata into the SDS database. It lets subsequent SDS operations
// (MakeHa, gateways, ...) that require db.GetResource work against it.
//
// It NEVER creates or modifies the DRBD resource, its backing devices, or any
// data: no drbdadm create-md/up/down, no lvcreate/mkfs, no writes to the
// backing device. It only reads the live /etc/drbd.d/<name>.res on a reachable
// node and records what it finds. When nodes/port/protocol are supplied they
// override the auto-discovered values; anything left empty/zero is discovered
// from the .res. Adopting a resource that is already recorded simply refreshes
// the record (idempotent, no error).
func (rm *ResourceManager) AdoptResource(ctx context.Context, name string, nodes []string, port uint32, protocol string) (*AdoptResult, error) {
	if rm.deployment == nil {
		return nil, fmt.Errorf("deployment client not set")
	}
	if rm.controller.db == nil {
		return nil, fmt.Errorf("database not available")
	}
	name = strings.TrimSpace(name)
	if name == "" {
		return nil, fmt.Errorf("resource name is required")
	}

	// Normalize an explicit --nodes list (trim, drop blanks).
	var flagNodes []string
	for _, n := range nodes {
		if n = strings.TrimSpace(n); n != "" {
			flagNodes = append(flagNodes, n)
		}
	}

	// Ordered candidate hosts to read the .res from: an explicit --nodes flag
	// wins, then every registered node, then any statically configured host.
	candidates := rm.adoptCandidateHosts(ctx, flagNodes)
	if len(candidates) == 0 {
		return nil, fmt.Errorf("no reachable node to read DRBD config for %q: register a node or pass --nodes", name)
	}

	// Read the live config and confirm the resource actually exists. This is a
	// pure read — it never mutates the resource.
	configContent, usedHost, err := rm.readForeignResourceConfig(ctx, candidates, name)
	if err != nil {
		return nil, err
	}

	// Auto-discover from the config; flags override.
	discNodes := parseResourceConfigNodes(configContent)
	discPort := parsePortFromConfig(configContent)
	cfgVolumes := parseResourceConfigVolumes(configContent)

	adoptNodes := flagNodes
	if len(adoptNodes) == 0 {
		adoptNodes = discNodes
	}
	if len(adoptNodes) == 0 {
		return nil, fmt.Errorf("could not determine nodes for %q from %s:/etc/drbd.d/%s.res; pass --nodes", name, usedHost, name)
	}
	if port == 0 {
		port = discPort
	}
	if protocol == "" {
		protocol = "C"
	}

	// Persist SDS metadata only — the DRBD resource and its data are left
	// exactly as they are.
	resRecord := &database.Resource{
		Name:     name,
		Nodes:    strings.Join(adoptNodes, ","),
		Port:     int(port),
		Protocol: protocol,
		Replicas: len(adoptNodes),
	}
	if existing, err := rm.controller.db.GetResource(ctx, name); err == nil {
		resRecord.Profile = existing.Profile
		resRecord.Labels = cloneStringMap(existing.Labels)
		resRecord.CreatedAt = existing.CreatedAt
	}
	if err := rm.controller.db.SaveResource(ctx, resRecord); err != nil {
		return nil, fmt.Errorf("record adopted resource %q: %w", name, err)
	}

	savedVolumes := 0
	seenVolID := make(map[int]bool)
	for _, v := range cfgVolumes {
		// Skip diskless volume overrides (`disk none;` in a tiebreaker's `on`
		// section) and any volume with no backing disk — they carry no data to
		// record and would duplicate the data-bearing volume of the same ID.
		if v.DiskPath == "" || v.DiskPath == "none" {
			continue
		}
		if seenVolID[v.VolumeID] {
			continue
		}
		seenVolID[v.VolumeID] = true

		volumeName, pool := volumeNameAndPoolFromDiskPath(v.DiskPath)
		if volumeName == "" {
			volumeName = fmt.Sprintf("%s_vol%d", name, v.VolumeID)
		}
		// SizeGB is best-effort and left 0: adoption never probes/opens the
		// backing device to size it.
		volRecord := &database.Volume{
			ResourceName: name,
			VolumeName:   volumeName,
			VolumeID:     v.VolumeID,
			Pool:         pool,
			Device:       v.DiskPath,
		}
		if err := rm.controller.db.SaveVolume(ctx, volRecord); err != nil {
			return nil, fmt.Errorf("record adopted volume %d of %q: %w", v.VolumeID, name, err)
		}
		savedVolumes++
	}

	rm.controller.logger.Info("Adopted foreign DRBD resource into SDS management",
		zap.String("resource", name),
		zap.Strings("nodes", adoptNodes),
		zap.Uint32("port", port),
		zap.String("protocol", protocol),
		zap.Int("volumes", savedVolumes),
		zap.String("read_from", usedHost))

	return &AdoptResult{
		Name:     name,
		Nodes:    adoptNodes,
		Port:     port,
		Protocol: protocol,
		Volumes:  savedVolumes,
	}, nil
}

// adoptCandidateHosts builds the ordered, de-duplicated list of node addresses
// to try when reading a foreign resource's .res: explicit flag nodes first,
// then every registered node, then any statically configured host.
func (rm *ResourceManager) adoptCandidateHosts(ctx context.Context, flagNodes []string) []string {
	var hosts []string
	seen := make(map[string]bool)
	add := func(h string) {
		if h = strings.TrimSpace(h); h == "" || seen[h] {
			return
		}
		seen[h] = true
		hosts = append(hosts, h)
	}
	for _, n := range flagNodes {
		add(rm.controller.ResolveHost(n))
	}
	if rm.controller.nodes != nil {
		if list, err := rm.controller.nodes.ListNodes(ctx); err == nil {
			for _, n := range list {
				add(n.Address)
			}
		}
	}
	for _, h := range rm.GetHosts() {
		add(h)
	}
	return hosts
}

// readForeignResourceConfig reads /etc/drbd.d/<name>.res from the first
// candidate host that actually has it. It validates that the resource exists
// (a present, non-empty .res that declares the resource) so adoption never
// fabricates a record for a resource that is not really there. Returns the
// config content and the host it was read from.
func (rm *ResourceManager) readForeignResourceConfig(ctx context.Context, hosts []string, name string) (string, string, error) {
	cmd := fmt.Sprintf("cat /etc/drbd.d/%s.res 2>/dev/null || true", name)
	var lastErr error
	for _, host := range hosts {
		result, err := rm.deployment.Exec(ctx, []string{host}, cmd)
		if err != nil {
			lastErr = err
			continue
		}
		var content string
		for _, hr := range result.Hosts {
			if hr != nil && hr.Success {
				content = hr.Output
				break
			}
		}
		if strings.Contains(content, "resource "+name) ||
			(strings.Contains(content, "resource ") && strings.Contains(content, "on ")) {
			return content, host, nil
		}
	}
	if lastErr != nil {
		return "", "", fmt.Errorf("read DRBD config for %q: %w", name, lastErr)
	}
	return "", "", fmt.Errorf("DRBD resource %q not found: no /etc/drbd.d/%s.res on any reachable node — refusing to adopt a resource that does not exist", name, name)
}
