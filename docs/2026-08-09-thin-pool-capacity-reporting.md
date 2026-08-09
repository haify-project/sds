# Pool capacity reporting is blind to thin pool exhaustion (design)

Date: 2026-08-09
Status: **Implemented**, not yet deployed to the home cluster. Found while
recovering a real outage there.

Implemented:
- `pkg/deployment/lvmthin.go` — `LVSThinReport`, one cluster-wide `lvs` on the
  listing path, mirroring `LVSCacheReport`.
- `pkg/controller/poolthin.go` — `parseThinReport` / `thinUsageByPool` /
  `readThinUsage`, plus `thinOutOfSpace` reading LVM's volume health field.
- `PoolInfo.ThinUsage` folded into all three listing paths; `TotalBytes` /
  `FreeBytes` added and the GB conversion switched from truncation to rounding.
- Proto fields 17–23; `pbPoolInfo` carries them.
- `pkg/alert` — `PoolLister`, `checkPools`, five event types, configurable
  thresholds, and an `owners` map replacing the key-shape ownership guess.
- `alert.check_pools` / `pool_near_full_percent` / `pool_full_percent`, on by
  default.
- `PoolsPage.tsx` drives the bar from `data_percent` when there is a thin pool;
  `sds pool list` / `pool get` report utilisation.

`make ci` green. `parseThinReport` verified against real 30-line `lvs` output
from node-e: it picks `sdsthin` out and reports 44.96% data / 8.19% metadata,
matching `lvs` directly.

The snapshot listing defect at the end of this document is also fixed:
`LVListSnapshots` lost its stray `VG/LV` placeholder, failures are no longer
swallowed, size and creation time are parsed, and `--resource` filters. Verified
on node-e: the corrected command returns `exit=0` and 27 rows, all of which
parse.

Not deployed to the home cluster. `sds-controller` is
`PartOf=drbd-services@sds-meta.target`, so restarting it on the active node
relocates the management plane; standby binaries go first.

The Pools page reports **VG allocation**. In every deployment where the thin
pool is created with `lvextend -l +100%FREE` — which is what SDS itself does —
`vg_free` is permanently `0`, so the page shows `0 GB free` and a 100%-full bar
forever, regardless of whether the pool is empty or about to fail writes.

It therefore cannot warn about the one failure mode that actually takes a node
down: the thin pool running out of **data** space.

## The outage that exposed it

`openclaw` is a 6G volume replicated across four nodes, each on a 9.75G thin
pool that also holds ~28 hourly scheduled snapshots. Steady-state occupancy was
91–93%. The snapshot scheduler was working correctly — it prunes on schedule
(`pkg/controller/schedule.go`); the pool was simply sized with no headroom.

`node-a` was network-isolated for about a day. On reconnect DRBD started a full
resync, which has to write the entire 6G volume as fresh allocations. The pool
hit 100% and the kernel dropped the disk:

```
sdsthin  Data% 100.00  Attr twi-aotzD-      # D = out-of-data-space
drbd openclaw/0 drbd2: Cannot write resync data to local disk.
drbd openclaw/0 drbd2: disk( Failed -> Diskless )
```

The node then reported `disk:Diskless` on a resource where it is configured
diskful — a symptom that reads like a configuration error and is not one.

**Throughout all of this the Pools page looked exactly the same** as it does
now, at 30–47% occupancy: `0 GB free`, solid bar. Before and after the failure
were visually identical.

## Where it comes from

`pkg/controller/storage.go:212` (and the multi-host variant at `:293`):

```go
"sudo vgs --noheadings --units b --separator '|' -o vg_name,vg_size,vg_free"
```

`vg_free` is *unallocated extents in the volume group*. Once the thin pool LV
claims every extent, that is structurally zero and stays zero. It says nothing
about how full the thin pool is.

Four sites then do the same conversion (`storage.go:244`, `:322`, `:535`,
`:606`):

```go
TotalGB: totalSize / 1024 / 1024 / 1024,
FreeGB:  freeSize / 1024 / 1024 / 1024,
```

`web-ui/src/pages/PoolsPage.tsx:176`:

```ts
const total = Number(pool.totalGb);
const free  = Number(pool.freeGb);
const usedPercent = total > 0 ? ((total - free) / total) * 100 : 0;
```

With `free == 0`, `usedPercent` is always exactly 100.

### Measured on the four-node cluster, 2026-08-09

All four had just been expanded from 10G to 20G; occupancy is genuinely low.

| Node | UI shows | Actual thin pool `data_percent` |
|---|---|---|
| node-a | 0 GB free / 19 GB total | 30.78% |
| node-b | 0 GB free / 19 GB total | 46.55% |
| node-e | 0 GB free / 19 GB total | 45.50% |
| node-c | 0 GB free / 19 GB total | 38.55% |

## Secondary defect: integer truncation loses up to 1 GB

`vgs` reports `21470642176` bytes for a 20 GiB disk — 19.9961 GiB after PV
metadata. Integer division truncates to **19**, so a freshly created 20 GB pool
presents as 19 GB and looks like it lost a gigabyte. Round, or carry one decimal
place, rather than truncating.

## Proposed fix

