# Add a replica
Give a resource another full copy on another node.
Needs: operate

1. `haify_resource_status`: every current replica UpToDate. `haify_node_list`: the node is registered. `haify_pool_list`: its pool has room for the whole volume; on a thin pool leave headroom for snapshots. The add is refused when the pool has less free space than the volume, because the sync writes all of it and a full thin pool fails those writes and drops the new disk. Only the CLI can override that (`--ignore-free-space`); this tool cannot, and should not.
2. If the node is the resource's quorum tiebreaker, remove it as tiebreaker first (`haify_resource_set_tiebreaker` with an empty node); if it is a diskless client, `haify_resource_detach_diskless`. The add is refused otherwise. It is also refused when the resource replicates over TLS and the node is not ready for it (`haify_replication_tls_status` says what is missing), or when the resource is encrypted and the node cannot host an encrypted volume (cryptsetup and dm-crypt).
3. `haify_resource_add_replica`.
4. `haify_resource_status` until the new node is UpToDate. Until then it is not a redundant copy: it is syncing the whole volume from the Primary, which loads that node's disk and the network.
5. Optional: `haify_resource_verify` to compare it with the Primary afterwards.

The resource stays usable throughout. A resource made from a profile can be brought to the profile's replica count with `haify_resource_profile_adjust` (dry run first).
