package controller

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/liliang-cn/sds/pkg/database"
	"go.uber.org/zap"
)

// Diskless clients let a node access a DRBD resource with no local replica: it
// connects over the network, fetches data from a diskful peer, and can be
// promoted Primary to read/write the volume. This is LINSTOR's "diskless
// client" and is what allows a Kubernetes pod to run on a node that stores none
// of the data (e.g. a diskless compute node) yet still mount the volume.
//
// At the DRBD `.res` level a diskless client is identical to a quorum
// tiebreaker — an `on <node>` block whose every volume overrides `disk none`.
// The distinction is purely in SDS semantics: a tiebreaker is never promoted or
// mounted (DisklessNodes), whereas a client is (DisklessClients). Attach/detach
// therefore reuse the same config surgery the tiebreaker path relies on, but
// track the node separately and never treat it as a mere vote.

// errDisklessAlreadyPresent is returned by addDisklessClientBlock when the node
// already has an `on` block in the config; callers treat it as idempotent.
var errDisklessAlreadyPresent = fmt.Errorf("node already present in resource config")

// errDisklessNotPresent is returned by removeDisklessClientBlock when the node
// has no `on` block to remove; callers treat it as idempotent.
var errDisklessNotPresent = fmt.Errorf("node not present in resource config")

// onBlock is a parsed `on <name> { ... node-id N; ... }` host stanza.
type onBlock struct {
	name   string
	nodeID int
	// loopback is true when the stanza's address is 127.0.0.1, which marks a
	// node reachable only through an sds-proxy WAN leg (the DR site, or the
	// primary of a single-replica WAN resource). Such a node is wired by an
	// explicit `connection` section and must never be put in a connection-mesh:
	// the mesh would pair it with every other host on that host's LAN address,
	// which is unroutable from the other site.
	loopback bool
}

// parseOnBlocks extracts every `on <host>` stanza from a DRBD resource config,
// in file order, along with each host's node-id. It is brace-aware so volume
// override blocks nested inside an `on` stanza do not confuse it.
func parseOnBlocks(content string) []onBlock {
	var blocks []onBlock
	var cur *onBlock
	depth := 0
	for _, line := range strings.Split(content, "\n") {
		trimmed := strings.TrimSpace(line)
		// An `on` stanza opens at resource level (depth 1, inside `resource {`).
		if cur == nil && depth == 1 && strings.HasPrefix(trimmed, "on ") && strings.Contains(trimmed, "{") {
			if fields := strings.Fields(trimmed); len(fields) >= 2 {
				cur = &onBlock{name: fields[1], nodeID: -1}
			}
		} else if cur != nil && strings.HasPrefix(trimmed, "address") &&
			strings.Contains(trimmed, "127.0.0.1") {
			cur.loopback = true
		} else if cur != nil && strings.HasPrefix(trimmed, "node-id") {
			if fields := strings.Fields(trimmed); len(fields) >= 2 {
				if id, err := strconv.Atoi(strings.TrimSuffix(fields[1], ";")); err == nil {
					cur.nodeID = id
				}
			}
		}
		depth += strings.Count(line, "{") - strings.Count(line, "}")
		// Back to resource level closes the current `on` stanza.
		if cur != nil && depth <= 1 {
			blocks = append(blocks, *cur)
			cur = nil
		}
	}
	return blocks
}

// meshNeeded reports whether a connection-mesh must be written for the LAN
// hosts.
//
// Two LAN hosts normally need no mesh: with no explicit connection sections at
// all, DRBD wires every pair implicitly. That stops being true the moment a
// WAN-attached host is present, because the file then carries explicit
// `connection` sections and the implicit pairing no longer applies — leaving
// two replicas with no connection to each other at all.
func meshNeeded(lan, all []string) bool {
	if len(lan) > 2 {
		return true
	}
	return len(lan) >= 2 && len(lan) < len(all)
}

// lanHostNames returns the hosts that belong in a connection-mesh: everything
// except the WAN-attached ones, which have their own explicit connections.
func lanHostNames(blocks []onBlock) []string {
	names := make([]string, 0, len(blocks))
	for _, b := range blocks {
		if b.loopback {
			continue
		}
		names = append(names, b.name)
	}
	return names
}

