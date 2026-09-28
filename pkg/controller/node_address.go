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

// NodeAddressChange is what SetNodeAddress did.
type NodeAddressChange struct {
	Node               string
	OldAddress         string
	Address            string
	OldReplication     string // the address DRBD used before
	ReplicationAddress string // the address DRBD uses now
	Resources          []string
	Failed             []string // "<resource>: <reason>"
	WANResources       []string // resources whose WAN proxy must be rebuilt
}

// SetNodeAddress renumbers a registered node. The node must already answer on
// the new address and be the same machine (same hostname); the address must
// not belong to another node. replicationAddress, when set, moves DRBD traffic
// to it; empty keeps the node's current arrangement — a node whose replication
// ran on its management address follows it to the new one.
func (nm *NodeManager) SetNodeAddress(ctx context.Context, nodeRef, address, replicationAddress string) (*NodeAddressChange, error) {
	address = strings.TrimSpace(address)
	replicationAddress = strings.TrimSpace(replicationAddress)
	if net.ParseIP(address) == nil {
		return nil, fmt.Errorf("%q is not an IP address", address)
	}
	if replicationAddress != "" && net.ParseIP(replicationAddress) == nil {
		return nil, fmt.Errorf("replication address %q is not an IP address", replicationAddress)
	}

	nm.mu.RLock()
	var node *NodeInfo
	for _, n := range nm.nodes {
		if n.Name == nodeRef || n.Address == nodeRef {
			c := *n
			node = &c
		}
	}
	var clash string
	for addr, n := range nm.nodes {
		if node != nil && n.Name != node.Name && (addr == address || n.ReplicationAddress == address) {
			clash = n.Name
		}
	}
	nm.mu.RUnlock()
	if node == nil {
		return nil, fmt.Errorf("node %q is not registered", nodeRef)
	}
	if clash != "" {
		return nil, fmt.Errorf("%s already belongs to node %s", address, clash)
	}

	change := &NodeAddressChange{
		Node:           node.Name,
		OldAddress:     node.Address,
		Address:        address,
		OldReplication: replicationOf(node),
	}
	switch {
	case replicationAddress != "":
		change.ReplicationAddress = replicationAddress
	case node.ReplicationAddress != "" && node.ReplicationAddress != node.Address:
		change.ReplicationAddress = node.ReplicationAddress
	default:
		change.ReplicationAddress = address
	}
	if change.OldAddress == address && change.OldReplication == change.ReplicationAddress {
		return nil, fmt.Errorf("node %s already uses %s", node.Name, address)
	}

	// Same machine, or nothing moves: a new address that answers with some
	// other host's name is a typo or a reused lease, and renumbering onto it
	// would point every replica of every resource at the wrong machine.
	res, err := nm.controller.deployment.Exec(ctx, []string{address}, "hostname")
	if err != nil {
		return nil, fmt.Errorf("reach %s at %s: %w", node.Name, address, err)
	}
	var got string
	for _, hr := range res.Hosts {
		if !hr.Success {
			return nil, fmt.Errorf("reach %s at %s: %s", node.Name, address, hostFailure(hr))
		}
		got = strings.TrimSpace(hr.Output)
	}
	if node.Hostname != "" && got != node.Hostname {
		return nil, fmt.Errorf("%s answers as %q, but node %s is %q; not renumbering onto another machine",
			address, got, node.Name, node.Hostname)
	}

	nm.rekey(ctx, node, change)
	nm.updateHostsFiles(ctx, node.Hostname, change.OldAddress, address)
	nm.controller.logger.Info("Node renumbered",
		zap.String("node", node.Name),
		zap.String("from", change.OldAddress), zap.String("to", address),
		zap.String("replication_from", change.OldReplication),
		zap.String("replication_to", change.ReplicationAddress))
	return change, nil
}

func replicationOf(n *NodeInfo) string {
	if n.ReplicationAddress != "" {
		return n.ReplicationAddress
	}
	return n.Address
}

