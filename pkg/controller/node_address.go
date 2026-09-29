package controller

import (
	"context"
	"fmt"
	"net"
	"regexp"
	"sort"
	"strings"

	"go.uber.org/zap"
)

// A node's address is the key it is registered under, in memory and in the
// database, and a literal in the DRBD config of every resource it takes part
// in and in the other nodes' /etc/hosts. When a node was renumbered — a DHCP
// lease that moved, a subnet change — none of that followed: the controller
// kept dialling the old address, re-registering added a second entry under the
// new one beside the stale first, and DRBD kept connecting to an address the
// node no longer had.

// AddressMove asks for one node to be renumbered.
type AddressMove struct {
	Node               string
	Address            string
	ReplicationAddress string // empty keeps the node's current arrangement
}

// NodeMove is one node's renumbering as carried out.
type NodeMove struct {
	Node               string
	Hostname           string
	OldAddress         string
	Address            string
	OldReplication     string // the address DRBD used before
	ReplicationAddress string // the address DRBD uses now
}

// NodeAddressChange is what SetNodeAddresses did.
type NodeAddressChange struct {
	Moves        []NodeMove
	Resources    []string
	Failed       []string // "<resource>: <reason>"
	WANResources []string // resources whose WAN proxy must be rebuilt
}

// SetNodeAddress renumbers one registered node; see SetNodeAddresses.
func (nm *NodeManager) SetNodeAddress(ctx context.Context, nodeRef, address, replicationAddress string) (*NodeAddressChange, error) {
	return nm.SetNodeAddresses(ctx, []AddressMove{{Node: nodeRef, Address: address, ReplicationAddress: replicationAddress}})
}

// SetNodeAddresses renumbers registered nodes together. Each must already
// answer on its new address and be the same machine (same hostname), and no
// two nodes may end up on one address. A replication address, when given,
// moves DRBD traffic there; otherwise a node whose replication ran on its
// management address follows it, and one with its own replication network
// keeps it.
//
// Several at once is not a convenience. When a DHCP server hands every node a
// new lease, renumbering them one by one cannot work: the first node's
// resources are rewritten through peers the controller still knows only by
// their dead addresses, and when two nodes trade addresses the configs end up
// with both peers on one address in between. Here every check runs before
// anything changes, and the registry takes all the new addresses before any
// config is rewritten.
func (nm *NodeManager) SetNodeAddresses(ctx context.Context, moves []AddressMove) (*NodeAddressChange, error) {
	if len(moves) == 0 {
		return nil, fmt.Errorf("no node to renumber")
	}

	nm.mu.RLock()
	byName := make(map[string]*NodeInfo, len(nm.nodes))
	for _, n := range nm.nodes {
		c := *n
		byName[n.Name] = &c
		byName[n.Address] = &c
	}
	var all []NodeInfo
	for _, n := range nm.nodes {
		all = append(all, *n)
	}
	nm.mu.RUnlock()

	// Resolve and validate every move before touching anything.
	var resolved []NodeMove
	seen := map[string]bool{}
	for _, m := range moves {
		address := strings.TrimSpace(m.Address)
		repl := strings.TrimSpace(m.ReplicationAddress)
		if net.ParseIP(address) == nil {
			return nil, fmt.Errorf("%q is not an IP address", address)
		}
		if repl != "" && net.ParseIP(repl) == nil {
			return nil, fmt.Errorf("replication address %q is not an IP address", repl)
		}
		node := byName[strings.TrimSpace(m.Node)]
		if node == nil {
			return nil, fmt.Errorf("node %q is not registered", m.Node)
		}
		if seen[node.Name] {
			return nil, fmt.Errorf("node %s is named twice", node.Name)
		}
		seen[node.Name] = true
		mv := NodeMove{Node: node.Name, Hostname: node.Hostname, OldAddress: node.Address, Address: address,
			OldReplication: replicationOf(node)}
		switch {
		case repl != "":
			mv.ReplicationAddress = repl
		case node.ReplicationAddress != "" && node.ReplicationAddress != node.Address:
			mv.ReplicationAddress = node.ReplicationAddress
		default:
			mv.ReplicationAddress = address
		}
		if mv.OldAddress == mv.Address && mv.OldReplication == mv.ReplicationAddress {
			continue // already there
		}
		resolved = append(resolved, mv)
	}
	if len(resolved) == 0 {
		return nil, fmt.Errorf("nothing to change: every node already uses the address given")
	}

	// No two nodes on one address once everything has moved.
	final := map[string]string{} // address -> node
	for _, n := range all {
		addr, repl := n.Address, replicationOf(&n)
		for _, mv := range resolved {
			if mv.Node == n.Name {
				addr, repl = mv.Address, mv.ReplicationAddress
			}
		}
		for _, a := range []string{addr, repl} {
			if other, taken := final[a]; taken && other != n.Name {
				return nil, fmt.Errorf("%s would belong to both %s and %s", a, other, n.Name)
			}
			final[a] = n.Name
		}
	}

	// Same machine, or nothing moves: a new address that answers with some
	// other host's name is a typo or a reused lease, and renumbering onto it
	// would point every replica of every resource at the wrong machine.
	for _, mv := range resolved {
		res, err := nm.controller.deployment.Exec(ctx, []string{mv.Address}, "hostname")
		if err != nil {
			return nil, fmt.Errorf("reach %s at %s: %w", mv.Node, mv.Address, err)
		}
		var got string
		for _, hr := range res.Hosts {
			if !hr.Success {
				return nil, fmt.Errorf("reach %s at %s: %s", mv.Node, mv.Address, hostFailure(hr))
			}
			got = strings.TrimSpace(hr.Output)
		}
		if mv.Hostname != "" && got != mv.Hostname {
			return nil, fmt.Errorf("%s answers as %q, but node %s is %q; not renumbering onto another machine",
				mv.Address, got, mv.Node, mv.Hostname)
		}
	}

	nm.rekey(ctx, resolved, byName)
	nm.updateKnownHosts(ctx, resolved)
	for _, mv := range resolved {
		nm.updateHostsFiles(ctx, mv.Hostname, mv.OldAddress, mv.Address)
		nm.controller.logger.Info("Node renumbered",
			zap.String("node", mv.Node),
			zap.String("from", mv.OldAddress), zap.String("to", mv.Address),
			zap.String("replication_from", mv.OldReplication),
			zap.String("replication_to", mv.ReplicationAddress))
	}
	return &NodeAddressChange{Moves: resolved}, nil
}