// onHostNames returns the host names of the given `on` blocks in order.
func onHostNames(blocks []onBlock) []string {
	names := make([]string, len(blocks))
	for i, b := range blocks {
		names[i] = b.name
	}
	return names
}

// dedupResourceVolumes keeps the first block seen per volume ID. A config that
// already carries diskless override blocks (`disk none`, same minor) reports
// each volume twice via parseResourceConfigVolumes; the resource-level block
// comes first, so first-wins yields the canonical (id, minor) set.
func dedupResourceVolumes(vols []resourceConfigVolume) []resourceConfigVolume {
	seen := make(map[int]bool, len(vols))
	var out []resourceConfigVolume
	for _, v := range vols {
		if v.Minor < 0 || seen[v.VolumeID] {
			continue
		}
		seen[v.VolumeID] = true
		out = append(out, v)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].VolumeID < out[j].VolumeID })
	return out
}

// insertBeforeResourceClose splices block in just before the resource's closing
// brace (the final `}` line). Returns an error on a malformed config.
func insertBeforeResourceClose(content, block string) (string, error) {
	lines := strings.Split(content, "\n")
	idx := -1
	for i := len(lines) - 1; i >= 0; i-- {
		if strings.TrimSpace(lines[i]) == "}" {
			idx = i
			break
		}
	}
	if idx < 0 {
		return "", fmt.Errorf("malformed resource config: no closing brace")
	}
	blockLines := strings.Split(strings.TrimRight(block, "\n"), "\n")
	out := make([]string, 0, len(lines)+len(blockLines))
	out = append(out, lines[:idx]...)
	out = append(out, blockLines...)
	out = append(out, lines[idx:]...)
	return strings.Join(out, "\n"), nil
}

// stripConnectionMesh removes any `connection-mesh { ... }` stanza. The mesh is
// rebuilt from scratch whenever the host set changes so it always lists exactly
// the current participants.
func stripConnectionMesh(content string) string {
	var out []string
	depth := 0
	skipping := false
	for _, line := range strings.Split(content, "\n") {
		trimmed := strings.TrimSpace(line)
		if !skipping && strings.HasPrefix(trimmed, "connection-mesh") && strings.Contains(trimmed, "{") {
			skipping = true
			depth = strings.Count(line, "{") - strings.Count(line, "}")
			continue
		}
		if skipping {
			depth += strings.Count(line, "{") - strings.Count(line, "}")
			if depth <= 0 {
				skipping = false
			}
			continue
		}
		out = append(out, line)
	}
	return strings.Join(out, "\n")
}

// buildConnectionMesh renders a full-mesh stanza listing every host. DRBD 9
// needs an explicit mesh once a resource has more than two nodes.
func buildConnectionMesh(hosts []string) string {
	var b strings.Builder
	b.WriteString("\n    connection-mesh {\n        hosts")
	for _, h := range hosts {
		b.WriteString(" " + h)
	}
	b.WriteString(";\n    }\n")
	return b.String()
}

// addDisklessClientBlock returns content with an `on <node>` stanza added as a
// diskless client: every volume overrides `disk none`, the node gets the next
// free node-id, and the connection-mesh is rebuilt to include it. It returns
// errDisklessAlreadyPresent if node already has an `on` block.
func addDisklessClientBlock(content, node, ip string, port int) (string, error) {
	blocks := parseOnBlocks(content)
	for _, b := range blocks {
		if b.name == node {
			return "", errDisklessAlreadyPresent
		}
	}

	nextID := 0
	for _, b := range blocks {
		if b.nodeID >= nextID {
			nextID = b.nodeID + 1
		}
	}

	vols := dedupResourceVolumes(parseResourceConfigVolumes(content))
	if len(vols) == 0 {
		return "", fmt.Errorf("resource config has no volumes to render diskless")
	}

	var blk strings.Builder
	fmt.Fprintf(&blk, "\n    on %s {\n", node)
	fmt.Fprintf(&blk, "        address   %s:%d;\n", ip, port)
	fmt.Fprintf(&blk, "        node-id   %d;\n", nextID)
	for _, v := range vols {
		fmt.Fprintf(&blk, "        volume %d {\n", v.VolumeID)
		fmt.Fprintf(&blk, "            device    minor %d;\n", v.Minor)
		blk.WriteString("            disk      none;\n")
		blk.WriteString("        }\n")
	}
	blk.WriteString("    }\n")

	stripped := stripConnectionMesh(content)
	withOn, err := insertBeforeResourceClose(stripped, blk.String())
	if err != nil {
		return "", err
	}

	// The mesh covers the LAN participants plus the newcomer; a WAN-attached
	// host keeps its explicit connection and stays out.
	allHosts := append(lanHostNames(blocks), node)
	if meshNeeded(allHosts, append(onHostNames(blocks), node)) {
		withOn, err = insertBeforeResourceClose(withOn, buildConnectionMesh(allHosts))
		if err != nil {
			return "", err
		}
	}
	return withOn, nil
}

