# Nodes changed IP address
After a DHCP change, or when nodes swap addresses.
Needs: admin

1. Every node must already answer on its new address as itself. The tool checks the hostname there and refuses otherwise.
2. `haify_node_set_address` once, with every node that changed in `moves`. One at a time cannot work when several changed: each change is pushed through the other nodes, whose addresses are stale too, and a swap makes two nodes share an address for a moment, which makes every DRBD config invalid and stops `drbdadm adjust` for all resources.
3. It rewrites the node registry, /etc/hosts and root's known_hosts on the nodes, and the address in every DRBD config the nodes take part in. It rebuilds the WAN tunnels of WAN resources on those nodes. `failed` lists every resource it could not update or whose tunnels it could not rebuild; a failed /etc/hosts or known_hosts update is only logged by the controller.
4. For each resource in `failed`: `haify_resource_repair`, and `haify_wan_repair` if it is a WAN resource. Both converge and are safe on a healthy resource.
5. `haify_node_health_check`, then `haify_resource_status` for each resource: all connected.
