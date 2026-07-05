# SDS × sds-proxy — optional WAN replication (design)

Date: 2026-07-04
Status: **Phase 1 (MVP) implemented.** **Opt-in only; the LAN default path is
untouched.**

Implemented (Phase 1a–1d):
- Data model + proto: `database.Resource` and `CreateResourceRequest` carry
  `WANMode/DRNode/DREndpoint/WANPort` (all zero ⇒ LAN).
- DRBD config WAN branch in `generateDrbdConfig`: protocol A + pull-ahead +
  loopback routing (pinned LAN output with `TestGenerateDrbdConfigLANUnchanged`).
- `pkg/wanproxy`: shared-CA PKI, dialer/acceptor TOML matching sds-proxy's schema,
  and `Provision`/`Deprovision` (systemd `sds-proxy@<resource>`), Provision before
  `drbdadm up`.
- Wiring: `CreateResourceWithVolumes(... *WANSpec)`, gRPC master-switch validation,
  CLI `--wan/--dr-node/--dr-endpoint/--wan-port`.

Validated: LAN create/delete unchanged (real IPs, protocol C) on the arm64 cluster;
all WAN validation rejections; `go test ./...` green. **Cross-WAN loop exercised
end-to-end** on a 2-node arm64 setup (one node as the DR site, its IP as
`--dr-endpoint`, arm64 `sds-proxy` staged at `/usr/local/bin/sds-proxy`): `resource
create --wan` provisioned the dialer/acceptor pair, the mTLS WAN link and both DRBD
legs came up, DRBD reached Connected/UpToDate through the proxy, and data written on
the primary read back identically on the DR node. Two fixes came out of it:
`DistributeConfig` now chunks large files (the ~7 MB proxy binary overflowed a
single `echo` arg), and sds-proxy backs off on instant reconnects (a zero-latency
reconnect flood had delayed the initial connection). A real WAN (e.g. aliyun↔orange)
adds the network latency that naturally paces establishment.

## Guiding principle

WAN replication is a **pure opt-in add-on**. A resource created without any WAN
flag behaves EXACTLY as today: direct LAN DRBD, protocol C, real peer IPs, no
proxy, no new dependencies. Every WAN code path is gated behind an explicit flag,
so the default cannot regress. `WANMode == false` is the zero value everywhere.

## What "WAN mode" gives a resource

A resource replicated across the internet (primary site ↔ DR site, DR possibly
behind NAT) via a per-resource **sds-proxy** pair: protocol A async + buffering +
zstd + mTLS + pull-ahead + ping-ack synthesis (survives a dead WAN in Ahead,
recovers with a partial resync — all already validated in sds-proxy).

## Opt-in surface

CLI (absent flags ⇒ LAN, unchanged):
```
sds-cli resource create --name data --nodes site1 --pool vg0 --size 10 \
    --wan --dr-node site2 --dr-endpoint 47.109.108.170 [--wan-port 37901]
```
- `--wan` is the master switch. Without it, `--dr-*` are rejected and nothing
  below runs.
- `--dr-node` — the remote (DR) node; must be a registered node.
- `--dr-endpoint` — the DR site's public WAN address the primary dials (the DR is
  typically the acceptor / public side; the primary may be behind NAT and dials
  out, mirroring the aliyun↔orange topology).
- `--wan-port` — WAN mTLS port (default a random >3000 per resource).

API: add optional `bool wan`, `string dr_node`, `string dr_endpoint`,
`uint32 wan_port` to `CreateResource` (proto3 → default false/empty ⇒ LAN).

## Data model (backward-compatible)

Extend `database.Resource` (pkg/database) with optional fields — existing records
deserialize with these zero-valued, i.e. LAN:
```go
type Resource struct {
    // ... existing ...
    WANMode    bool   // false = LAN (default)
    DRNode     string // remote node name (WAN only)
    DREndpoint string // DR public WAN address (WAN only)
    WANPort    int    // WAN mTLS port (WAN only)
}
```

