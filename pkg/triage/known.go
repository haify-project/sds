package triage

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Known failure modes: the ones SDS knows the cause of.
//
// Everything else in this package groups evidence and stops. These nine state
// a cause and give steps, and the bar for joining them is high: the match has
// to be specific enough that a false positive is hard to construct, and the
// advice has to be right without adaptation. A step an operator has to adjust
// is a step they will adjust wrongly while the cluster is down.
//
// Each matcher returns findings it can show the line for. None of them infers:
// if the evidence is not there, the mode does not fire and the generic
// grouping reports the lines as what they are.

type matcher func(Input) []Finding

var matchers = []matcher{
	matchPhantomPeer,
	matchSplitBrain,
	matchThinPool,
	matchFilesystemAborted,
	matchDRBDModuleMissing,
	matchPromoterStartFailed,
	matchUnplannedFailover,
	matchNoPrimary,
	matchNodeUnreachable,
	matchMountFull,
}

func knownFindings(in Input) []Finding {
	var out []Finding
	for _, m := range matchers {
		out = append(out, m(in)...)
	}
	return out
}

// ── 1. a peer slot nobody forgot ────────────────────────────────────────

// matchPhantomPeer finds a DRBD connection to a peer that is not a registered
// node.
//
// This is the quorum failure that reads as "the resource will not promote and
// every node looks fine". Removing a replica without `drbdadm forget-peer`
// leaves its slot in the metadata; the slot still counts toward the majority,
// so a two-node cluster needs three votes and can never have them. Nothing
// reports it as an error — the phantom simply sits in `Connecting` forever,
// which is also what a peer that is merely down looks like.
//
// What makes it identifiable is the registry: a peer name DRBD knows and the
// controller does not is not a node that is down, it is a node that is gone.
func matchPhantomPeer(in Input) []Finding {
	known := map[string]bool{}
	for _, n := range in.Nodes {
		known[strings.ToLower(n.Node)] = true
		if n.Address != "" {
			known[strings.ToLower(n.Address)] = true
		}
	}
	if len(known) == 0 {
		return nil // without a registry there is nothing to be a phantom against
	}

	type phantom struct {
		peer     string
		resource string
		nodes    map[string]bool
		lines    []Evidence
	}
	seen := map[string]*phantom{}

	for _, n := range in.Nodes {
		out, ok := n.Collector("drbd_status")
		if !ok || !out.Ok {
			continue
		}
		resource := ""
		for _, line := range out.Lines {
			if r, isRes := drbdResourceLine(line); isRes {
				resource = r
				continue
			}
			peer, state, isPeer := drbdPeerLine(line)
			if !isPeer || known[strings.ToLower(peer)] {
				continue
			}
			// Connected to something unregistered is a registry gap, not a
			// quorum fault; only a peer that never connects holds the vote.
			if !strings.EqualFold(state, "Connecting") {
				continue
			}
			key := resource + "/" + peer
			p := seen[key]
			if p == nil {
				p = &phantom{peer: peer, resource: resource, nodes: map[string]bool{}}
				seen[key] = p
			}
			p.nodes[n.Node] = true
			p.lines = append(p.lines, Evidence{
				Source: n.Node + "/drbd_status", Node: n.Node, Line: strings.TrimSpace(line),
			})
		}
	}

	out := make([]Finding, 0, len(seen))
	for _, p := range seen {
		res := p.resource
		if res == "" {
			res = "<resource>"
		}
		out = append(out, Finding{
			ID:       "phantom-peer",
			Title:    fmt.Sprintf("%s has a peer slot for %q, which is not a registered node", res, p.peer),
			Severity: SeverityCritical,
			Count:    len(p.lines),
			Nodes:    sortedKeys(p.nodes),
			Resource: p.resource,
			Known:    true,
			Cause: "A replica was removed without clearing its slot from the DRBD metadata. " +
				"The slot still counts toward quorum, so the surviving nodes can never form a " +
				"majority and the resource will not promote — while every node that is actually " +
				"present looks healthy.",
			Advice: []string{
				fmt.Sprintf("Confirm the slot is a leftover and not a node you meant to keep: drbdsetup status %s --verbose", res),
				fmt.Sprintf("On every surviving node, clear it: drbdadm forget-peer %s:%s", res, p.peer),
				fmt.Sprintf("Check the majority is now reachable: drbdsetup status %s", res),
			},
			Caution: "forget-peer is not reversible. A node that is merely powered off will need a " +
				"full resync to rejoin after this, so be sure the peer is gone for good.",
			Evidence: p.lines,
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Title < out[j].Title })
	return out
}

// drbdResourceLine recognises the un-indented line that names a resource in
// `drbdsetup status` output.
func drbdResourceLine(line string) (string, bool) {
	if line == "" || line[0] == ' ' || line[0] == '\t' {
		return "", false
	}
	f := strings.Fields(line)
	if len(f) < 2 || !strings.HasPrefix(f[1], "node-id:") {
		return "", false
	}
	return f[0], true
}

// drbdPeerLine recognises an indented peer line and returns its name and
// connection state.
func drbdPeerLine(line string) (peer, state string, ok bool) {
	if line == "" || (line[0] != ' ' && line[0] != '\t') {
		return "", "", false
	}
	f := strings.Fields(line)
	if len(f) < 2 {
		return "", "", false
	}
	for _, tok := range f[1:] {
		if v, found := strings.CutPrefix(tok, "connection:"); found {
			return f[0], v, true
		}
	}
	return "", "", false
}

// ── 2. split brain ──────────────────────────────────────────────────────

func matchSplitBrain(in Input) []Finding {
	ev, nodes := nodeLinesMatching(in, func(l string) bool {
		ll := strings.ToLower(l)
		return strings.Contains(ll, "split-brain") || strings.Contains(ll, "split brain")
	}, "drbd_kernel", "kernel_errors", "reactor_journal")
	if len(ev) == 0 {
		return nil
	}
	return []Finding{{
		ID:       "split-brain",
		Title:    "DRBD reported split brain",
		Severity: SeverityCritical,
		Count:    len(ev),
		Nodes:    nodes,
		Known:    true,
		Cause: "Two nodes both wrote to the same resource while they could not see each other. " +
			"DRBD has kept both versions and refuses to connect them, because merging them " +
			"automatically would silently destroy whichever side it did not pick.",
		Advice: []string{
			"Decide which side has the writes you need — check the application, not DRBD; both sides are internally consistent.",
			"On the side you are discarding: drbdadm disconnect <resource>",
			"On the side you are discarding: drbdadm secondary <resource>",
			"On the side you are discarding: drbdadm connect --discard-my-data <resource>",
			"On the side you are keeping: drbdadm connect <resource>",
			"Watch it resync: drbdsetup status <resource>",
		},
		Caution: "--discard-my-data destroys every write made on that side during the split. " +
			"Take a snapshot or a backup of the discarded side first if there is any doubt " +
			"about which one is right.",
		Evidence: ev,
	}}
}

// ── 3. thin pool ────────────────────────────────────────────────────────

// matchThinPool reads the `lvs` output for a pool that is out of room.
//
// Data and metadata are separate exhaustions with the same consequence and
// different fixes, and the metadata one is the one that surprises people: a
// pool with 40% of its data space free stops accepting writes when its much
// smaller metadata volume fills.
func matchThinPool(in Input) []Finding {
	type hit struct {
		lv, vg string
		pct    float64
		nodes  map[string]bool
		ev     []Evidence
	}
	data, meta := map[string]*hit{}, map[string]*hit{}

	record(in, "storage", func(node, line string) {
		lv, vg, dataPct, metaPct, ok := parseLVSLine(line)
		if !ok {
			return
		}
		key := vg + "/" + lv
		if dataPct >= 90 {
			h := data[key]
			if h == nil {
				h = &hit{lv: lv, vg: vg, nodes: map[string]bool{}}
				data[key] = h
			}
			h.pct = maxFloat(h.pct, dataPct)
			h.nodes[node] = true
			h.ev = append(h.ev, Evidence{Source: node + "/storage", Node: node, Line: strings.TrimSpace(line)})
		}
		if metaPct >= 90 {
			h := meta[key]
			if h == nil {
				h = &hit{lv: lv, vg: vg, nodes: map[string]bool{}}
				meta[key] = h
			}
			h.pct = maxFloat(h.pct, metaPct)
			h.nodes[node] = true
			h.ev = append(h.ev, Evidence{Source: node + "/storage", Node: node, Line: strings.TrimSpace(line)})
		}
	})

	var out []Finding
	emit := func(m map[string]*hit, dimension string) {
		keys := make([]string, 0, len(m))
		for k := range m {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			h := m[k]
			sev := SeverityWarning
			if h.pct >= 99 {
				sev = SeverityCritical
			} else if h.pct >= 95 {
				sev = SeverityError
			}
			cause := "A thin pool that runs out of data space stops accepting writes, and every " +
				"volume in it stops at once — including ones that are themselves nearly empty."
			advice := []string{
				fmt.Sprintf("See what is using it: lvs -o lv_name,lv_size,data_percent %s", h.vg),
				fmt.Sprintf("Extend the pool if there is free space in the group: lvextend -L +20G %s/%s", h.vg, h.lv),
				fmt.Sprintf("If the group itself is full, add a disk first: sds-cli pool add-disk --name %s --disks /dev/<device>", h.vg),
				"Delete snapshots that are no longer needed — an old snapshot pins every block it was taken over.",
			}
			if dimension == "metadata" {
				cause = "A thin pool's metadata volume is much smaller than its data space and fills " +
					"independently. When it fills the pool stops accepting writes even though the " +
					"data space still shows room free, which is why this looks like a pool with " +
					"plenty of space refusing to write."
				advice = []string{
					fmt.Sprintf("Confirm which dimension is full: lvs -o lv_name,data_percent,metadata_percent %s/%s", h.vg, h.lv),
					fmt.Sprintf("Extend the metadata volume: lvextend --poolmetadatasize +1G %s/%s", h.vg, h.lv),
					"Delete snapshots that are no longer needed — each one costs metadata whether or not it costs data.",
				}
			}
			out = append(out, Finding{
				ID:       "thin-pool-" + dimension,
				Title:    fmt.Sprintf("thin pool %s/%s is %.1f%% full on %s space", h.vg, h.lv, h.pct, dimension),
				Severity: sev,
				Count:    len(h.ev),
				Nodes:    sortedKeys(h.nodes),
				Known:    true,
				Cause:    cause,
				Advice:   advice,
				Caution: "Do not free space by removing a logical volume that a DRBD resource is " +
					"still backed by; that destroys the replica, and the peers will not put it back.",
				Evidence: h.ev,
			})
		}
	}
	emit(data, "data")
	emit(meta, "metadata")
	return out
}

// parseLVSLine reads one row of `lvs -o lv_name,vg_name,lv_size,data_percent,metadata_percent`.
// A row whose percentage columns are blank is a plain volume, not a pool.
func parseLVSLine(line string) (lv, vg string, dataPct, metaPct float64, ok bool) {
	f := strings.Fields(line)
	if len(f) < 5 || f[0] == "LV" {
		return "", "", 0, 0, false
	}
	d, errD := strconv.ParseFloat(strings.TrimSuffix(f[3], "%"), 64)
	m, errM := strconv.ParseFloat(strings.TrimSuffix(f[4], "%"), 64)
	if errD != nil && errM != nil {
		return "", "", 0, 0, false
	}
	if errD != nil {
		d = 0
	}
	if errM != nil {
		m = 0
	}
	return f[0], f[1], d, m, true
}

// ── 4. the filesystem gave up ───────────────────────────────────────────

func matchFilesystemAborted(in Input) []Finding {
	ev, nodes := nodeLinesMatching(in, func(l string) bool {
		ll := strings.ToLower(l)
		return strings.Contains(ll, "remounting filesystem read-only") ||
			strings.Contains(ll, "aborting journal") ||
			strings.Contains(ll, "ext4-fs error") ||
			(strings.Contains(ll, "xfs") && strings.Contains(ll, "corruption"))
	}, "kernel_errors", "drbd_kernel")
	if len(ev) == 0 {
		return nil
	}
	return []Finding{{
		ID:       "filesystem-aborted",
		Title:    "a filesystem hit an error and went read-only",
		Severity: SeverityCritical,
		Count:    len(ev),
		Nodes:    nodes,
		Known:    true,
		Cause: "The kernel found something it could not reconcile and stopped writing rather than " +
			"make it worse. On a DRBD volume this is usually the block layer underneath — a full " +
			"thin pool, a failing disk, or a resource that lost its backing device — not the " +
			"filesystem itself.",
		Advice: []string{
			"Look at what is underneath before touching the filesystem: check this report's thin-pool and DRBD findings first.",
			"Read the surrounding kernel messages for the first error, not the loudest: dmesg -T | grep -iE 'ext4|xfs|drbd|I/O' | head -50",
			"Once the cause underneath is fixed, unmount and check: umount <mountpoint> && fsck -y <device>",
		},
		Caution: "Do not remount read-write to 'try again'. If the block layer is still broken that " +
			"turns a stopped filesystem into a corrupted one.",
		Evidence: ev,
	}}
}

// ── 5. the module is not there ──────────────────────────────────────────

func matchDRBDModuleMissing(in Input) []Finding {
	ev, nodes := nodeLinesMatching(in, func(l string) bool {
		ll := strings.ToLower(l)
		return strings.Contains(ll, "module drbd not found") ||
			(strings.Contains(ll, "modprobe") && strings.Contains(ll, "drbd") && strings.Contains(ll, "fatal"))
	}, "drbd_kernel", "kernel_errors", "reactor_journal", "promoter_journal", "sds_journal")
	if len(ev) == 0 {
		return nil
	}
	return []Finding{{
		ID:       "drbd-module-missing",
		Title:    "the DRBD kernel module is not loadable on this node",
		Severity: SeverityCritical,
		Count:    len(ev),
		Nodes:    nodes,
		Known:    true,
		Cause: "The drbd-utils package installs cleanly on its own, so a node can look configured " +
			"and pass a casual check while having no kernel module at all. Such a node cannot " +
			"carry a resource: every drbdadm up on it fails.",
		Advice: []string{
			"Confirm what is actually there: drbdadm --version   (DRBD_KERNEL_VERSION=0 means no module)",
			"Install the module for the running kernel — the package name follows the distribution: drbd-dkms, kmod-drbd9x, or a LINBIT build matching uname -r.",
			"Load it and make that persist: modprobe drbd && echo drbd > /etc/modules-load.d/drbd.conf",
			"Re-check the node: sds-cli node health-check <node>",
		},
		Evidence: ev,
	}}
}

// ── 6. the promoter could not start its target ──────────────────────────

func matchPromoterStartFailed(in Input) []Finding {
	ev, nodes := nodeLinesMatching(in, func(l string) bool {
		ll := strings.ToLower(l)
		if !strings.Contains(ll, "drbd-services@") && !strings.Contains(ll, "drbd-promote@") &&
			!strings.Contains(ll, "service-ip@") {
			return false
		}
		return strings.Contains(ll, "failed with result") ||
			strings.Contains(ll, "failed to start") ||
			strings.Contains(ll, "start request repeated too quickly")
	}, "promoter_journal", "reactor_journal", "failed_units")
	if len(ev) == 0 {
		return nil
	}
	return []Finding{{
		ID:       "promoter-start-failed",
		Title:    "a drbd-reactor promoter could not start its target",
		Severity: SeverityError,
		Count:    len(ev),
		Nodes:    nodes,
		Known:    true,
		Cause: "The promoter runs its start list in order — mount, then service IP, then the " +
			"services — and stops at the first step that fails, leaving the resource unpromoted. " +
			"The step that failed is named in the journal immediately before this line; the " +
			"failure people usually chase is the last line, which is only the target giving up.",
		Advice: []string{
			"Find the first failure, not the last: journalctl -u 'drbd-services@*' -u 'drbd-promote@*' -u 'service-ip@*' --since '-30min' -o short-iso | head -60",
			"Check the mount step's device exists and the filesystem is sound: lsblk /dev/drbd* && drbdsetup status",
			"Check the service IP is not already held elsewhere on the network: ip addr show | grep <vip>",
			"After fixing the failing step, let reactor try again: drbd-reactorctl status && systemctl reset-failed 'drbd-services@*'",
		},
		Evidence: ev,
	}}
}

// ── 7. a failover nobody asked for ──────────────────────────────────────

// matchUnplannedFailover pairs a failover event with the restart that caused it.
//
// Restarting a service the promoter manages is not a restart of that service:
// reactor sees the unit stop, demotes the resource and another node promotes
// it, so the whole target moves and the control plane is gone for seconds.
// That is the single most common self-inflicted outage on this cluster, and it
// is invisible in the events alone — a failover event looks the same whether a
// node died or somebody typed systemctl restart.
//
// The pairing is what makes it identifiable, so this only fires when both
// halves are present within the window. Without the journal line it stays a
// plain failover event, reported by the generic grouping.
func matchUnplannedFailover(in Input) []Finding {
	var failovers []Event
	for _, e := range in.Events {
		if strings.EqualFold(e.Type, "resource.failover") {
			failovers = append(failovers, e)
		}
	}
	if len(failovers) == 0 {
		return nil
	}

	const near = 3 * time.Minute
	var out []Finding
	for _, fo := range failovers {
		var ev []Evidence
		nodes := map[string]bool{}
		for _, n := range in.Nodes {
			for _, cname := range []string{"promoter_journal", "reactor_journal", "sds_journal"} {
				c, ok := n.Collector(cname)
				if !ok {
					continue
				}
				for _, line := range c.Lines {
					if !restartLine(line) {
						continue
					}
					if at, ok := journalTime(line); ok && !fo.At.IsZero() &&
						absDuration(at.Sub(fo.At)) > near {
						continue
					}
					nodes[n.Node] = true
					ev = append(ev, Evidence{Source: n.Node + "/" + cname, Node: n.Node, Line: strings.TrimSpace(line)})
				}
			}
		}
		if len(ev) == 0 {
			continue
		}
		ev = append([]Evidence{{
			Source: "event", Node: fo.Node, At: stamp(fo.At),
			Line: fo.Type + ": " + fo.Message,
		}}, ev...)
		out = append(out, Finding{
			ID:       "unplanned-failover",
			Title:    fmt.Sprintf("%s failed over right after a managed service was restarted", orDefault(fo.Resource, "a resource")),
			Severity: SeverityError,
			Count:    len(ev),
			Nodes:    sortedKeys(nodes),
			Resource: fo.Resource,
			Known:    true,
			Cause: "Restarting a service that a drbd-reactor promoter manages is not a restart of " +
				"that service. Reactor sees the unit stop, demotes the resource, and another node " +
				"promotes it — the whole target moves and the control plane is unreachable for " +
				"several seconds. In the events this is indistinguishable from a node dying.",
			Advice: []string{
				"To replace a binary without moving the role: install it on every member node first, then move the role deliberately with sds-cli ha evict " + orDefault(fo.Resource, "<resource>"),
				"To take a node out for maintenance: sds-cli ha evict " + orDefault(fo.Resource, "<resource>") + "   (this is the supported way to move a role)",
				"Never systemctl restart a unit listed in the promoter's start list — check first with: drbd-reactorctl status",
			},
			Evidence: ev,
		})
	}
	return out
}

func restartLine(line string) bool {
	ll := strings.ToLower(line)
	if !strings.Contains(ll, "stopping") && !strings.Contains(ll, "scheduled restart") &&
		!strings.Contains(ll, "deactivated successfully") && !strings.Contains(ll, "stopped") {
		return false
	}
	return strings.Contains(ll, "drbd-services@") || strings.Contains(ll, "sds-controller") ||
		strings.Contains(ll, "sds-ai") || strings.Contains(ll, "drbd-promote@")
}

// journalTime reads the leading timestamp of a `-o short-iso` journal line.
func journalTime(line string) (time.Time, bool) {
	f := strings.Fields(line)
	if len(f) == 0 {
		return time.Time{}, false
	}
	for _, layout := range []string{"2006-01-02T15:04:05-0700", "2006-01-02T15:04:05Z07:00", "2006-01-02T15:04:05"} {
		if t, err := time.Parse(layout, f[0]); err == nil {
			return t, true
		}
	}
	return time.Time{}, false
}

// ── 8 & 9. what the events already say plainly ──────────────────────────

func matchNoPrimary(in Input) []Finding {
	var ev []Evidence
	nodes := map[string]bool{}
	resource := ""
	for _, e := range in.Events {
		if !strings.EqualFold(e.Type, "resource.no_primary") || strings.EqualFold(e.Status, "resolved") {
			continue
		}
		resource = orDefault(resource, e.Resource)
		if e.Node != "" {
			nodes[e.Node] = true
		}
		ev = append(ev, Evidence{Source: "event", Node: e.Node, At: stamp(e.At), Line: e.Type + ": " + e.Message})
	}
	if len(ev) == 0 {
		return nil
	}
	return []Finding{{
		ID:       "no-primary",
		Title:    fmt.Sprintf("%s has no Primary — nothing can use it right now", orDefault(resource, "a resource")),
		Severity: SeverityCritical,
		Count:    len(ev),
		Nodes:    sortedKeys(nodes),
		Resource: resource,
		Known:    true,
		Cause: "No node holds the Primary role, so the volume is not mounted and whatever depends " +
			"on it is stopped. Either no node can reach quorum, or every candidate refused to " +
			"promote — the promoter's journal says which.",
		Advice: []string{
			"Check quorum before anything else; this report's phantom-peer finding, if present, is the answer: drbdsetup status " + orDefault(resource, "<resource>"),
			"Read why the promotion was refused: journalctl -u 'drbd-promote@*' --since '-30min' -o short-iso | tail -40",
			"If quorum is genuinely lost and you accept the risk, promote deliberately rather than forcing: sds-cli ha evict " + orDefault(resource, "<resource>"),
		},
		Caution: "Do not use drbdadm primary --force to get out of this without first understanding " +
			"why quorum is missing. Forcing a Primary on the wrong side is how a quorum problem " +
			"becomes split brain.",
		Evidence: ev,
	}}
}

func matchNodeUnreachable(in Input) []Finding {
	nodes := map[string]bool{}
	var ev []Evidence
	for _, n := range in.Nodes {
		if n.Reachable {
			continue
		}
		nodes[n.Node] = true
		line := n.Node + " did not answer the controller"
		if n.Error != "" {
			line += ": " + n.Error
		}
		ev = append(ev, Evidence{Source: "collector", Node: n.Node, Line: line})
	}
	for _, e := range in.Events {
		if strings.EqualFold(e.Type, "node.unreachable") && !strings.EqualFold(e.Status, "resolved") {
			if e.Node != "" {
				nodes[e.Node] = true
			}
			ev = append(ev, Evidence{Source: "event", Node: e.Node, At: stamp(e.At), Line: e.Type + ": " + e.Message})
		}
	}
	if len(ev) == 0 {
		return nil
	}
	return []Finding{{
		ID:       "node-unreachable",
		Title:    "the controller cannot reach " + strings.Join(sortedKeys(nodes), ", "),
		Severity: SeverityError,
		Count:    len(ev),
		Nodes:    sortedKeys(nodes),
		Known:    true,
		Cause: "The controller reaches nodes over SSH, so this is one of three things and they " +
			"look identical from here: the node is down, the network to it is down, or its SSH " +
			"key or address changed. Everything else in this report about that node is missing " +
			"rather than clean.",
		Advice: []string{
			"Separate down from unreachable: ping <address> and then ssh <address> true",
			"If SSH is the problem, check the address the controller has for it: sds-cli node list",
			"Remember that a resource's replicas on an unreachable node still hold quorum votes; do not remove the node to 'clean up' while the cluster is degraded.",
		},
		Evidence: ev,
	}}
}

// ── 10. the mount itself is full ────────────────────────────────────────

func matchMountFull(in Input) []Finding {
	type hit struct {
		mount string
		pct   int
		nodes map[string]bool
		ev    []Evidence
	}
	hits := map[string]*hit{}
	record(in, "mounts", func(node, line string) {
		f := strings.Fields(line)
		if len(f) < 6 || f[0] == "Filesystem" {
			return
		}
		pct, err := strconv.Atoi(strings.TrimSuffix(f[4], "%"))
		if err != nil || pct < 90 {
			return
		}
		mount := f[5]
		h := hits[mount]
		if h == nil {
			h = &hit{mount: mount, nodes: map[string]bool{}}
			hits[mount] = h
		}
		if pct > h.pct {
			h.pct = pct
		}
		h.nodes[node] = true
		h.ev = append(h.ev, Evidence{Source: node + "/mounts", Node: node, Line: strings.TrimSpace(line)})
	})
	keys := make([]string, 0, len(hits))
	for k := range hits {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := make([]Finding, 0, len(keys))
	for _, k := range keys {
		h := hits[k]
		sev := SeverityWarning
		if h.pct >= 98 {
			sev = SeverityCritical
		} else if h.pct >= 95 {
			sev = SeverityError
		}
		out = append(out, Finding{
			ID:       "mount-full",
			Title:    fmt.Sprintf("%s is %d%% full", h.mount, h.pct),
			Severity: sev,
			Count:    len(h.ev),
			Nodes:    sortedKeys(h.nodes),
			Known:    true,
			Cause: "A DRBD-backed filesystem that fills stops the service using it, and on the " +
				"controller's own volume that means the control plane stops being able to write " +
				"its database.",
			Advice: []string{
				fmt.Sprintf("Find what grew: du -xh %s --max-depth=2 | sort -h | tail -20", h.mount),
				"Grow the volume rather than deleting blindly — DRBD resizes online: sds-cli resource resize-volume --resource <resource> --volume 0 --size <new size>",
			},
			Evidence: h.ev,
		})
	}
	return out
}

// ── helpers ─────────────────────────────────────────────────────────────

// nodeLinesMatching collects every line from the named collectors that a
// predicate accepts, with the node it came from.
func nodeLinesMatching(in Input, keep func(string) bool, collectors ...string) ([]Evidence, []string) {
	var ev []Evidence
	nodes := map[string]bool{}
	for _, n := range in.Nodes {
		for _, name := range collectors {
			c, ok := n.Collector(name)
			if !ok {
				continue
			}
			for _, line := range c.Lines {
				if !keep(line) {
					continue
				}
				nodes[n.Node] = true
				ev = append(ev, Evidence{Source: n.Node + "/" + name, Node: n.Node, Line: strings.TrimSpace(line)})
			}
		}
	}
	return ev, sortedKeys(nodes)
}

// record walks one collector's lines on every node.
func record(in Input, collector string, fn func(node, line string)) {
	for _, n := range in.Nodes {
		c, ok := n.Collector(collector)
		if !ok || !c.Ok {
			continue
		}
		for _, line := range c.Lines {
			fn(n.Node, line)
		}
	}
}

func orDefault(s, fallback string) string {
	if strings.TrimSpace(s) != "" {
		return s
	}
	return fallback
}

func maxFloat(a, b float64) float64 {
	if a > b {
		return a
	}
	return b
}

func absDuration(d time.Duration) time.Duration {
	if d < 0 {
		return -d
	}
	return d
}
