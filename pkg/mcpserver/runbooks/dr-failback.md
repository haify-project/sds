# Fail a WAN resource back to its primary site
Return to the primary site after the DR node was promoted.
Needs: admin

Failover itself is not a tool. It force-promotes the DR node and loses the writes still in the WAN buffer, so it is done at the CLI on purpose: `sds-cli resource dr-failover <resource> --yes`.

1. `sds_resource_status`: the DR node is Primary and the resource is a WAN resource.
2. Say plainly to whoever owns the data: what the primary-site nodes wrote after the link was cut is discarded in favour of the DR's copy. Get their yes.
3. Stop the service on the primary-site nodes and unmount the volume there. `sds_resource_dr_failback` refuses while anything holds it open, and names the node.
4. `sds_resource_dr_failback` with the wait time you can afford. It rejoins the primary site (discarding its writes), waits for the resync from the DR, then demotes the DR and promotes the primary site. Its phase is `resyncing` until it is done: call it again until `done`.
5. For the last step the DR node must be unmounted too; if it is still in use the call says so and stops. Unmount and call again.
6. `sds_resource_mount` on the new Primary and start the service there.

It can be repeated at any point; each call carries on from where the resource stands.