func replicationOf(n *NodeInfo) string {
	if n.ReplicationAddress != "" {
		return n.ReplicationAddress
	}
	return n.Address
}

// rekey moves the nodes to their new addresses everywhere the controller keeps
// them: the registry, the host list and name map every lookup goes through,
// and the database, where the address is the key. Every old key goes before
// any new one arrives, and every mapping is applied once to the original
// values: two nodes trading addresses must not clobber each other halfway.
func (nm *NodeManager) rekey(ctx context.Context, moves []NodeMove, byName map[string]*NodeInfo) {
	oldToNew := make(map[string]string, len(moves))
	updated := make([]NodeInfo, 0, len(moves))
	for _, mv := range moves {
		oldToNew[mv.OldAddress] = mv.Address
		u := *byName[mv.Node]
		u.Address = mv.Address
		u.ReplicationAddress = ""
		if mv.ReplicationAddress != mv.Address {
			u.ReplicationAddress = mv.ReplicationAddress
		}
		u.State = NodeStateOnline
		updated = append(updated, u)
	}

	nm.mu.Lock()
	for _, mv := range moves {
		delete(nm.nodes, mv.OldAddress)
	}
	for i := range updated {
		n := updated[i]
		nm.nodes[n.Address] = &n
	}
	nm.mu.Unlock()

	c := nm.controller
	c.hostsLock.Lock()
	for i, h := range c.hosts {
		if nw, ok := oldToNew[h]; ok {
			c.hosts[i] = nw
		}
	}
	for k, v := range c.hostsMap {
		if nw, ok := oldToNew[v]; ok {
			c.hostsMap[k] = nw
		}
	}
	newAddr := make(map[string]bool, len(updated))
	for _, n := range updated {
		newAddr[n.Address] = true
	}
	for old := range oldToNew {
		if !newAddr[old] { // on a swap, one node's old address is another's new one
			delete(c.hostsMap, old)
		}
	}
	for _, n := range updated {
		c.hostsMap[n.Name] = n.Address
		if n.Hostname != "" {
			c.hostsMap[n.Hostname] = n.Address
		}
	}
	if c.gateway != nil {
		c.gateway.SetHosts(c.hosts)
	}
	hosts := append([]string(nil), c.hosts...)
	c.hostsLock.Unlock()
	if c.resources != nil {
		c.resources.SetHosts(hosts)
	}

	if c.db != nil {
		for _, mv := range moves {
			if err := c.db.DeleteNode(ctx, mv.OldAddress); err != nil {
				c.logger.Warn("Failed to remove the old node record", zap.String("address", mv.OldAddress), zap.Error(err))
			}
		}
		for i := range updated {
			if err := c.db.SaveNode(ctx, nodeRecord(&updated[i])); err != nil {
				c.logger.Error("Failed to save the renumbered node", zap.String("node", updated[i].Name), zap.Error(err))
			}
		}
		// A pool created by address records it as its node.
		if pools, err := c.db.ListPools(ctx); err == nil {
			for _, p := range pools {
				if nw, ok := oldToNew[p.Node]; ok {
					p.Node = nw
					_ = c.db.SavePool(ctx, p)
				}
			}
		}
	}
}

