package controller

import (
	"context"
	"fmt"
	"strings"

	"go.uber.org/zap"

	"github.com/liliang-cn/sds/pkg/database"
	"github.com/liliang-cn/sds/pkg/wanproxy"
)

// AddDR attaches an asynchronous off-site replica to a resource that is already
// running, turning a LAN cluster into the two-site shape without recreating it.
//
// WAN mode could only be chosen when a resource was created. That is the wrong
// time to have to decide: off-site DR is exactly the thing an operator adds
// *after* a service has proven it matters, and the only way to get there was to
// destroy a serving resource and rebuild it — which for anything holding real
// data means an outage and a restore.
//
// Everything here is additive. The existing replicas keep their synchronous
// mesh and their addresses; the DR joins through one sds-proxy leg per replica
// (DRBD 9 is a full mesh, so a failover inside the primary site must not land
// on a node with no path to the DR). The mounted filesystem is never touched,
// so a promoted resource keeps serving throughout.
func (rm *ResourceManager) AddDR(ctx context.Context, resource, drNode, drEndpoint string, wanPort uint32, egressAddr string) error {
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
	if dbRes.WANMode {
		return fmt.Errorf("resource %q already replicates to DR node %q; remove it first to change the DR site",
			resource, dbRes.DRNode)
	}

	drNode = strings.TrimSpace(drNode)
	drEndpoint = strings.TrimSpace(drEndpoint)
	if drNode == "" {
		return fmt.Errorf("a DR node is required")
	}
	if drEndpoint == "" {
		return fmt.Errorf("a DR endpoint is required (the address the primary site dials)")
	}
	if rm.controller.nodes.GetNodeAddressByName(drNode) == "" {
		return fmt.Errorf("DR node %q is not a registered node", drNode)
	}

	primaries := splitCSV(dbRes.Nodes)
	for _, n := range primaries {
		if n == drNode {
			return fmt.Errorf("node %q already holds a replica of %q; the DR site must be a different node", drNode, resource)
		}
	}
	for _, n := range splitCSV(dbRes.DisklessNodes) {
		if n == drNode {
			return fmt.Errorf("node %q is the quorum tiebreaker of %q; it cannot also be the DR site", drNode, resource)
		}
	}
	if len(primaries) == 0 {
		return fmt.Errorf("resource %q has no replicas to replicate from", resource)
	}

	if wanPort == 0 {
		wanPort = randomWANPort()
		rm.controller.logger.Info("auto-allocated WAN proxy port",
			zap.String("resource", resource), zap.Uint32("wan_port", wanPort))
	}

	drAddr := rm.controller.ResolveHost(drNode)
	primaryAddrs := make([]string, 0, len(primaries))
	for _, n := range primaries {
		primaryAddrs = append(primaryAddrs, rm.controller.ResolveHost(n))
	}

	rm.controller.logger.Info("Adding DR site to running resource",
		zap.String("resource", resource),
		zap.Strings("primaries", primaries),
		zap.String("dr_node", drNode),
		zap.String("dr_endpoint", drEndpoint),
		zap.Uint32("wan_port", wanPort))

	// The DR inherits the resource's minors; make sure they are free there
	// before anything is provisioned.
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
	if err := rm.assertMinorsFreeOn(ctx, drAddr, resource, resourceMinors(liveConfig)); err != nil {
		return err
	}

	// The DR node needs backing volumes of its own: it is a full replica, not a
	// diskless voter. Sizes and pools come from the resource record, which is
	// where create wrote them.
	dbVols, err := rm.controller.db.ListVolumes(ctx, resource)
	if err != nil {
		return fmt.Errorf("list volumes of %q: %w", resource, err)
	}
	if len(dbVols) == 0 {
		return fmt.Errorf("resource %q has no recorded volumes to replicate", resource)
	}
	for _, v := range dbVols {
		if err := rm.createBackingVolume(ctx, []string{drAddr}, []string{drNode},
			"lvm", v.Pool, v.VolumeName, uint32(v.SizeGB)); err != nil {
			return fmt.Errorf("create backing volume %s/%s on DR node %q: %w", v.Pool, v.VolumeName, drNode, err)
		}
	}

	// Proxy first: in WAN mode DRBD connects to a loopback port the proxy owns,
	// so bringing DRBD up first would find nothing listening.
	multi := wanproxy.MultiSpec{
		Resource:          resource,
		PrimaryNodeAddrs:  primaryAddrs,
		DRNodeAddr:        drAddr,
		DRPublicEndpoint:  drEndpoint,
		BaseWANPort:       int(wanPort),
		BaseDRBDPort:      dbRes.Port,
		PrimaryEgressAddr: egressAddr,
		BinaryPath:        rm.wanproxyBinaryPath(),
	}
	if err := wanproxy.ProvisionMulti(ctx, rm.wanproxyDeployClient(), multi); err != nil {
		return fmt.Errorf("provision WAN proxy for %s: %w", resource, err)
	}

	// Rewrite the LIVE config rather than regenerating from the record: the
	// resource row does not carry the storage type or DRBD option overrides, so
	// a regenerated file would silently drop them (and any manual tuning).
	newConfig, err := rm.addDRToConfig(liveConfig, resource, drNode, primaries,
		splitCSV(dbRes.DisklessNodes), drAddr, dbVols, dbRes.Port)
	if err != nil {
		return fmt.Errorf("rewrite config for DR site: %w", err)
	}

	allHosts := append(append([]string{}, primaryAddrs...), drAddr)
	for _, n := range splitCSV(dbRes.DisklessNodes) {
		allHosts = append(allHosts, rm.controller.ResolveHost(n))
	}
	if _, err := rm.deployment.DistributeConfig(ctx, allHosts, newConfig, resPath); err != nil {
		return fmt.Errorf("distribute DR config: %w", err)
	}

	// Metadata on the DR only — the existing replicas keep theirs.
	if err := rm.execAllSuccess(ctx, []string{drAddr},
		fmt.Sprintf("sudo drbdadm create-md --force %s", resource),
		"create metadata on DR node"); err != nil {
		return err
	}

	// Let the running peers learn the new connection, then bring the DR up.
	if err := rm.execAllSuccess(ctx, allHosts,
		fmt.Sprintf("sudo drbdadm adjust %s", resource),
		"adjust peers for DR site"); err != nil {
		return err
	}
	upRes, err := rm.deployment.Exec(ctx, []string{drAddr}, fmt.Sprintf("sudo drbdadm up %s", resource))
	if err != nil {
		return fmt.Errorf("bring up DR replica: %w", err)
	}
	if !upRes.AllSuccess() {
		detail := upRes.FailureDetails()
		if !strings.Contains(detail, "exists already") && !strings.Contains(detail, "already? still?") {
			return fmt.Errorf("bring up DR replica on %q: %s", drNode, detail)
		}
	}

	// Record it last, so a failure above leaves the DB describing reality.
	dbRes.WANMode = true
	dbRes.DRNode = drNode
	dbRes.DREndpoint = drEndpoint
	dbRes.WANPort = int(wanPort)
	dbRes.WANEgressAddress = egressAddr
	if !strings.Contains(","+dbRes.Nodes+",", ","+drNode+",") {
		dbRes.Nodes = dbRes.Nodes + "," + drNode
	}
	if err := rm.controller.db.SaveResource(ctx, dbRes); err != nil {
		return fmt.Errorf("record DR site: %w", err)
	}

	rm.controller.logger.Info("DR site added; initial sync runs in the background",
		zap.String("resource", resource), zap.String("dr_node", drNode))
	return nil
}

