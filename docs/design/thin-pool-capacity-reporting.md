# Thin pool capacity reporting (design)

Date: 2026-08-09
Status: **Implemented.** Found while recovering a real outage on a test
cluster.

Pool capacity used to be reported as **VG allocation** only. For an LVM thin
pool that number says nothing about whether the next write will succeed: the
thin pool LV holds almost every extent in the group, so `vg_free` is close to
zero for the whole life of the pool. `haify pool convert-thin` creates the
pool with `lvcreate -l 100%FREE` and grows it with `lvextend -l +100%FREE`,
leaving `vg_free` at exactly 0; `haify pool create --type lvm-thin` without
`--size` uses `-l 95%FREE`, leaving 5%. Either way the Pools page showed a
nearly or completely full bar regardless of whether the pool was empty or about
to fail writes.

It therefore could not warn about the failure mode that actually takes a node
down: the thin pool running out of **data** (or **metadata**) space.

## The outage that exposed it

`app0` is a 6G volume replicated across four nodes, each on a 9.75G thin
pool that also holds ~28 hourly scheduled snapshots. Steady-state occupancy was
91–93%. The snapshot scheduler was working correctly; the pool was simply sized
with no headroom.

`node1` was network-isolated for about a day. On reconnect DRBD started a full
resync, which has to write the entire 6G volume as fresh allocations. The pool
hit 100% and the kernel dropped the disk:

```
haifythin  Data% 100.00  Attr twi-aotzD-      # D = out-of-data-space
drbd app0/0 drbd2: Cannot write resync data to local disk.
drbd app0/0 drbd2: disk( Failed -> Diskless )
```

The node then reported `disk:Diskless` on a resource where it is configured
diskful — a symptom that reads like a configuration error and is not one.

Throughout, the Pools page looked the same as it did at 30–47% occupancy:
`0 GB free`, solid bar.

A secondary defect: GB values were computed by integer division, so a 20 GiB
disk (`21470642176` bytes after PV metadata, 19.9961 GiB) showed as 19 GB.

## What the code does

### Collection

- `pkg/deployment/lvmthin.go` — `LVSThinReport` runs one `lvs` per host
  covering every volume group:

  ```
  sudo lvs --noheadings --nosuffix --units b --separator '|' \
    -o vg_name,lv_name,segtype,lv_size,data_percent,metadata_percent,lv_attr
  ```

  Thin pools are picked out by `segtype`, not by name. `lv_size` is the pool's
  data capacity, which `data_percent` is a percentage of.
- `pkg/controller/poolthin.go` — `parseThinReport` turns the output into a
  `PoolThinInfo` (`PoolLV`, `SizeBytes`, `DataPercent`, `MetaPercent`,
  `OutOfSpace`) per VG; `thinUsageByPool` / `readThinUsage` feed it into
  `PoolInfo.ThinUsage` on the pool listing paths. `OutOfSpace` is read from the
  volume health field of `lv_attr` (`thinOutOfSpace`), i.e. LVM's own verdict,
  not inferred from a percentage.

Data and metadata are carried separately: metadata exhaustion stops writes as
completely as data exhaustion, and the two fill at unrelated rates.

`PoolInfo` also carries `TotalBytes` / `FreeBytes`, and `bytesToGB` rounds to
the nearest GiB instead of truncating.

### API

`PoolInfo` in `api/proto/v1/haify.proto`, fields 17–23: `thin_pool_lv`,
`thin_size_bytes`, `thin_data_percent`, `thin_metadata_percent`,
`thin_out_of_space`, `total_bytes`, `free_bytes`. `total_gb` / `free_gb` still
describe the volume group. An empty `thin_pool_lv` — not a zero percentage — is
how "no thin pool" is told apart from "a thin pool at 0%".

### Display

- `haify pool list` prints, for a thin pool, the thin pool's own free/total
  and `N% used` (plus `OUT OF SPACE` when LVM says so).
- `haify pool get` prints the thin pool's total/free, the VG size and
  unallocated space on a separate line, the thin LV and its size, and data and
  metadata percentages. An out-of-space pool prints a warning on stderr.