// removeDisklessClientBlock returns content with node's `on` stanza removed and
// the connection-mesh rebuilt (dropped entirely if two or fewer hosts remain).
// It returns errDisklessNotPresent if node has no `on` block.
func removeDisklessClientBlock(content, node string) (string, error) {
	var out []string
	depth := 0
	removing := false
	removed := false
	for _, line := range strings.Split(content, "\n") {
		trimmed := strings.TrimSpace(line)
		if !removing && strings.HasPrefix(trimmed, "on ") && strings.Contains(trimmed, "{") {
			if fields := strings.Fields(trimmed); len(fields) >= 2 && fields[1] == node {
				removing = true
				removed = true
				depth = strings.Count(line, "{") - strings.Count(line, "}")
				continue
			}
		}
		if removing {
			depth += strings.Count(line, "{") - strings.Count(line, "}")
			if depth <= 0 {
				removing = false
			}
			continue
		}
		out = append(out, line)
	}
	if !removed {
		return "", errDisklessNotPresent
	}

	stripped := stripConnectionMesh(strings.Join(out, "\n"))
	blocksLeft := parseOnBlocks(stripped)
	remaining := lanHostNames(blocksLeft)
	if meshNeeded(remaining, onHostNames(blocksLeft)) {
		return insertBeforeResourceClose(stripped, buildConnectionMesh(remaining))
	}
	return stripped, nil
}