// addDRToConfig rewrites a LAN resource config into the two-site shape.
//
// Three edits, all additive to the primary site:
//   - a host stanza for the DR node, with its own volumes and a fresh node-id;
//   - connection-mesh narrowed to the primary site, because a mesh entry for the
//     DR would pair it with the replicas on their LAN addresses, which cannot
//     reach it;
//   - one explicit `connection` per replica carrying the loopback endpoints of
//     that replica's WAN leg, plus protocol A and pull-ahead so the WAN cannot
//     stall writes in the primary site.
func (rm *ResourceManager) addDRToConfig(content, resource, drNode string, primaries, tiebreakers []string,
	drAddr string, vols []*database.Volume, port int) (string, error) {

	drName := rm.controller.nodes.GetDRBDNameByRef(drNode)
	blocks := parseOnBlocks(content)
	for _, b := range blocks {
		if b.name == drName {
			return "", fmt.Errorf("node %q is already in the config of %q", drNode, resource)
		}
	}
	nextID := 0
	for _, b := range blocks {
		if b.nodeID >= nextID {
			nextID = b.nodeID + 1
		}
	}

	// 1. The DR host stanza. Its address is loopback: every connection it has
	//    is a WAN leg terminated by its local acceptor.
	var dr strings.Builder
	fmt.Fprintf(&dr, "\n    on %s {\n", drName)
	fmt.Fprintf(&dr, "        address   127.0.0.1:%d;\n", port)
	fmt.Fprintf(&dr, "        node-id   %d;\n", nextID)
	for _, v := range vols {
		fmt.Fprintf(&dr, "        volume %d {\n", v.VolumeID)
		fmt.Fprintf(&dr, "            device    minor %d;\n", minorForVolume(content, v.VolumeID))
		fmt.Fprintf(&dr, "            disk      /dev/%s/%s;\n", v.Pool, v.VolumeName)
		fmt.Fprintf(&dr, "            meta-disk internal;\n")
		fmt.Fprintf(&dr, "        }\n")
	}
	fmt.Fprintf(&dr, "    }\n")

	// 2 + 3. Insert before the resource's closing brace, dropping any existing
	//        connection-mesh so it can be re-emitted for the primary site only.
	lines := strings.Split(content, "\n")
	out := make([]string, 0, len(lines)+40)
	inMesh := false
	closeIdx := -1
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "connection-mesh") {
			inMesh = true
			continue
		}
		if inMesh {
			if strings.Contains(line, "}") {
				inMesh = false
			}
			continue
		}
		if trimmed == "}" {
			closeIdx = len(out) // resource-level close; keep updating, last wins
		}
		out = append(out, line)
	}
	if closeIdx < 0 {
		return "", fmt.Errorf("malformed config for %q: no closing brace", resource)
	}

	var tail strings.Builder
	tail.WriteString(dr.String())

	lanHosts := make([]string, 0, len(primaries)+len(tiebreakers))
	for _, n := range append(append([]string{}, primaries...), tiebreakers...) {
		lanHosts = append(lanHosts, rm.controller.nodes.GetDRBDNameByRef(n))
	}
	if len(lanHosts) > 1 {
		tail.WriteString("\n    connection-mesh {\n        hosts")
		for _, h := range lanHosts {
			fmt.Fprintf(&tail, " %s", h)
		}
		tail.WriteString(";\n    }\n")
	}

	for i, p := range primaries {
		legPort := port + i
		fmt.Fprintf(&tail, "\n    connection {\n")
		fmt.Fprintf(&tail, "        host %s address 127.0.0.1:%d;\n",
			rm.controller.nodes.GetDRBDNameByRef(p), legPort+wanDRBDBindOffset)
		fmt.Fprintf(&tail, "        host %s address 127.0.0.1:%d;\n", drName, legPort)
		fmt.Fprintf(&tail, "        net {\n")
		fmt.Fprintf(&tail, "            protocol A;\n")
		fmt.Fprintf(&tail, "            on-congestion pull-ahead;\n")
		fmt.Fprintf(&tail, "            congestion-fill 400M;\n")
		fmt.Fprintf(&tail, "        }\n")
		fmt.Fprintf(&tail, "    }\n")
	}

	final := append([]string{}, out[:closeIdx]...)
	final = append(final, strings.Split(strings.TrimRight(tail.String(), "\n"), "\n")...)
	final = append(final, out[closeIdx:]...)
	return strings.Join(final, "\n"), nil
}

// minorForVolume finds the device minor an existing config assigns to a volume,
// so the DR's stanza uses the same one (a resource's minor is cluster-wide).
func minorForVolume(content string, volumeID int) int {
	inVol := false
	depth := 0
	for _, line := range strings.Split(content, "\n") {
		trimmed := strings.TrimSpace(line)
		if !inVol && strings.HasPrefix(trimmed, fmt.Sprintf("volume %d", volumeID)) {
			inVol = true
			depth = strings.Count(line, "{") - strings.Count(line, "}")
			continue
		}
		if inVol {
			if m, ok := parseAnyDeviceMinor(line); ok {
				return m
			}
			depth += strings.Count(line, "{") - strings.Count(line, "}")
			if depth <= 0 {
				inVol = false
			}
		}
	}
	// Volume 0 may be written without an explicit `volume 0 {` wrapper.
	if volumeID == 0 {
		for _, line := range strings.Split(content, "\n") {
			if m, ok := parseAnyDeviceMinor(line); ok {
				return m
			}
		}
	}
	return volumeID
}
