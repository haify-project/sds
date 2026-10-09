package controller

import "context"

// poolHosts are the hosts a live pool listing asks: every registered host
// except those the node registry holds offline. A node that stopped answering
// does not refuse SSH, it lets the connection hang, so asking it cost every
// listing a TCP timeout per query — several per listing, one listing per
// placement decision — and `haify pool list` failed outright with one node down.
// What is known of its pools is still in the database.
func (sm *StorageManager) poolHosts(ctx context.Context) []string {
	hosts := sm.controller.GetHosts()
	if sm.controller.nodes == nil {
		return hosts
	}
	nodes, err := sm.controller.nodes.ListNodes(ctx)
	if err != nil {
		return hosts
	}
	offline := map[string]bool{}
	for _, n := range nodes {
		if n.State == NodeStateOffline {
			offline[n.Address] = true
			offline[sm.controller.ResolveHost(n.Address)] = true
		}
	}
	if len(offline) == 0 {
		return hosts
	}
	live := make([]string, 0, len(hosts))
	for _, h := range hosts {
		if !offline[h] && !offline[sm.controller.ResolveHost(h)] {
			live = append(live, h)
		}
	}
	return live
}
