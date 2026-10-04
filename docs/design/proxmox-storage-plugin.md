# SDS Proxmox VE Storage Plugin — Design

Date: 2026-07-02 (revised 2026-07-28, 2026-10-02)
Status: **Implemented** (`deploy/proxmox/`). Installation and usage:
`deploy/proxmox/README.md`.

## Goal

Let Proxmox VE provision VM disks on SDS/DRBD storage so that guests get
replication, HA restart and RAM-only live migration — the Proxmox-side
counterpart of the Kubernetes CSI driver. Snapshots are a secondary
convenience.

Not supported: linked clones / templates (`create_base`, `clone_image`),
qcow2 on DRBD, renaming volumes, activating a snapshot.

## Decisions

- **The plugin is Perl.** `pvedaemon` loads storage plugins in-process as Perl
  modules subclassing `PVE::Storage::Plugin`; there is no non-Perl plugin API.
- **Thin Perl → sds REST.** The plugin speaks HTTP+JSON to the controller's
  grpc-gateway (default port 3375) with `HTTP::Tiny` and `JSON::PP`, both of
  which ship with PVE. A PVE node needs no sds binaries. The rejected
  alternative, shelling out to `sds`, would need a version-matched Go binary
  on every PVE node and a stable machine-readable CLI output. LINSTOR's plugin
  takes the same REST route.
- **HA reuses the quorum-guarded promote** (`PromoteForNode`), built for the
  CSI hard-failover path.
- **The one new controller capability is `SetDualPrimary`**, for the
  live-migration window.
- **Compute-only PVE nodes are first-class.** `activate_volume` on a node that
  holds no replica attaches it as a diskless client first.
- **The PVE node needs DRBD locally** (DRBD 9 module, `drbd-utils`, `sudo`,
  registration as an sds node under its PVE node name), because it must see
  `/dev/drbdN`. `deploy/proxmox/preflight.sh` checks this before the first
  `alloc_image`.

## Architecture

```
Proxmox node (pvedaemon)                         SDS storage cluster
 ┌─────────────────────────────┐                 ┌───────────────────────────┐
 │ PVE::Storage::Custom::       │  HTTP/REST      │ sds-controller (:3375)    │
 │   SDSPlugin.pm  ──────────── │ ───────────────▶│   grpc-gateway REST       │
 │   SDS/Client.pm, SDS/Naming.pm│  (JSON)         │   └─ DRBD/LVM/reactor     │
 └─────────────────────────────┘                 └───────────────────────────┘
```

The storage is declared `shared 1`, so PVE treats each disk as available on
every node: live migration copies only RAM, and the HA manager can restart a
VM on another node.

## Components (`deploy/proxmox/`)

- `SDSPlugin.pm` — storage type `sds`, `PVE::Storage::Custom::SDSPlugin`.
- `PVE/Storage/Custom/SDS/Client.pm` — REST client; bearer token when set;
  several controller addresses (moves on only when a connection is refused)
  and `https://` with certificate verification.
- `PVE/Storage/Custom/SDS/Naming.pm` — volume ↔ resource naming and size
  conversions.
- `PVE/Storage/Custom/SDS/Capacity.pm` — turns `GET /v1/pools` into the
  storage's total/free.
- `PVE/Storage/Custom/SDS/Migration.pm` — whether another node's Primary is a
  live migration (and so may get the dual-primary window) or a leftover.
- `PVE/Storage/Custom/SDS/Activation.pm` — `activate_volume` and
  `deactivate_volume`: the migration window on two nodes, detaching a diskless
  client on deactivate, and promoting or demoting with local `drbdadm` when no
  controller answers.
- `install.sh`, `preflight.sh`, `storage.cfg.example`, Perl tests in `t/`.

`storage.cfg` options:

| Key | Meaning |
| --- | --- |
| `controller` (fixed) | comma-separated `host`/`host:port`, optionally `https://`; REST port defaults to 3375 |
| `controllerca` | CA bundle for `https://` addresses; default the system store |
| `sdspool` | sds pool for new volumes |
| `sdsnodes` | comma-separated replica nodes; unset = auto-place by free space |
| `replicas` | replica count for auto-placement (ignored with `sdsnodes`) |
| `storagetype` | `lvm`, `lvm-thin` or `zfs` |
| `resourceprefix` | resource name prefix, default `pve`; give each PVE cluster sharing one sds cluster its own |
| `apitoken` | bearer token when sds `[auth]`/`[rbac]` is enabled |
| `onnoquorum` | `suspend-io` (default) or `io-error`, sent as `on-no-quorum` and `on-no-data-accessible` for new disks |

The plugin declares storage API version 11. On a PVE release whose accepted
window does not include 11, `api()` reports the nearest accepted version.

## Volume model

- One PVE disk = one sds DRBD resource.
- `vm-<vmid>-disk-<n>` ↔ `<prefix>-<vmid>-<n>`. VM ids are unique per PVE
  cluster, so the mapping is collision-free and reversible; `list_images`
  needs no side table.
- Format `raw` only.

## PVE method → sds REST

