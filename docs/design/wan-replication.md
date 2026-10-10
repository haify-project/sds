# Haify × haify-proxy: optional WAN replication (design)

Date: 2026-07-04 (updated to match the current implementation)
Status: implemented. Opt-in only; the LAN path is unchanged.

## Guiding principle

WAN replication is opt-in. A resource created without any WAN flag behaves
exactly as a LAN resource always has: direct DRBD between real peer
addresses, the requested protocol (default C), no proxy. Every WAN code path
is gated behind `WANMode`, whose zero value is `false`.

## What WAN mode gives a resource

Replication across the internet from a primary site (possibly behind NAT) to
one DR node through one haify-proxy tunnel per WAN leg. The tunnel
provides protocol A, DRBD pull-ahead, zstd compression, mTLS, a 4 GiB buffer,
and ping-ack synthesis on the acceptor, so DRBD survives a dead WAN in `Ahead`
and recovers with a partial resync.

There are two shapes:

- Two endpoints: one primary-site node and the DR node. The whole resource is
  protocol A.
- Primary site + DR: several primary-site replicas in a synchronous LAN mesh,
  plus one asynchronous DR copy. Only the WAN legs are protocol A; the LAN
  mesh keeps the requested protocol.

## Opt-in surface

At create time (without these flags the resource is LAN):

```
haify resource create --name data --port 7000 --size 10G \
    --nodes site1-a,site1-b --wan --dr-node dr1 --dr-endpoint 203.0.113.7 \
    [--wan-port 37901] [--wan-egress-address 10.0.0.5]
```

- `--wan` is the master switch. Without it the DR flags are rejected.
- `--nodes` is required in WAN mode (no auto-placement) and lists the
  primary-site replicas.
- `--dr-node`: the DR node, which must be registered.
- `--dr-endpoint`: the DR site's public address, which the primary site dials.
  The DR is the acceptor (public side); the primary site may be behind NAT and
  dials out.
- `--wan-port`: the base mTLS port. 0 picks a random port in 3001–65535. Leg
  *i* uses base + *i*.
- `--wan-egress-address`: the source address the primary's proxy binds before
  dialing (see "Dedicated networks").

On a running resource:

- `haify resource add-dr <res> --dr-node --dr-endpoint [--wan-port]
  [--egress-address]` attaches a DR copy in place; existing replicas keep their
  synchronous mesh and a promoted resource keeps serving while the DR syncs.
- `haify wan set-endpoint <res>` changes the DR endpoint and/or egress
  address and rebuilds the tunnels. An endpoint that does not answer is refused
  unless `--skip-check`.
- `haify wan repair <res> [--dry-run]` re-provisions the legs a resource
  should have and removes instances orphaned by a renumbered or removed node.

gRPC: `CreateResourceRequest` fields `wan`, `dr_node`, `dr_endpoint`,
`wan_port`, `wan_egress_address`; RPCs `AddDR`, `SetWanEndpoint`,
`RepairWanProxy`, `DRFailback`.

## Data model

`database.Resource` carries `WANMode`, `DRNode`, `DREndpoint`, `WANPort` and
`WANEgressAddress`. Records written before these fields existed deserialize
with them zero-valued, i.e. LAN.

## DRBD config generation

`generateDrbdConfig` (`pkg/controller/resource_drbd_config.go`) takes a
`*wanConfig`; nil renders the LAN config unchanged.

With two endpoints the resource uses protocol A, plus these `net` defaults
(user options still override them):

```
on-congestion pull-ahead; congestion-fill 2M; congestion-extents 500;
ping-timeout 20; csums-alg sha256;
```

Addresses are loopback so each node talks to its local proxy:

```
on <primary> { address 127.0.0.1:<P+9>; }   # binds here, connects to 127.0.0.1:<P> = local dialer
on <dr-node> { address 127.0.0.1:<P>;   }   # local acceptor dials it here
```

