# Take a node out of service (reboot, repair)
Drain a node, do the work, bring it back.
Needs: admin

1. Survey. `haify_resource_list` and `haify_resource_status`: which resources have a replica on the node and which are Primary there. `haify_ha_promoter_status`: which HA resources are active there.
2. Only one node at a time. Every resource with a replica on this node needs another replica that is UpToDate, and the rest must keep a majority. If not, stop: taking the node down makes the resource unavailable or leaves one copy.
3. Unmount volumes that were mounted by hand on the node with `haify_resource_unmount`.
4. `haify_node_drain`. It marks the node maintenance (no new replicas or tiebreakers land on it until undrain) and returns the resources it moved. HA resources, gateways and haify-meta are evicted through drbd-reactor, as `haify_ha_evict` does. Every other Primary is demoted and promoted on the first replica in the node list that is diskful, UpToDate, connected, and not drained, offline or a WAN DR node; if that promote fails the node is promoted back. To choose the target yourself, move the Primary first with `haify_resource_set_role`.
5. If it reports resources it could not move, each is still Primary on the node, with the reason (no qualifying replica, held open, eviction failed). Fix that and drain again; draining a drained node is safe.
6. Confirm the node is Primary nowhere, then do the work.
7. After it is back: `haify_node_health_check`, then `haify_resource_status` until every replica on it is UpToDate again (it resyncs what it missed). Then `haify_node_undrain`. Roles do not move back and do not need to.

Do not reboot while a resync to or from another node is running.