| PVE method | sds REST | notes |
| --- | --- | --- |
| `alloc_image` | `POST /v1/resources` | protocol C; pool, storage type, nodes or replicas from `storage.cfg` |
| `free_image` | `DELETE /v1/resources/{name}` | controller cascades teardown |
| `activate_volume` | `POST …/diskless-clients` (only if this node is not a participant), then `POST …/primary` with `quorumGuarded` | waits for `/dev/drbdN` to appear |
| `deactivate_volume` | `POST …/secondary`, then dual-primary off, then `DELETE …/diskless-clients/{node}` when this node is only a client | without a controller: `drbdadm secondary` |
| `path` | `GET /v1/resources/{name}` | `/dev/drbdN`; without a controller, the local by-res link of a volume up here |
| `volume_resize` | `PATCH /v1/resources/{name}/volumes/0` | |
| `list_images` | `GET /v1/resources` | filtered by naming |
| `status` | `GET /v1/pools` | the smallest node's total/free for `sdspool`, since a replica must fit on every node; for a thin pool, the thin pool's own size and data usage rather than the VG's |
| `volume_snapshot` / `_rollback` / `_delete` | `POST /v1/volumes/{pool/lv}/snapshots`, `…/{snap}/restore`, `DELETE …/{snap}?node=` | run on a diskful node, preferring the Primary |
| `activate_storage` / `check_connection` | `GET /v1/resources` | fail fast on an unreachable controller |

Snapshots run on a diskful node because the backing LV exists only where a
replica is; the PVE host usually holds none. `DELETE` carries no body, so the
snapshot node travels as a query parameter.

### Live migration

- `activate_volume` on the migration target sees another node still Primary,
  checks that PVE is live-migrating the VM from there (the VM config in
  pmxcfs is still on another node and carries `lock: migrate`), opens the
  dual-primary window on the source and target only
  (`POST …/dual-primary {enable: true, nodes: [source, target]}`), then promotes.
  If the promote fails or the device does not appear, it closes the window
  before failing. A Primary elsewhere with no migration under way is a
  leftover from a failed deactivate; activation refuses it rather than run the
  guest with two writers allowed (`SDS/Migration.pm`).
- `deactivate_volume` on the source demotes and then always disables
  dual-primary, whether or not a window was opened.
- Offline migration and HA restart never open the window: only one node
  activates.

### HA

PVE `ha-manager` restarts the VM on a surviving node; `activate_volume` there
promotes with `quorumGuarded`. The controller force-promotes only when that
node holds DRBD quorum and refuses otherwise, so an HA restart after a hard
node failure cannot split-brain. A resource without quorum does not fail over.

## `SetDualPrimary` (`pkg/controller/dualprimary.go`)

`drbdadm net-options --allow-two-primaries={yes|no} <res>` on the resource's
nodes. It is a runtime-only toggle, never written to the `.res` file, so a
reboot or `drbdadm adjust` returns the resource to single-primary even if the
"off" call is lost.

- **enable** refuses WAN resources (protocol A; two Primaries over an async
  link corrupts data) and fails if any node rejects the command. `nodes`
  limits it to named participants: a migration names its source and target,
  the ends of the only connection that carries two Primaries, so a host that
  is down elsewhere in the resource does not block it.
- **disable** is idempotent: it tolerates a missing resource and per-node
  command failures, then verifies with `drbdsetup show` and returns an error if
  any node still has `allow-two-primaries`.

Exposed over gRPC, REST and the MCP tool `sds_resource_dual_primary`;
`sds resource dual-primary` toggles it by hand.

## Validation

On a single PVE 8.4.11 host against a three-node sds cluster: `alloc_image` (2-node
auto-placed resource), `list_images`, `volume_resize`, `volume_snapshot`,
`volume_snapshot_delete`, `path`, `free_image`, `status`.

On a two-node PVE 9.2.5 cluster (`pve1`/`pve2`, nested VMs, both diskless sds
nodes):

- `activate_volume` / `deactivate_volume` on compute-only nodes; the guest boots
  from `/dev/drbdN`.
- Live migration in both directions, 34 ms and 22 ms downtime, no disk copy.
  Afterwards `drbdsetup show` on both nodes showed no `allow-two-primaries`.
- Hard-stopping `pve1`: the guest started on `pve2` from the same volume.

API version window: PVE 8.4 accepts [9,11], 9.1 [9,13], 9.2.5 [9,15]; the
declared 11 is inside all three. Signature changes since 11 append parameters,
which Perl ignores.

A two-node PVE cluster loses quorum when one node dies, so HA waits in
`wait_for_quorum` instead of taking over. A QDevice (`corosync-qnetd` on a
third machine, `pvecm qdevice setup <ip> -f`) supplies the third vote.

Defects found by this validation and fixed:

1. Snapshots were sent to the hypervisor node, which holds no LV; they now go
   to a diskful node.
2. Snapshot delete passed no node, so the controller failed with
   `failed to delete snapshot: []`; the node is now a query parameter.
3. A resize issued during the initial resync left the LVs grown and DRBD not,
   and every retry then failed at `lvresize` (non-zero exit when already at the
   target size). `ResizeVolume` now checks the actual LV size before treating
   that as a failure, and surfaces the DRBD error.

## Not built

Whole-VM migration off VMware is not part of this plugin. PVE 8.2+ ships an
ESXi import wizard that copies disks into any target storage, including `sds`;
SDS does not reimplement it.
