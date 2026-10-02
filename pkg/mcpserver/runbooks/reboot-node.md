# Take a node out of service (reboot, repair)
Drain a node, do the work, bring it back.
Needs: admin

1. Survey. `sds_resource_list` and `sds_resource_status`: which resources have a replica on the node and which are Primary there. `sds_ha_promoter_status`: which HA resources are active there.
2. Only one node at a time. Every resource with a replica on this node needs another replica that is UpToDate, and the rest must keep a majority. If not, stop: taking the node down makes the resource unavailable or leaves one copy.
3. Move what an HA promoter runs (sds-meta, gateways) with `sds_ha_evict`, one resource at a time, waiting for each to land. See the planned-switchover runbook.
4. Unmount volumes that were mounted by hand on the node with `sds_resource_unmount`.
5. `sds_node_drain`. It changes DRBD roles only and returns the resources it moved. For each resource Primary on the node it promotes the first other node in the resource's node list, without checking that node's state, and on a WAN resource that can be the DR node; to choose, move the Primary yourself first with `sds_resource_set_role`. If it fails on a resource — mounted, held open, no other replica — it names it; fix that and drain again. What it already moved stays moved, and the resource it was working on may have no Primary until you promote it: check `sds_resource_status`.
6. Confirm the node is Primary nowhere, then do the work.
7. After it is back: `sds_node_health_check`, then `sds_resource_status` until every replica on it is UpToDate again (it resyncs what it missed). Then `sds_node_undrain`. Roles do not move back and do not need to.

Do not reboot while a resync to or from another node is running.