// AttachDisklessClient adds node to an existing resource as a diskless data
// client. The node stores no replica: it connects over DRBD, syncs no bulk
// data, and can then be promoted Primary (e.g. by the CSI node service) to
// serve the volume over the network. Idempotent — re-attaching an existing
// client is a no-op.
func (rm *ResourceManager) AttachDisklessClient(ctx context.Context, resource, node string) error {
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

	// A node already holding a diskful replica cannot also be a diskless client.
	for _, n := range splitCSV(dbRes.Nodes) {
		if n == node {
			return fmt.Errorf("node %q already holds a diskful replica of %q", node, resource)
		}
	}
	// A quorum tiebreaker is diskless-in-config but must never be promoted;
	// refuse rather than silently reclassify it as a mountable client.
	for _, n := range splitCSV(dbRes.DisklessNodes) {
		if n == node {
			return fmt.Errorf("node %q is a quorum tiebreaker of %q and cannot be a diskless client", node, resource)
		}
	}
	clients := splitCSV(dbRes.DisklessClients)
	alreadyClient := false
	for _, n := range clients {
		if n == node {
			alreadyClient = true
			break
		}
	}

	rm.controller.logger.Info("Attaching diskless client to resource",
		zap.String("resource", resource),
		zap.String("node", node),
		zap.Bool("already_recorded", alreadyClient))

	// Resolve the client's DRBD replication address the same way create does.
	ip := rm.controller.nodes.GetNodeAddressByName(node)
	if ip == "" {
		rm.mu.RLock()
		ip = rm.hostMap[node]
		rm.mu.RUnlock()
	}
	if ip == "" {
		ip = node
	}
	ip = resolveToIP(ip)

	// Read the live .res from a current participant and splice in the client.
	resPath := fmt.Sprintf("/etc/drbd.d/%s.res", resource)
	hosts, err := rm.resourceHosts(ctx, resource)
	if err != nil {
		return err
	}
	catResult, err := rm.deployment.Exec(ctx, []string{hosts[0]}, "cat "+resPath)
	if err != nil {
		return fmt.Errorf("failed to read resource config: %w", err)
	}
	var originalConfig string
	var readOK bool
	for _, r := range catResult.Hosts {
		originalConfig, readOK = r.Output, r.Success
		break
	}
	if !readOK {
		return fmt.Errorf("failed to read resource config for %q", resource)
	}

	// The union of current participants is exactly the on-hosts in the live
	// config (diskful + tiebreakers + any existing clients) — resolve each so
	// the rewritten config and the adjust reach every one of them.
	existingBlocks := parseOnBlocks(originalConfig)
	participantHosts := make([]string, 0, len(existingBlocks))
	for _, b := range existingBlocks {
		participantHosts = append(participantHosts, rm.controller.ResolveHost(b.name))
	}

	// Same rule as the tiebreaker path: the resource's minors were allocated
	// over the nodes it had at create time, so a node attaching later can
	// already be using them for something else.
	if err := rm.assertMinorsFreeOn(ctx, rm.controller.ResolveHost(node), resource,
		resourceMinors(originalConfig)); err != nil {
		return err
	}

	newConfig, err := addDisklessClientBlock(originalConfig, node, ip, dbRes.Port)
	if err == errDisklessAlreadyPresent {
		// The .res already carries the client (a prior partial attach). Make sure
		// it is up and recorded, then return success.
		if _, err := rm.deployment.DRBDUp(ctx, []string{ip}, resource); err != nil {
			return fmt.Errorf("bring up existing diskless client: %w", err)
		}
		return rm.recordDisklessClient(ctx, dbRes, clients, node)
	}
	if err != nil {
		return fmt.Errorf("build diskless client config: %w", err)
	}

	// Roll back the config everywhere and tear the client down if a later step
	// fails, so a retry starts from the pre-attach state.
	allHosts := append(append([]string{}, participantHosts...), ip)
	committed := false
	defer func() {
		if committed {
			return
		}
		rm.controller.logger.Warn("Diskless client attach failed; rolling back",
			zap.String("resource", resource), zap.String("node", node))
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer cancel()
		_, _ = rm.deployment.DRBDDown(cleanupCtx, []string{ip}, resource)
		_, _ = rm.deployment.Exec(cleanupCtx, []string{ip}, "sudo rm -f "+resPath)
		_, _ = rm.deployment.DistributeConfig(cleanupCtx, participantHosts, originalConfig, resPath)
		_, _ = rm.deployment.Exec(cleanupCtx, participantHosts, fmt.Sprintf("sudo drbdadm adjust %s", resource))
	}()

	// Push the rewritten config to every participant plus the new client.
	if _, err := rm.deployment.DistributeConfig(ctx, allHosts, newConfig, resPath); err != nil {
		return fmt.Errorf("distribute diskless client config: %w", err)
	}

	// Let existing peers learn the new connection (adjust reconciles the added
	// mesh edge without disturbing the running replica set).
	if err := rm.execAllSuccess(ctx, participantHosts,
		fmt.Sprintf("sudo drbdadm adjust %s", resource),
		"adjust peers for diskless client"); err != nil {
		return err
	}

	// Bring the resource up on the client. With `disk none` it attaches
	// Diskless, connects to the diskful peers, and is immediately promotable —
	// no create-md and no initial sync (it has no backing storage to sync to).
	if _, err := rm.deployment.DRBDUp(ctx, []string{ip}, resource); err != nil {
		return fmt.Errorf("bring up diskless client: %w", err)
	}

	committed = true
	rm.controller.logger.Info("Diskless client attached",
		zap.String("resource", resource), zap.String("node", node))
	return rm.recordDisklessClient(ctx, dbRes, clients, node)
}