// updateHostsFiles points every node's /etc/hosts entry for the node at its
// new address. Every IPv4 entry naming the host is rewritten, not only the one
// with the address being replaced: registration appends entries and nothing
// ever removed one, so a node renumbered before this existed has several, all
// stale. Loopback lines are left alone — a host names itself on 127.0.1.1 —
// and the duplicates the rewrite produces are dropped. Best effort: a stale
// entry misleads a person, not the controller, which dials addresses.
func (nm *NodeManager) updateHostsFiles(ctx context.Context, hostname, old, address string) {
	if hostname == "" || old == address {
		return
	}
	nm.controller.hostsLock.RLock()
	hosts := append([]string(nil), nm.controller.hosts...)
	nm.controller.hostsLock.RUnlock()
	if _, err := nm.controller.deployment.Exec(ctx, hosts,
		"echo "+base64Std(hostsFileScript(hostname, address))+" | base64 -d | sudo /bin/sh"); err != nil {
		nm.controller.logger.Warn("Failed to update /etc/hosts for a renumbered node", zap.Error(err))
	}
}

// updateKnownHosts puts each renumbered node's host keys into every node's
// root known_hosts under its new address, and forgets what was recorded under
// its old one.
//
// Without it the renumbering works from the node running the controller and
// breaks the day another node takes over: when addresses are reshuffled — a
// DHCP lease swap, two nodes trading places — each node still holds the key
// of the machine that used to be at an address, and accept-new refuses a key
// that changed. On the Lima cluster that surfaced weeks later as "mkdir
// failed" on the first resource created after a controller failover.
//
// The keys are read over the connection that just proved the node answers at
// its new address with its own hostname. Every stale entry is removed before
// any key is added, so a node moving onto another's old address does not have
// its fresh entry removed along with the other's stale one.
func (nm *NodeManager) updateKnownHosts(ctx context.Context, moves []NodeMove) {
	var forget []string
	var entries strings.Builder
	for _, mv := range moves {
		forget = append(forget, mv.OldAddress, mv.Address)
		res, err := nm.controller.deployment.Exec(ctx, []string{mv.Address}, "cat /etc/ssh/ssh_host_*_key.pub")
		if err != nil {
			nm.controller.logger.Warn("Read host keys of a renumbered node", zap.String("node", mv.Node), zap.Error(err))
			continue
		}
		for _, hr := range res.Hosts {
			if !hr.Success {
				continue
			}
			for _, line := range strings.Split(hr.Output, "\n") {
				f := strings.Fields(line)
				if len(f) >= 2 && (strings.HasPrefix(f[0], "ssh-") || strings.HasPrefix(f[0], "ecdsa-")) {
					fmt.Fprintf(&entries, "%s %s %s\n", mv.Address, f[0], f[1])
				}
			}
		}
	}
	nm.controller.hostsLock.RLock()
	hosts := append([]string(nil), nm.controller.hosts...)
	nm.controller.hostsLock.RUnlock()
	if _, err := nm.controller.deployment.Exec(ctx, hosts,
		"echo "+base64Std(knownHostsScript(forget, entries.String()))+" | base64 -d | sudo /bin/sh"); err != nil {
		nm.controller.logger.Warn("Failed to update known_hosts for renumbered nodes", zap.Error(err))
	}
}

// knownHostsScript removes every address in forget from root's known_hosts,
// then appends entries.
func knownHostsScript(forget []string, entries string) string {
	var b strings.Builder
	b.WriteString("f=/root/.ssh/known_hosts\nmkdir -p /root/.ssh && touch \"$f\"\n")
	seen := map[string]bool{}
	for _, a := range forget {
		if a == "" || seen[a] {
			continue
		}
		seen[a] = true
		fmt.Fprintf(&b, "ssh-keygen -R %q -f \"$f\" >/dev/null 2>&1\n", a)
	}
	if entries != "" {
		fmt.Fprintf(&b, "cat >> \"$f\" <<'SDS_KNOWN_HOSTS'\n%sSDS_KNOWN_HOSTS\n", entries)
	}
	b.WriteString("rm -f \"$f.old\"\n")
	return b.String()
}

// hostsFileScript rewrites /etc/hosts in place; see updateHostsFiles.
func hostsFileScript(hostname, address string) string {
	return fmt.Sprintf(`f=/etc/hosts
awk -v new=%[1]q -v name=%[2]q '
$1 !~ /^127\./ && $1 !~ /:/ { for (i = 2; i <= NF; i++) if ($i == name) { $1 = new; break } }
!seen[$0]++ { print }' "$f" > "$f.sds-new" && cat "$f.sds-new" > "$f" && rm -f "$f.sds-new"
`, address, hostname)
}