- `web-ui/src/pages/PoolsPage.tsx` drives the bar from `thinDataPercent` when
  `thinPoolLv` is set, colours it at 85% / 95%, and falls back to VG figures for
  a pool with no thin LV.
- The Proxmox plugin's `status()` reports `thinSizeBytes` and the space
  `thinDataPercent` leaves unused for a thin pool (smallest node wins), and VG
  figures only for a group without one or a storage pinned to `storagetype lvm`.

### Alerts

`pkg/alert/alert_pools.go` — `checkPools` reads pools through the `PoolLister`
interface every poll and, for each pool with a thin LV, raises:

| Event | Severity | Condition |
|---|---|---|
| `pool.data_near_full` | warning | data ≥ near-full and < full threshold |
| `pool.data_full` | critical | data ≥ full threshold |
| `pool.metadata_near_full` | warning | metadata ≥ near-full and < full threshold |
| `pool.metadata_full` | critical | metadata ≥ full threshold |
| `pool.out_of_space` | critical | LVM reports the pool out of data space |

Near-full and full are mutually exclusive, so crossing the critical threshold
resolves the warning in the same poll. Pools are keyed by name and node. The
monitor records which lister raised each condition (`owners`); a condition is
cleared as vanished only when its own source answered that poll, so a failed
pool listing does not resolve outstanding pool alerts.

Configuration (`/etc/haify/controller.toml`):

```toml
[alert]
check_pools = true            # default
pool_near_full_percent = 85.0 # default
pool_full_percent = 95.0      # default
```

The defaults match `ThinPoolNearFullPercent` / `ThinPoolFullPercent` in
`poolthin.go`. The gap between them is deliberate: past 95% a pool may not have
room for a full DRBD resync of the volumes it holds, because a resync
reallocates every block.

### Metrics and placement

- `pkg/controller/metricsobserver.go` — `poolCapacityBytes` exports a thin
  pool's own size and used bytes (`thin_size_bytes × data_percent`) to
  Prometheus; a pool without a thin LV exports its VG figures.
- `pkg/controller/placement.go` — `poolPlacementCapacity` treats a thin pool's
  free space as a ranking signal, not a ceiling: a thin volume allocates as it
  is written, so its nominal size is not required up front. A thin pool whose
  data or metadata is at or above `ThinPoolFullPercent`, or that LVM flags out
  of space, is excluded (`thinPoolExhausted`).

## Snapshot listing: `haify resource snapshot list` saw no LVM snapshots

Turned up while clearing space during the same recovery.

`LVListSnapshots` in `pkg/deployment` used to carry a literal `VG/LV` — the
placeholder from the `lvs` man page — so `lvs` printed every snapshot and then
exited 5 because no such volume existed. The scheduler reads output regardless
of exit status, so retention kept working; `StorageManager.ListLvmSnapshots`
skipped any host that dispatch marked failed, so no snapshot was ever visible
through the listing path, manual or scheduled.

Current command:

```
sudo lvs -S lv_role=snapshot -o lv_name,lv_size,lv_time,origin \
  --noheadings --nosuffix --units b --separator='|' <vg>
```

The `|` separator matters because `lv_time` contains spaces. Alongside that:

1. A failed command now returns an error instead of an empty list.
2. Size and creation time are parsed (size was hardcoded to 0).
3. `--resource` filters: the request carries the resource, and the controller
   filters by that resource's backing volumes as recorded in the database.
   The `haify_snapshot_list` MCP tool uses the same path.

The controller enumerates snapshots live and keeps no snapshot table, so
removing one by hand with `lvremove` remains safe.

## Sizing

A thin pool holding a volume plus N scheduled snapshots needs enough free space
to absorb a **full resync of the volume**, not just the snapshot deltas. Sizing
to the delta is what made 91% the normal state on `app0`.

When the thin pool takes every free extent of the VG, LVM's
`thin_pool_autoextend_threshold` cannot help, because there is nothing left to
extend into. Leaving VG headroom and enabling autoextend is the LVM-side
safeguard; the alerts above are the Haify-side one.

`haify pool add` on a thin-backed group extends the thin pool with
`lvextend -l +95%FREE` after `vgextend` (metadata first, sized at 1% of the
grown pool as convert-thin does), so a new disk adds usable thin capacity
rather than sitting unallocated in the group.