With a primary site and a DR, each node's `address` is its LAN replication
address (the DR's is loopback and unused). The primary-site nodes get a
`connection-mesh`. Each primary *i* gets an explicit `connection` to the DR:

```
connection {
    host <primary-i> address 127.0.0.1:<bind port>;
    host <dr>        address 127.0.0.1:<P+i>;
    net { protocol A; on-congestion pull-ahead; congestion-fill 400M; csums-alg sha256; }
}
```

The bind port starts at `P + 100 + i` and is probed on the host for a free
loopback port (`pickWANBindPorts`), because a fixed offset collides with
another resource whose DRBD port sits one offset away.

The generated config uses `quorum majority` so that the initial
force-promote is not blocked. Once the peers are up the controller rewrites
quorum to a majority of the primary site (`applyLocalSiteQuorum`), so the DR
does not vote on whether the primary site may write. `add-dr` applies the same
rule, so attaching a DR does not raise the bar for the local nodes.

## haify-proxy orchestration — `pkg/wanproxy`

The package is called only for WAN resources. It uses the dispatch/SSH deploy
layer through a small `DeploymentClient` interface.

1. PKI: one shared CA and one leaf (SAN `haify-proxy`, server and client EKU),
   generated once, cached on the controller under
   `/var/lib/haify/wanproxy-pki`, valid for ten years, and distributed to every
   WAN node. Each proxy pins its peer to the SAN.
2. Legs: `MultiSpec.Legs()` expands a resource into one `ProxySpec` per
   primary-site node. Leg *i* uses WAN port base + *i* and loopback DRBD port
   P + *i* on both ends. A single-leg resource keeps the per-resource name
   (`haify-proxy@<resource>`); multi-leg resources use
   `haify-proxy@<resource>_<node>`, named after the node's registered name, not
   its address, so renumbering a node does not orphan its tunnel.
3. Config: the dialer (primary) gets `drbd_listen = 127.0.0.1:<P+i>`,
   `peer = <dr-endpoint>:<wan-port>`, optional `bind_addr = <egress>:0`.
   The acceptor (DR) gets `wan_listen = 0.0.0.0:<wan-port>`,
   `drbd_listen = 127.0.0.1:<P+i>`, `acceptor_park_secs = 30`,
   `synthesize_ping_acks = true`. Both get `on_congestion = "pull-ahead"`,
   `zstd_level = 3`, `bulk_cap_bytes = 4 GiB`, metrics every 5 s.
4. Deploy: push the `haify-proxy` binary to `/usr/local/bin/haify-proxy`,
   certs and config to `/etc/haify-proxy/`, and the `haify-proxy@.service`
   template, then enable and start each instance. The controller picks a binary
   per node architecture (`/usr/local/bin/haify-proxy-<goarch>` beside the
   default), and assumes the binary is pre-staged when it has none for that
   architecture.
5. Reachability: after the acceptor starts, the primary probes the DR's WAN
   port over TCP (with retries). The package cannot open cloud security groups;
   the operator must allow the TCP port. UDP is not used.
6. Lifecycle: proxies are provisioned before `drbdadm up`, and on delete they
   are removed after `drbdadm down`. A failed create deprovisions what it
   provisioned.

## HA / DR semantics

- WAN legs are protocol A. The DR can lag by whatever is buffered, so
  failover to the DR is manual, never automatic: auto-promoting an async
  secondary that may be behind risks data loss. The DR node therefore never
  gets a promoter: `ha create`, gateways, `add-replica`/`remove-replica` and
  `resource repair` place promoters on the primary-site replicas only, and
  `resource repair` retires one that an older version gave to a DR node.
  drbd-reactor still provides HA within the primary site.
- `haify resource dr-failover <res> --yes` force-promotes the DR node after
  printing the data-loss warning. Without `--yes` it only prints the warning.
- `haify resource dr-failback <res> [--node] [--wait]` returns to the primary
  site in phases and can be re-run until it reports done: it rejoins the
  primary-site nodes (discarding what they wrote after the failover in favour
  of the DR's copy), waits for the resync from the DR, then makes the primary
  site Primary again.
- One DR node per resource.
- `haify resource status` shows WAN mode, the DR endpoint, each leg's
  `haify-proxy` unit state on both ends, whether the DR's WAN port is reachable,
  and the primary's proxy counters. haify-proxy writes those counters to
  `/run/haify-proxy/<leg>.json` (a file read over SSH, so no extra listening port
  on a WAN-facing host). `buffer_used_bytes` is the amount a DR failover would
  lose; throughput, achieved compression ratio, reconnect count and ring-full
  events are shown alongside. An unreadable snapshot renders as unknown, never
  as zero.
- `haify resource tls` (DRBD kernel TLS) is refused for WAN resources: the
  WAN legs already run mutual TLS in haify-proxy.

## Dedicated networks

- LAN replication network: `haify node register --address <mgmt>
  [--replication-address <repl>]`. SSH uses `--address`; generated `.res`
  files point DRBD at `--replication-address`. Omitted, replication shares the
  management address. Existing resources keep their `.res` until re-rendered.
- WAN egress: `--wan-egress-address` (create) / `--egress-address`
  (`add-dr`, `wan set-endpoint`) renders as `bind_addr = "<ip>:0"` in the dialer
  config, so replication leaves over a chosen uplink and reaches the DR firewall
  from a predictable source IP. Port 0 lets the kernel pick an ephemeral port; a
  fixed one would fail with EADDRINUSE on reconnect during TIME_WAIT. The
  address is persisted on the resource record because the proxy config is
  re-rendered from it.

## Not built

- Per-resource PKI or certificate rotation (the shared CA is the only mode).
- Automatic cross-WAN failover.
- More than one DR node per resource.
