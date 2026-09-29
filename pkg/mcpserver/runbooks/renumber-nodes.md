# Nodes changed IP address
After a DHCP change, or when nodes swap addresses.
Needs: admin

1. Every node must already answer on its new address as itself. The tool checks the hostname there and refuses otherwise.
2. `sds_node_set_address` once, with every node that changed in the same call. One at a time cannot work when several changed: each change is pushed through the other nodes, whose addresses are stale too, and a swap makes two nodes share an address for a moment, which makes every DRBD config invalid and stops `drbdadm adjust` for all resources.
3. It rewrites the node registry, /etc/hosts and known_hosts on the nodes and the address in every DRBD config the nodes take part in, and reports what it could not do.
4. `sds_resource_repair` for any resource the report lists as failed, and `sds_wan_repair` for WAN resources. Both converge and are safe on a healthy resource.
5. `sds_node_health_check`, then `sds_resource_status` for each resource: all connected.