// DetachDisklessClient removes a diskless client added by AttachDisklessClient:
// it downs the resource on that node, rewrites the config on the remaining
// participants, and drops the node from the resource record. Idempotent.
func (rm *ResourceManager) DetachDisklessClient(ctx context.Context, resource, node string) error {
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

	rm.controller.logger.Info("Detaching diskless client from resource",
		zap.String("resource", resource), zap.String("node", node))

	ip := rm.controller.nodes.GetNodeAddressByName(node)
	if ip == "" {
		ip = node
	}
	ip = resolveToIP(ip)

	resPath := fmt.Sprintf("/etc/drbd.d/%s.res", resource)
	hosts, err := rm.resourceHosts(ctx, resource)
	if err != nil {
		return err
	}
	catResult, err := rm.deployment.Exec(ctx, []string{hosts[0]}, "cat "+resPath)
	if err != nil {
		return fmt.Errorf("failed to read resource config: %w", err)
	}
	var originalConfig string
	var ok bool
	for _, r := range catResult.Hosts {
		originalConfig, ok = r.Output, r.Success
		break
	}
	if !ok {
		return fmt.Errorf("failed to read resource config for %q", resource)
	}

	// Down the client first so it stops holding a connection to peers we are
	// about to drop it from. Best-effort: the node may already be gone.
	_, _ = rm.deployment.DRBDDown(ctx, []string{ip}, resource)
	_, _ = rm.deployment.Exec(ctx, []string{ip}, "sudo rm -f "+resPath)

	newConfig, err := removeDisklessClientBlock(originalConfig, node)
	if err == errDisklessNotPresent {
		// Not in the config — just make sure it is out of the record.
		return rm.forgetDisklessClient(ctx, dbRes, node)
	}
	if err != nil {
		return fmt.Errorf("build config without diskless client: %w", err)
	}

	remaining := onHostNames(parseOnBlocks(newConfig))
	remainingHosts := make([]string, 0, len(remaining))
	for _, n := range remaining {
		remainingHosts = append(remainingHosts, rm.controller.ResolveHost(n))
	}
	if _, err := rm.deployment.DistributeConfig(ctx, remainingHosts, newConfig, resPath); err != nil {
		return fmt.Errorf("distribute config without diskless client: %w", err)
	}
	if err := rm.execAllSuccess(ctx, remainingHosts,
		fmt.Sprintf("sudo drbdadm adjust %s", resource),
		"adjust peers after diskless client detach"); err != nil {
		return err
	}

	rm.controller.logger.Info("Diskless client detached",
		zap.String("resource", resource), zap.String("node", node))
	return rm.forgetDisklessClient(ctx, dbRes, node)
}

// recordDisklessClient persists node into the resource's DisklessClients set.
// Best-effort at the DB layer: a save failure is logged, not fatal, because the
// resource is already live on the client at this point.
func (rm *ResourceManager) recordDisklessClient(ctx context.Context, dbRes *database.Resource, clients []string, node string) error {
	for _, n := range clients {
		if n == node {
			return nil // already recorded
		}
	}
	dbRes.DisklessClients = strings.Join(append(clients, node), ",")
	if err := rm.controller.db.SaveResource(ctx, dbRes); err != nil {
		rm.controller.logger.Warn("Failed to record diskless client in database",
			zap.String("resource", dbRes.Name), zap.String("node", node), zap.Error(err))
	}
	return nil
}

// forgetDisklessClient removes node from the resource's DisklessClients set.
func (rm *ResourceManager) forgetDisklessClient(ctx context.Context, dbRes *database.Resource, node string) error {
	var kept []string
	for _, n := range splitCSV(dbRes.DisklessClients) {
		if n != node {
			kept = append(kept, n)
		}
	}
	dbRes.DisklessClients = strings.Join(kept, ",")
	if err := rm.controller.db.SaveResource(ctx, dbRes); err != nil {
		rm.controller.logger.Warn("Failed to remove diskless client from database",
			zap.String("resource", dbRes.Name), zap.String("node", node), zap.Error(err))
	}
	return nil
}

// splitCSV splits a comma-separated node list, trimming blanks.
func splitCSV(s string) []string {
	var out []string
	for _, part := range strings.Split(s, ",") {
		part = strings.TrimSpace(part)
		if part != "" {
			out = append(out, part)
		}
	}
	return out
}