## Config generation — one branch, LAN untouched

`ResourceManager.generateDrbdConfig` already parameterizes `protocol` and takes a
`drbdOptions map` with `section/key` entries. WAN mode is a **new branch that only
runs when `WANMode`**:

- **LAN (default):** current output verbatim. No change.
- **WAN:** force protocol A and inject the pull-ahead options (unless the user
  overrode them), then emit **loopback-routed** addresses instead of peer IPs:
  ```
  net { protocol A; on-congestion pull-ahead; congestion-fill 2M;
        congestion-extents 500; ping-timeout 20; }
  on <primary> { ... address 127.0.0.1:<P+9>; }   # binds locally; connects to the local dialer
  on <dr-node> { ... address 127.0.0.1:<P>;   }   # dialer(primary):drbd_listen = acceptor(dr):drbd_listen = 127.0.0.1:<P>
  ```
  The primary's DRBD connects to `127.0.0.1:<P>` = the local **dialer**; the DR's
  DRBD listens on `127.0.0.1:<P>` where the local **acceptor** dials it. This is
  the asymmetric-but-both-loopback routing proven with `wantest`. (Ports: DRBD-vs-
  proxy on the same host must differ; use `P` for the proxy's drbd_listen and
  `P+9` for the node's own bind.)

The per-host asymmetry (each host's own address must be loopback so its connect to
the local proxy works) means WAN mode writes a **per-host** `.res` — SDS already
deploys per host over dispatch, so this is a rendering detail, not new plumbing.

## sds-proxy orchestration — new `pkg/wanproxy`

Only invoked on WAN resource create/delete. Reuses the dispatch/SSH deploy layer.

1. **PKI:** one shared CA + leaf (SAN `sds-proxy`, server+client EKU) for the
   controller, generated once and cached; distributed to both sites. (Per-resource
   PKI is possible later; shared CA is the MVP.)
2. **Config gen:** render dialer config on the primary
   (`drbd_listen=127.0.0.1:P`, `peer=<dr-endpoint>:<wan-port>`) and acceptor config
   on the DR node (`wan_listen=0.0.0.0:<wan-port>`, `drbd_listen=127.0.0.1:P`),
   with `on_congestion=pull-ahead`, `synthesize_ping_acks=true`.
3. **Deploy:** push the `sds-proxy` binary (once per node) + certs + config; install
   a per-resource systemd unit `sds-proxy@<resource>` and enable+start it.
4. **Lifecycle:** resource delete → stop+remove the unit + config; resource status
   → surface proxy `active`/WAN health. Order: proxy up **before** `drbdadm up`.

## HA / DR semantics (important, document loudly)

- WAN = protocol A (async). The DR node can lag by the buffer, so **failover to the
  DR site is a manual DR-recovery action, NOT automatic drbd-reactor failover** —
  auto-promoting a possibly-behind async secondary risks data loss. The
  drbd-reactor promoter still runs **within the primary site** for local HA.
- Two endpoints only (primary site ↔ DR site). No 3-way WAN.
- Surface the async lag / potential data-loss window in `resource status`.

## Firewall / reachability

The DR endpoint's `wan-port` must be reachable from the primary's egress (one TCP
port; UDP not needed). `pkg/wanproxy` documents/validates this; it cannot open
cloud security groups itself.

## Phasing

1. **MVP:** flags + data model + WAN-mode config gen + `pkg/wanproxy` (shared PKI,
   deploy, per-resource systemd) + docs. Manual DR failover.
2. Later: per-resource PKI, `resource status` WAN metrics (buffer/throughput once
   sds-proxy M3 exposes them), seamless DR-failover tooling, multi-DR.

## Non-goals

- Not changing the LAN path in any way.
- Not automatic cross-WAN HA failover (async safety).
- Not replicating DRBD Proxy's large buffer / throttling (separate sds-proxy work).