The plumbing already exists: `pkg/controller/poolcache.go:176` calls
`deployment.LVThinPoolIn(ctx, address, poolName)` to resolve the thin pool LV
name on a host. Getting its utilisation is one more `lvs` away.

1. **Collect it.** Alongside the existing `vgs`, run:

   ```
   sudo lvs --noheadings --units b --separator '|' \
     -o lv_name,lv_size,data_percent,metadata_percent <vg>/<thinpool>
   ```

   Note `data_percent` and `metadata_percent` are *both* needed —
   metadata exhaustion fails a pool just as hard as data exhaustion, and the
   two fill at unrelated rates.

2. **Carry it.** Add `ThinDataPercent` / `ThinMetaPercent` to `PoolInfo` and to
   the proto/DB pool record. `PoolInfo.Thin` already exists
   (`storage.go:61`), so the UI can tell which pools have meaningful values.

3. **Render the right number.** In `PoolsPage.tsx`, when `thin` is set, drive
   the bar from `data_percent` and label it "pool used". Keep VG
   total/free as a secondary line — it is still worth seeing that the VG is
   fully committed, it just is not a health signal. Surface
   `metadata_percent` too, at least once it crosses a threshold.

4. **Fix the truncation** in all four conversion sites.

5. **Alert on it.** A pool above ~85% data or metadata deserves an event on the
   existing notification path. That is the difference between noticing this
   before a resync and noticing it after a node goes `Diskless`.

### Backward compatibility

`ThinDataPercent` is absent for plain VGs and for pools whose agent has not
been upgraded. Treat the zero value as "unknown" and fall back to the current
VG rendering — do not draw a 0%-used bar for a pool that simply did not report.

## Related: `sds resource snapshot list` saw no snapshots at all — fixed

Turned up while clearing space during the same recovery. Worth fixing in the
same pass because it blocks operators from acting on a full pool.

**Correction.** An earlier revision of this document blamed a naming mismatch —
that the scheduler's `<volume>_sched_<timestamp>` names did not match what the
manual snapshot path looked for. That was wrong, and it was a guess from the
symptom rather than from the code. The names were never the problem.

The command in `pkg/deployment/deployment.go` carried a literal `VG/LV`:

```
sudo lvs -S lv_role=snapshot VG/LV -o lv_name,lv_size,lv_time --noheadings --separator=' ' <vg>
```

`VG/LV` is a placeholder from the lvs man page that was never substituted. lvs
still printed every snapshot it found on stdout, but **exited 5** because no
such volume existed. Verified on node-e: that exact command prints all 27
snapshots and returns `exit=5`.

The two consumers of the command then diverged on how forgiving they were:

- The scheduler reads output with `execLines` regardless of exit status, so
  retention kept working — which is precisely why nobody noticed.
- `StorageManager.ListLvmSnapshots` looped over `if r.Success`, dispatch marks
  a host failed on a non-zero exit, and 27 good rows were discarded in silence.

So it was not that scheduled snapshots were invisible: **no** snapshot was ever
visible through this path, manual or scheduled, on any node.

Fixed by dropping the placeholder, switching to a `|` separator (lv_time
contains spaces, so whitespace splitting read the date as three extra columns),
and adding `origin`. Verified on node-e: `exit=0`, 27 rows, all parsed.

Three further defects on the same path, fixed with it:

1. **A failed command returned an empty list, not an error.** That silence is
   what let this survive; `ListLvmSnapshots` now fails loudly.
2. **`SizeGB` was hardcoded to `0`** with the comment "LVM list output needs
   parsing for size", and `lv_time` was fetched and discarded. Both are parsed
   now.
3. **`--resource` was accepted, printed in the heading, and then ignored.** The
   LVM branch listed the whole volume group, so `--resource a` and
   `--resource b` returned identical lists of everything in the pool, each
   under a heading naming a different resource. The request now carries
   `resource`, and the controller filters by that resource's backing volumes
   read from the database — not rebuilt from the `<name>_data` /
   `<name>_vol<K>` convention, which lives in resource creation and would drift.
   The same fix applies to the `sds_snapshot_list` MCP tool, whose description
   likewise claimed to list "a DRBD resource's data volume".

`lvremove` by hand remains safe — the controller enumerates snapshots live via
`lvs -S lv_role=snapshot` and keeps no snapshot table in `sds.db` — but it is no
longer the only thing that works.

## Note on sizing, for whoever tunes the defaults

The pools were grown 10G → 20G on 2026-08-09, which drops steady state to
~45%. That is a workaround, not a fix: a thin pool holding a volume plus N
hourly snapshots needs enough free space to absorb a **full resync of the
volume**, not just the snapshot deltas. Sizing to the delta is what made 91%
the normal state.

Also worth reconsidering: `lvextend -l +100%FREE` leaves the VG with no spare
extents, which is exactly why LVM's own guard rail is unavailable —

```
WARNING: You have not turned on protection against thin pools running out of space.
WARNING: Set activation/thin_pool_autoextend_threshold below 100 to trigger
         automatic extension of thin pools before they get full.
```

`thin_pool_autoextend` cannot help when there is nothing left to extend into.
Leaving deliberate VG headroom and enabling autoextend would have contained
this incident without any UI change at all.