// addDisklessVolumeOverrides returns content with volume volNum added, as
// `disk none` on the given minor, to every `on` stanza that is diskless.
//
// A node is diskless in a resource's config exactly when its `on` stanza
// overrides its volumes with `disk none` — tiebreakers and diskless clients
// alike. When a volume is added to the resource, those stanzas must gain an
// override for it too. Without one, every node reads the new volume as having
// a disk on the tiebreaker, and the tiebreaker's own copy of the config (if it
// is rewritten at all) has no such volume: DRBD then refuses the connection
// ("packet received for volume 1, which is not configured locally") and the
// tiebreaker retries forever. A two-replica resource left with its tiebreaker
// disconnected has no quorum to spare, so the next node failure does not fail
// over — which is the state two gateways on the test cluster were found in.
func addDisklessVolumeOverrides(content string, volNum, minor int) string {
	lines := strings.Split(content, "\n")
	var out []string
	depth := 0
	inOn := false
	onHasDiskNone := false
	onHasThisVolume := false
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if !inOn && depth == 1 && strings.HasPrefix(trimmed, "on ") && strings.Contains(trimmed, "{") {
			inOn, onHasDiskNone, onHasThisVolume = true, false, false
		}
		if inOn {
			if strings.HasPrefix(trimmed, "disk") && strings.Contains(trimmed, "none") {
				onHasDiskNone = true
			}
			if f := strings.Fields(trimmed); len(f) >= 2 && f[0] == "volume" && strings.TrimSuffix(f[1], "{") == strconv.Itoa(volNum) {
				onHasThisVolume = true
			}
		}
		opens := strings.Count(line, "{")
		closes := strings.Count(line, "}")
		// The line that closes the `on` stanza: depth returns to 1 after it.
		if inOn && closes > 0 && depth+opens-closes == 1 {
			if onHasDiskNone && !onHasThisVolume {
				out = append(out,
					fmt.Sprintf("        volume %d {", volNum),
					fmt.Sprintf("            device    minor %d;", minor),
					"            disk      none;",
					"        }")
			}
			inOn = false
		}
		out = append(out, line)
		depth += opens - closes
	}
	return strings.Join(out, "\n")
}

// disklessParticipantHosts returns the resolved addresses of every node that
// takes part in a resource without a disk: quorum tiebreakers and diskless data
// clients. They hold a copy of the resource config like any other node, so any
// change to the resource's volume set has to reach them as well.
func (rm *ResourceManager) disklessParticipantHosts(ctx context.Context, resource string) []string {
	hosts := rm.disklessHosts(ctx, resource)
	if rm.controller.db == nil {
		return hosts
	}
	dbRes, err := rm.controller.db.GetResource(ctx, resource)
	if err != nil || dbRes == nil {
		return hosts
	}
	seen := map[string]bool{}
	for _, h := range hosts {
		seen[h] = true
	}
	for _, n := range splitCSV(dbRes.DisklessClients) {
		if h := rm.controller.ResolveHost(n); h != "" && !seen[h] {
			seen[h] = true
			hosts = append(hosts, h)
		}
	}
	return hosts
}

// removeDisklessVolumeOverrides returns content with volume volNum's override
// removed from every `on` stanza — the counterpart of
// addDisklessVolumeOverrides. Removing a volume leaves its `disk none` override
// behind otherwise, and a diskless node whose config still describes a volume
// its peers no longer have fails the handshake exactly as one missing a volume
// does.
func removeDisklessVolumeOverrides(content string, volNum int) string {
	lines := strings.Split(content, "\n")
	var out []string
	depth := 0
	inOn := false
	skipping := false
	skipDepth := 0
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		opens := strings.Count(line, "{")
		closes := strings.Count(line, "}")
		if !inOn && depth == 1 && strings.HasPrefix(trimmed, "on ") && strings.Contains(trimmed, "{") {
			inOn = true
		}
		if inOn && !skipping && depth == 2 {
			if f := strings.Fields(trimmed); len(f) >= 2 && f[0] == "volume" && strings.TrimSuffix(f[1], "{") == strconv.Itoa(volNum) {
				skipping, skipDepth = true, depth
			}
		}
		next := depth + opens - closes
		if skipping {
			if next == skipDepth {
				skipping = false
			}
			depth = next
			continue
		}
		out = append(out, line)
		if inOn && next == 1 {
			inOn = false
		}
		depth = next
	}
	return strings.Join(out, "\n")
}

// reconcileDisklessVolumeOverrides gives every diskless stanza a `disk none`
// override for every volume the resource has. It is addDisklessVolumeOverrides
// applied for each volume, and like it, a no-op on a config already in
// agreement.
func reconcileDisklessVolumeOverrides(content string) string {
	for _, v := range dedupResourceVolumes(parseResourceConfigVolumes(content)) {
		content = addDisklessVolumeOverrides(content, v.VolumeID, v.Minor)
	}
	return content
}
