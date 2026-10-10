# Realtime DRBD sync progress (design)

Date: 2026-07-01
Status: Implemented

## Context

The web UI showed each resource's node roles and disk states but not resync
progress. During an initial or recovery sync, operators need to see which peer
is syncing and how far along it is.

The original live-status path parsed the text output of `drbdadm status`
(`parseNodeStatesFromStatus`), which yields only role and disk state and had
already misattributed a diskless tiebreaker's disk state to another node.
`drbdsetup status <res> --json` is structured, keyed by node name, and carries
per-peer `replication-state`, `peer-disk-state`, `percent-in-sync` and, during
a resync or verify, `percent-resync-done`.

## Decisions

- Data source: `drbdsetup status <res> --json`, with the text parser kept as
  a fallback.
- Scope: every peer's replication state plus the sync percentage, in the
  resources table and the resource detail view. A diskless tiebreaker or
  data client appears as an ordinary peer.
- Polling: none while idle. A resource's status is fetched once; it
  is re-polled every 2 s only while one of its peers is resyncing, and polling
  stops when the resync ends. Backend push (SSE/WebSocket) was not chosen.

## Backend

```
 UI (TanStack Query, refetchInterval = syncPollInterval)
        │ GET /v1/resources/{name}/status
        ▼
 controller ResourceStatus / GetResource  (pkg/controller/resource_query.go)
        │ text `drbdadm status` → parseNodeStatesFromStatus
        │ DRBDStatusJSON(<host that answered>, res) → drbdsetup status <res> --json
        │ parseNodeStatesFromJSON (pkg/controller/resource_status_parse.go)
        ▼
 per-node ResourceNodeState, keyed by node name
```

The JSON query goes to the same host whose text status answered: the JSON
"local" node is whichever node was asked, so querying a different one would
mislabel every row. If the JSON call fails, exits non-zero or does not parse
(e.g. a DRBD without `--json`), the text-parsed states are kept and the UI
shows no percentage.

`parseNodeStatesFromJSON`:

- Local node: `Role` from the top-level `role`, `DiskState` and `Quorum`
  from `devices[0]`. No replication relationship to itself;
  `SyncPercent = 100`.
- Each peer (`connections[].name`): `Role` from `peer-role`, `Connection`
  from `connection-state`, `TLS`, `DiskState` from
  `peer_devices[0].peer-disk-state`, `Replication` from
  `peer_devices[0].replication-state`, `OutOfSyncKiB` summed over all peer
  devices. `SyncPercent` is `percent-resync-done` when present, otherwise
  `percent-in-sync`, otherwise 100.

`ResourceNodeState.SyncPercentKnown` records whether the percentage came from
DRBD; only then is it exported to metrics and alerts.

Proto `NodeResourceState`: `role = 1`, `disk_state = 2`,
`replication_state = 3`, `sync_percent = 4`, `node = 5` (the Haify node name;
the map key is the DRBD host name).

## Web UI

`web-ui/src/pages/resources/replication.ts`:

- `isPeerSyncing(state)`: true for `SyncSource`, `SyncTarget`,
  `PausedSyncS/T`, `StartingSyncS/T`, `WFBitMapS/T`, or any other
  non-`Established`, non-`Off` state below 100%.
- `syncPollInterval(query)`: `2000` while any peer is syncing, otherwise
  `false`. Used as `refetchInterval` by `ResourcesPage.tsx` and
  `resources/ResourceDetail.tsx`, so only resources that are syncing keep
  polling.
- `replicationSummary(status)`: the table's one-line Replication column:
  `syncing NN%` (tooltip: replication state and peer), `no quorum`, the first
  non-UpToDate replica's disk state (Diskless is not treated as degraded), or
  `UpToDate`. The table's "Syncing" filter counts resources in the first state.

`ResourceDetail.tsx` shows a progress bar (`SyncBar`) on each syncing peer's
row; `components/ResourceTopology.tsx` appends the percentage to a syncing
peer's label.

## Not built

- ETA / throughput (`estimated-seconds-to-finish`, `db/dt`) are in the JSON but
  not shown.