// rekey moves the node to its new address everywhere the controller keeps it:
// the registry, the host list and name map every lookup goes through, and the
// database, where the address is the key.
func (nm *NodeManager) rekey(ctx context.Context, node *NodeInfo, change *NodeAddressChange) {
	old := change.OldAddress
	updated := *node
	updated.Address = change.Address
	updated.ReplicationAddress = ""
	if change.ReplicationAddress != change.Address {
		updated.ReplicationAddress = change.ReplicationAddress
	}
	updated.State = NodeStateOnline

	nm.mu.Lock()
	delete(nm.nodes, old)
	nm.nodes[updated.Address] = &updated
	nm.mu.Unlock()

	c := nm.controller
	c.hostsLock.Lock()
	for i, h := range c.hosts {
		if h == old {
			c.hosts[i] = updated.Address
		}
	}
	for k, v := range c.hostsMap {
		if v == old {
			c.hostsMap[k] = updated.Address
		}
	}
	delete(c.hostsMap, old)
	c.hostsMap[updated.Name] = updated.Address
	if c.gateway != nil {
		c.gateway.SetHosts(c.hosts)
	}
	hosts := append([]string(nil), c.hosts...)
	c.hostsLock.Unlock()
	if c.resources != nil {
		c.resources.SetHosts(hosts)
	}

	if c.db != nil {
		if err := c.db.DeleteNode(ctx, old); err != nil {
			c.logger.Warn("Failed to remove the old node record", zap.String("address", old), zap.Error(err))
		}
		if err := c.db.SaveNode(ctx, nodeRecord(&updated)); err != nil {
			c.logger.Error("Failed to save the renumbered node", zap.String("node", updated.Name), zap.Error(err))
		}
		// A pool created by address records it as its node.
		if pools, err := c.db.ListPools(ctx); err == nil {
			for _, p := range pools {
				if p.Node == old {
					p.Node = updated.Address
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

// hostsFileScript rewrites /etc/hosts in place; see updateHostsFiles.
func hostsFileScript(hostname, address string) string {
	return fmt.Sprintf(`f=/etc/hosts
awk -v new=%[1]q -v name=%[2]q '
$1 !~ /^127\./ && $1 !~ /:/ { for (i = 2; i <= NF; i++) if ($i == name) { $1 = new; break } }
!seen[$0]++ { print }' "$f" > "$f.sds-new" && cat "$f.sds-new" > "$f" && rm -f "$f.sds-new"
`, address, hostname)
}

// RenumberInResources points every resource the node takes part in at its new
// replication address: the address line of its host section is rewritten on
// every participant and adjusted. Resources are done one at a time and a
// failure does not stop the rest; each one's outcome is in the change.
func (rm *ResourceManager) RenumberInResources(ctx context.Context, change *NodeAddressChange) {
	if rm.controller.db == nil {
		return
	}
	all, err := rm.controller.db.ListResources(ctx)
	if err != nil {
		change.Failed = append(change.Failed, "list resources: "+err.Error())
		return
	}
	for _, r := range all {
		if !nodeTakesPart(change.Node, r.Nodes, r.DisklessNodes, r.DisklessClients, r.DRNode) {
			continue
		}
		if r.WANMode {
			change.WANResources = append(change.WANResources, r.Name)
			// The endpoint the primary dials and the source it dials from are
			// operator-given addresses, often this node's own.
			moved := false
			if r.DRNode == change.Node && (r.DREndpoint == change.OldAddress || r.DREndpoint == change.OldReplication) {
				r.DREndpoint, moved = change.ReplicationAddress, true
			}
			if r.WANEgressAddress != "" && (r.WANEgressAddress == change.OldAddress || r.WANEgressAddress == change.OldReplication) {
				r.WANEgressAddress, moved = change.ReplicationAddress, true
			}
			if moved {
				if err := rm.controller.db.SaveResource(ctx, r); err != nil {
					change.Failed = append(change.Failed, fmt.Sprintf("%s: save WAN endpoint: %v", r.Name, err))
				}
			}
		}
		if change.OldReplication == change.ReplicationAddress {
			continue // DRBD's address did not move; only the management one did
		}
		if err := rm.rewriteResourceConfig(ctx, r.Name, func(current string) (string, error) {
			return renumberDrbdAddress(current, change.OldReplication, change.ReplicationAddress), nil
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

// renumberDrbdAddress replaces old with new in the config's address
// statements — `address 10.0.0.1:7000;` and `address ipv4 10.0.0.1:7000;` —
// and nowhere else. An address is written with its port, so the match cannot
// catch 10.0.0.1 inside 10.0.0.10.
func renumberDrbdAddress(config, old, new string) string {
	re := regexp.MustCompile(`(\baddress\s+(?:ipv4\s+)?)` + regexp.QuoteMeta(old) + `(:\d+)`)
	return re.ReplaceAllString(config, "${1}"+new+"${2}")
}
