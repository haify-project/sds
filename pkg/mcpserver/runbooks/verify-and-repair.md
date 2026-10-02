# Check that replicas hold the same data, and repair
Online verify, and what its numbers mean.
Needs: operate

1. `sds_resource_verify` on the resource. It reads every replica in full, so it loads the disks while it runs; do it off-peak. It compares from the Primary unless you name a node. Call it again while the phase is running to follow it.
2. Read the result. `found_kib` is what this verify itself found. `out_of_sync_kib` is everything DRBD has marked out of sync with that peer, including marks left by earlier verifies, interrupted resyncs and reconnects. A verify only adds marks; it never clears them. `found_kib` is only known when `baseline_known` is true: the controller must have seen the count before this verify started, which a controller restart or failover in between loses.
   - all zero: the copies are the same.
   - `found_kib` above zero: differences. A few blocks that vanish on a second verify were writes in flight; the same blocks again are real.
   - only old marks (`found_kib` zero): usually harmless leftovers. If you want certainty before copying anything, compare the backing devices by hash on both nodes.
3. `sds_resource_verify` with resync, from the Primary. It copies that node's data over every marked block, which makes the copies identical and clears the marks, and is harmless when they already were. With no marked block on any connected replica there is nothing to copy and it says so. It refuses a source that is not Primary unless another replica agrees with it, and never overwrites a Primary.
4. Verify again: zero everywhere. The `resource.out_of_sync` alert resolves.

Never resync from a copy you have not decided is right. On a thin pool, check headroom first: a resync can allocate blocks the target had not written.