// RenumberInResources brings every resource a renumbered node takes part in
// onto the registry's addresses: each participant's config gets them and is
// adjusted. Resources are done one at a time and a failure does not stop the
// rest; each one's outcome is in the change.
func (rm *ResourceManager) RenumberInResources(ctx context.Context, change *NodeAddressChange) {
	if rm.controller.db == nil {
		return
	}
	all, err := rm.controller.db.ListResources(ctx)
	if err != nil {
		change.Failed = append(change.Failed, "list resources: "+err.Error())
		return
	}
	byHost := rm.controller.nodes.drbdAddressesByHost()
	for _, r := range all {
		var involved []NodeMove
		for _, mv := range change.Moves {
			if nodeTakesPart(mv.Node, r.Nodes, r.DisklessNodes, r.DisklessClients, r.DRNode) {
				involved = append(involved, mv)
			}
		}
		if len(involved) == 0 {
			continue
		}
		replicationMoved := false
		for _, mv := range involved {
			replicationMoved = replicationMoved || mv.OldReplication != mv.ReplicationAddress
		}
		if r.WANMode {
			change.WANResources = append(change.WANResources, r.Name)
			// The endpoint the primary dials and the source it dials from are
			// operator-given addresses, often a node's own.
			moved := false
			for _, mv := range involved {
				if r.DRNode == mv.Node && (r.DREndpoint == mv.OldAddress || r.DREndpoint == mv.OldReplication) {
					r.DREndpoint, moved = mv.ReplicationAddress, true
				}
				if r.WANEgressAddress != "" && (r.WANEgressAddress == mv.OldAddress || r.WANEgressAddress == mv.OldReplication) {
					r.WANEgressAddress, moved = mv.ReplicationAddress, true
				}
			}
			if moved {
				if err := rm.controller.db.SaveResource(ctx, r); err != nil {
					change.Failed = append(change.Failed, fmt.Sprintf("%s: save WAN endpoint: %v", r.Name, err))
				}
			}
		}
		if !replicationMoved {
			continue // DRBD's addresses did not move; only management ones did
		}
		if err := rm.rewriteResourceConfig(ctx, r.Name, func(current string) (string, error) {
			return reconcileDrbdAddresses(current, byHost), nil
		}); err != nil {
			change.Failed = append(change.Failed, fmt.Sprintf("%s: %v", r.Name, err))
			continue
		}
		change.Resources = append(change.Resources, r.Name)
	}
	sort.Strings(change.Resources)
}

func nodeTakesPart(node string, lists ...string) bool {
	for _, l := range lists {
		for _, n := range splitCSV(l) {
			if n == node {
				return true
			}
		}
	}
	return false
}

// drbdAddressStmt is an IPv4 `address` statement, wherever it sits on a line.
var drbdAddressStmt = regexp.MustCompile(`(\baddress\s+(?:ipv4\s+)?)(\d+\.\d+\.\d+\.\d+):(\d+)`)

// reconcileDrbdAddresses sets the address of every `on <host>` stanza whose
// host is in addrByHost to that address, keeping its port. It does not look
// at what the address was, so a config left half-rewritten — some stanzas
// renumbered, some not, two peers briefly on one address — comes out right
// either way. Loopback addresses belong to WAN proxy legs and are left alone,
// as is every host the map does not know.
func reconcileDrbdAddresses(config string, addrByHost map[string]string) string {
	lines := strings.Split(config, "\n")
	depth := 0
	host := ""
	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		if host == "" && depth == 1 && strings.HasPrefix(trimmed, "on ") && strings.Contains(trimmed, "{") {
			if f := strings.Fields(trimmed); len(f) >= 2 {
				host = f[1]
			}
		}
		if want, ok := addrByHost[host]; host != "" && ok && want != "" {
			lines[i] = drbdAddressStmt.ReplaceAllStringFunc(line, func(stmt string) string {
				m := drbdAddressStmt.FindStringSubmatch(stmt)
				if strings.HasPrefix(m[2], "127.") {
					return stmt
				}
				return m[1] + want + ":" + m[3]
			})
		}
		depth += strings.Count(line, "{") - strings.Count(line, "}")
		if host != "" && depth <= 1 {
			host = ""
		}
	}
	return strings.Join(lines, "\n")
}

// drbdAddressesByHost maps every registered node's DRBD host name to the
// address DRBD should use for it.
func (nm *NodeManager) drbdAddressesByHost() map[string]string {
	nm.mu.RLock()
	defer nm.mu.RUnlock()
	out := make(map[string]string, len(nm.nodes))
	for _, n := range nm.nodes {
		name := n.Hostname
		if name == "" {
			name = n.Name
		}
		out[name] = replicationOf(n)
	}
	return out
}
