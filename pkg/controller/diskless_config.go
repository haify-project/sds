package controller

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
)

// onBlock is a parsed `on <name> { ... node-id N; ... }` host stanza.
type onBlock struct {
	name   string
	nodeID int
	// loopback is true when the stanza's address is 127.0.0.1, which marks a
	// node reachable only through an haify-proxy WAN leg (the DR site, or the
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
