# Realtime DRBD Sync Progress — Design

Date: 2026-07-01
Status: Approved (design), pending implementation plan

## Context

The SDS web UI shows each resource's node roles and disk states, but not the
live resync progress. When a resource is doing an initial or recovery sync,
operators want to see, in real time, **which node is syncing to which and how
far along** (e.g. `orange1 → orange2  86%`).

Two facts shape this work:

1. The controller's current live-status path parses **text** `drbdadm status`
   (`parseNodeStatesFromStatus`) and only extracts each node's role and disk
   state. It does not surface replication state or sync percentage. That text
   parser also recently produced a node/disk misattribution bug with diskless
   tiebreakers.
2. `drbdsetup status <res> --json` is available on all cluster nodes (drbd
   9.3.0) and returns structured, node-keyed data including
   `replication-state`, `peer-disk-state`, and `percent-in-sync` per peer.

## Decisions

- **Data source:** parse `drbdsetup status <res> --json` for live status
  (Approach A). Structured, keyed by node name / node-id — no index-based
  misattribution. Includes the diskless tiebreaker as a normal peer. The text
  parser is kept as a graceful fallback.
- **Scope:** full — always show each peer's replication state
  (Established / SyncSource / SyncTarget) plus the sync percentage during a
  resync, in both the Resources list page and the Status dialog. The diskless
  tiebreaker is shown as a peer row (closing the prior gap where it was omitted).
- **Polling:** idle = **zero polling**. A single seed fetch on page/dialog open
  (and after mutating actions); adaptive `refetchInterval` polls a resource
  every 2s **only while it has an active resync**, and stops the moment it
  reaches Established/100%.

## Architecture

```
 UI (TanStack Query, adaptive refetchInterval)
   seed fetch once → if syncing: poll 2s → stop at 100%
        │ GET /v1/resources/<name>/status
        ▼
 controller ResourceStatus / GetResource
        │ DRBDStatusJSON(hosts[0], res)  →  drbdsetup status <res> --json
        │ parseNodeStatesFromJSON()  (fallback: parseNodeStatesFromStatus)
        ▼
 per-node state: {role, disk, replication, sync_percent}, keyed by node name
```

The live-status path (`GetResource` and `ResourceStatus`) queries one node
(`hosts[0]`) with `drbdsetup status <res> --json`, parses it into per-node
states keyed by node name, and returns them. The queried node is the "local"
node; its peers (including the tiebreaker) come from the JSON `connections[]`.

## Data Model

`ResourceNodeState` (pkg/controller/resources.go) gains one field:

```go
type ResourceNodeState struct {
    Role        string
    DiskState   string
    Replication string  // existing; now populated: Established|SyncSource|SyncTarget|...
    SyncPercent float64 // NEW: percent-in-sync for a peer being synced (0..100)
}
```

- **Local node** (the queried node, `nodeAddresses[0]`): `Role` from JSON
  top-level `role`, `DiskState` from `devices[0].disk-state`. No `Replication`
  (a node has no replication relationship to itself); `SyncPercent` unset.
- **Each peer** (`connections[].name` → node name): `Role` from `peer-role`,
  `DiskState` from `peer_devices[0].peer-disk-state`, `Replication` from
  `peer_devices[0].replication-state`, `SyncPercent` from
  `peer_devices[0].percent-in-sync`.

**Direction (derived in the UI):** the local node = `resource.nodes[0]`. For a
peer whose `Replication == "SyncSource"`, the local node is the source →
render `local → peer  <percent>%`. For `"SyncTarget"`, render
`peer → local  <percent>%`.

Proto: `NodeResourceState` gains `double sync_percent = 4;` (it already has
`replication_state = 3`).

## Components / Files

| File | Change |
| --- | --- |
| `pkg/deployment/deployment.go` | Add `DRBDStatusJSON(ctx, hosts, resource)` running `sudo drbdsetup status <res> --json` |
| `pkg/controller/resources.go` | Add pure `parseNodeStatesFromJSON(output, localNode string) (map[string]*ResourceNodeState, error)`; add `SyncPercent` to `ResourceNodeState`; `GetResource`/`ResourceStatus` try JSON first, fall back to `parseNodeStatesFromStatus` on error |
| `api/proto/v1/sds.proto` | `NodeResourceState.sync_percent` (regenerate) |
| `pkg/controller/server.go` | Map `SyncPercent` into proto for `GetResource` + `ResourceStatus` |
| `web-ui/src/services/api.ts` | Node-state type gains `replicationState`, `syncPercent` |
| `web-ui/src/pages/ResourcesPage.tsx` | Status dialog: Replication column + progress bar when syncing + tiebreaker row. List page: per-resource sync badge (`orange1 → orange2 86%`) only when syncing. Adaptive `refetchInterval`. |

