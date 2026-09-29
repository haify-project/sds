# Planned switchover of an HA resource
Move the controller (sds-meta) or a gateway to another node, on purpose.
Needs: admin

1. `sds_ha_promoter_status` shows where the resource is active now. `sds_resource_status` for it: every replica must be UpToDate and connected. A takeover by a replica that is behind, or that lacks quorum, fails or costs the service.
2. `sds_ha_status` lists what rides the resource. For sds-meta that is the controller, its VIP, the AI Copilot and this MCP server.
3. `sds_ha_evict` with the resource name. It moves that one resource and nothing else on the node.
   - sds-meta: the call returns as soon as the switchover is launched and this connection drops for several seconds, because this server moves too. That is expected; reconnect and go on.
   - any other resource: the call returns once another node has taken over, and fails if none did.
4. `sds_ha_promoter_status` again: the node changed and the status is active. After sds-meta, `sds_node_list` answering means the controller is up on the new node.

NFS and iSCSI clients see a pause of some seconds, not I/O errors.

Do not stop or restart a drbd-reactor-managed service by hand: the promoter reads it as a fault and fails the resource over, unplanned. Do not evict two resources back to back; wait for the first to land.