## Data Flow + Polling

- **Seed fetch:** Resources list mount, Status dialog open, and after mutating
  actions (create, set-primary, etc.) → one `ResourceStatus` per resource.
- **Adaptive `refetchInterval`** (a function passed to TanStack Query):
  - Returns `2000` if any peer has `replicationState ∈ {SyncSource, SyncTarget}`
    or `syncPercent < 100`.
  - Returns `false` otherwise (**stop; zero polling when idle**).
  - On reaching Established/100%, the next evaluation returns `false` → polling
    stops automatically.
- List page: each resource has its own adaptive query, so only actively syncing
  resources keep polling.

## Rendering

- **Status dialog** Node States table: add a Replication badge column and, for a
  syncing peer, a progress bar with `syncPercent`. Show the tiebreaker as a row
  (`Diskless` / `Established`).
- **List page:** per resource, a small badge shown **only while syncing**:
  `同步中 orange1 → orange2  86%` with a thin progress bar. Nothing extra when idle.

## Error Handling / Fallback

- `DRBDStatusJSON` fails (older drbd without `--json`, non-zero exit, or JSON
  parse error) → fall back to `parseNodeStatesFromStatus` (role/disk only, no
  percent). The feature degrades gracefully; the UI simply shows no progress bar.
- `percent-in-sync` absent (steady state / not syncing) → treat as `100`.
- `hosts[0]` unreachable → existing `ResourceStatus` error path is unchanged.

## Testing

### Unit (Go) — real captured fixtures

**Steady state** (no resync): local orange1 Primary/UpToDate; peer orange2
`replication-state: Established`, `peer-disk-state: UpToDate`,
`percent-in-sync: 100`; peer orange3 `replication-state: Established`,
`peer-disk-state: Diskless`, `peer-client: true`. Assert 3 node states, orange2
UpToDate with no active sync, orange3 present as Diskless tiebreaker.

**Mid-sync** (captured live at 3.55%):
```json
"connections": [
  { "name": "orange2", "peer-role": "Secondary",
    "peer_devices": [ { "replication-state": "SyncSource",
      "peer-disk-state": "Inconsistent", "percent-in-sync": 3.55 } ] },
  { "name": "orange3", "peer-role": "Secondary",
    "peer_devices": [ { "replication-state": "Established",
      "peer-disk-state": "Diskless", "peer-client": true,
      "percent-in-sync": 100.00 } ] }
]
```
Assert: orange2 `Replication == "SyncSource"`, `DiskState == "Inconsistent"`,
`SyncPercent == 3.55`; orange3 `DiskState == "Diskless"`,
`Replication == "Established"`; local orange1 Primary/UpToDate.

**Fallback:** invalid/empty JSON → `parseNodeStatesFromJSON` returns an error and
the caller falls back to the text parser (a `parseNodeStatesFromStatus` result).

### UI (Playwright, real 3-node cluster — required)

1. Force a resync: `ssh orange2 sudo drbdadm invalidate data` (orange2 discards
   its copy and re-syncs from Primary orange1).
2. Open the Resources page / Status dialog via Playwright.
3. Observe the live badge/progress `orange1 → orange2  NN%` climb across
   successive snapshots and **stop at 100%** (verifying adaptive polling starts
   and then stops). Capture screenshots as evidence.
4. Confirm that when idle, no polling occurs (network panel shows the status
   request is not repeated once sync completes).

## Out of Scope

- Backend push (SSE/WebSocket) — polling was chosen.
- ETA / throughput display (`estimated-seconds-to-finish`, `db/dt [MiB/s]` are
  present in the JSON and could be added later; not in this iteration).
- Rewriting the text parser or other callers of `parseNodeStatesFromStatus`;
  it stays as the fallback.
