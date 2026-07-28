# SDS Proxmox VE Storage Plugin — Design

Date: 2026-07-02 (revised 2026-07-28)
Status: **Active — implementing.** Phase 1 of the "receive VMware refugees" track;
phase 2 (whole-VM migration off VMware) builds on this and is scoped separately
at the end of this document.

## Status & conclusions

- **The plugin interface MUST be Perl.** Proxmox VE's `pvedaemon` loads storage
  plugins in-process as Perl modules subclassing `PVE::Storage::Plugin`; there is
  no official non-Perl plugin API. Every PVE storage plugin (LINSTOR, Ceph/rbd,
  ZFS-over-iSCSI, …) is a Perl module. Perl cannot be avoided entirely.
- **DECIDED (2026-07-28): thin Perl → sds REST directly.** The plugin speaks
  HTTP+JSON to the controller's grpc-gateway on `:3375` using `HTTP::Tiny` +
  `JSON::PP` — both ship with PVE, so a PVE node gains **zero new dependencies**
  and installing the plugin means copying one `.pm`. The rejected alternative
  (a ~100-line Perl shim shelling out to `sds-cli`) would require installing and
  version-matching a Go binary on every PVE node *and* promoting `sds-cli`'s
  human-readable output into a stable `--json` contract. LINSTOR's official
  plugin takes the same REST route.
- **HA needs no new sds work** — it reuses the quorum-guarded `PromoteForNode`
  built for the CSI hard-failover feature.
- **The only new sds capability required is `SetDualPrimary`** (allow-two-primaries
  toggle) for the live-migration window.
- **REVISED (2026-07-28): activate-on-any-node now works in v1.** The original
  design listed "migration to a non-replica node" as a later enhancement. Since
  then `AttachDisklessClient` shipped, so `activate_volume` on a node that holds
  no replica attaches it as a diskless client first. A PVE host that only runs
  VMs (contributes no disks) is therefore a first-class citizen — which is
  exactly how the validation host below is set up.
- **RESOLVED (2026-07-28): validation environment exists.** `dell`
  (192.168.123.98) runs real Proxmox VE 8.4.11 (kernel 6.11.11-2-pve, Debian 12)
  and the `orange1/2/3` sds cluster is reachable. Validation is
  PVE-host-plus-external-storage-cluster (NOT hyperconverged): `dell` joins sds
  as a diskless node. This settles the open environment question from 2026-07-02.
- **NEW PREREQUISITE (2026-07-28): the PVE node needs DRBD locally.** To back a
  VM disk the PVE host must itself see `/dev/drbdN`, so it needs the DRBD 9
  kernel module (LINBIT `drbd-dkms` on Debian 12) + `drbd-utils`, and must be
  registered as an sds node. Unchecked, this surfaces as an inscrutable failure
  on the first `alloc_image`, so it gets an explicit preflight script.

## Goal

Let Proxmox VE provision VM/CT disks on SDS/DRBD storage so that guests get
**automatic replication, HA, and fast (memory-only) live migration** — the
Proxmox-side analog of the existing Kubernetes CSI driver. Primary value:
**HA + fast migration**; snapshots are a secondary convenience.

Non-goals (first version): linked clones / templates (`create_base` /
`clone_image`), qcow2-on-DRBD, GUI wizardry beyond a `storage.cfg` entry.

## Architecture

```
Proxmox node (pvedaemon)                         SDS storage cluster
 ┌─────────────────────────────┐                 ┌───────────────────────────┐
 │ PVE::Storage::Custom::       │  HTTP/REST      │ sds-controller (VIP:3375) │
 │   SDSPlugin.pm  ──────────── │ ───────────────▶│   grpc-gateway REST       │
 │   └─ thin Perl REST client   │  (JSON)         │   └─ DRBD/LVM/reactor      │
 └─────────────────────────────┘                 └───────────────────────────┘
        one plugin per PVE node                    provisions DRBD resources
                                                    on the storage nodes
```

A Perl plugin `PVE::Storage::Custom::SDSPlugin` runs on each Proxmox node and
translates PVE storage-API calls into sds-controller REST calls. VM disks are
DRBD-replicated block devices; the storage is declared `shared 1` so PVE treats
the disk as available cluster-wide → live migration copies only RAM, and the HA
manager can restart a VM on any replica node.

This mirrors LINSTOR's `libpve-storage-linstor` (a Perl REST-client plugin).
We reuse the sds backend wholesale; the plugin adds no storage logic of its own.

## Components

1. **`SDSPlugin.pm`** — implements the PVE storage API (`PVE::Storage::Custom`).
2. **Thin Perl REST client** — `HTTP::Tiny`/`LWP` + `JSON`, wrapping the sds
   endpoints the plugin needs. No gRPC codegen in Perl.
3. **`storage.cfg` type `sds`** — config keys: `controller` (VIP:3375),
   `pool` (default sds pool), `nodes`/`replicas` (replica placement), optional
   `apitoken` (for when sds `[auth]`/`[rbac]` is enabled).
4. **New sds capability: `SetDualPrimary`** — a REST/gRPC endpoint toggling
   `allow-two-primaries` on a resource, used only for the live-migration window.
5. **Install artifacts** — the `.pm`, an install script (copy to
   `/usr/share/perl5/PVE/Storage/Custom/`, restart `pvedaemon`/`pveproxy`), a
   **preflight check** (DRBD 9 module loadable, `drbdadm` present, this node
   registered with the controller, controller REST reachable), and a
   `storage.cfg` example. Debian packaging (`libpve-storage-sdsplugin`) is a
   later nicety, not required for the first version.

All of the above live in `deploy/proxmox/`, mirroring `deploy/k8s/` for CSI.

## Volume model

- **1 PVE disk = 1 sds DRBD resource** (independent lifecycle — resize, snapshot,
  delete are per-disk), matching the CSI "one resource per PVC" model.
- PVE volume id `vm-<vmid>-disk-<n>` ↔ a sanitized sds resource name (DRBD
  resource names are constrained; the plugin sanitizes and keeps a reversible
  mapping, e.g. `pve-<vmid>-<n>` with the original recorded in resource metadata
  / reconstructable from `list_images`).
- **Format: `raw` only** — DRBD exports a raw block device; qcow2-on-DRBD is not
  supported (and not needed — snapshots come from sds, not the image format).

## Data flow (PVE storage method → sds REST)

| PVE method | sds call | notes |
| --- | --- | --- |
| `alloc_image` | `CreateResource` (N replicas) | size, pool, replicas from `storage.cfg`; returns volid |
| `free_image` | `DeleteResource` | reuses the cascade teardown (gateway/HA/device/LV) |
| `activate_volume` | `AttachDisklessClient` (only if this node holds no replica) then `PromoteForNode` (quorum-guarded) | makes `/dev/drbdN` available on this node, replica or not |
| `deactivate_volume` | `SetSecondary` | after use / migration source |
| `path` | (local) | returns `/dev/drbdN` for the volume |
| `volume_resize` | `ResizeVolume` | online grow |
| `list_images` | `ListResources` | filtered by this storage's naming |
| `status` | `ListPools` | total/free capacity |
| `volume_snapshot` / `_rollback` / `_delete` | `CreateSnapshot` / `RestoreSnapshot` / `DeleteSnapshot` | LVM/ZFS-backed |

### Live migration (the core value)

- The storage is `shared 1`, so PVE migrates **RAM only** — the disk is already
  replicated to the target node by DRBD.
- **Dual-primary window**: during the live hand-off, source and target both need
  the volume active (Primary) briefly. DRBD forbids two Primaries by default, so
  the plugin brackets the migration with `SetDualPrimary(on)` before and
  `SetDualPrimary(off)` after cutover. The "off" call MUST run in a
  `finally`-style guard so a resource is never left in dual-primary.
- Implemented via the plugin's `activate_volume`/`volume_has_feature('copy'...)`
  hooks; offline migration and HA-restart do NOT open dual-primary (only one
  node activates).

### HA

- PVE `ha-manager` restarts the VM on a surviving node → the plugin's
  `activate_volume` there → **reuses the quorum-guarded `PromoteForNode`**: a
  survivor that holds DRBD quorum force-promotes safely; a node without quorum
  refuses (fail-closed, no split-brain). No new sds work for HA beyond what the
  CSI hard-failover feature already provides.

## New sds work

Only one addition: **`SetDualPrimary(resource, enable)`** — proto + server +
REST route + client + MCP tool. Implementation:
`drbdadm net-options --allow-two-primaries={yes|no} <res>` on the resource's
nodes, with `AllSuccess()` checks. Everything else (create/delete/resize/
promote/secondary/snapshot/diskless-attach) already exists in the REST surface.

Two safety constraints on this endpoint:

- **WAN resources are refused outright.** WAN mode is protocol A (async); two
  Primaries over an async link is a data-corruption class of mistake, not a
  performance trade-off. The guard is in the endpoint, not in the caller, so no
  future caller can reintroduce it.
- **`enable=false` is idempotent.** It runs in the plugin's `finally`-style
  guard and must succeed against a resource that is already single-primary, was
  never dual-primaried, or is mid-teardown — otherwise a failed migration leaves
  the resource stranded in dual-primary, which is the exact state this whole
  mechanism exists to bound.

## Error handling

- REST errors → the plugin `die`s with the controller's message; PVE surfaces it
  in the task log.
- `SetDualPrimary(off)` is always attempted after migration, even on failure, so
  a resource is never stranded in dual-primary.
- The quorum guard refuses unsafe promotes, so HA never splits brain; a
  quorum-less resource simply won't auto-fail-over (the documented safe trade-off
  — true 2-node-no-tiebreaker fencing is out of scope).

## Testing

- **Perl unit tests**: exercise each plugin method against a mocked REST client
  (assert the right sds endpoint + payload; assert dual-primary is always closed).
- **Go unit tests**: the new `SetDualPrimary` endpoint (enable/disable issues the
  correct `net-options` command to all nodes; `AllSuccess` failure surfaces).
- **Live test (environment settled 2026-07-28)**: run against `dell` (real PVE
  8.4.11) with the `orange1/2/3` sds cluster as external storage. `dell` joins
  sds as a node that contributes no disks and attaches volumes diskless, which
  the `AttachDisklessClient` path now makes a supported topology rather than a
  workaround. Round trip to validate: register node → preflight → install plugin
  → `alloc_image` → boot a guest off the volume → live-migrate → `volume_resize`
  → snapshot/rollback → `free_image`.
- A single-PVE-node setup validates everything except live migration between two
  PVE hosts (which needs a second PVE node joined to the same PVE cluster). If
  only `dell` is available, live migration is the one item that stays unverified
  — and it will be reported as unverified rather than assumed.

## Known nuances (noted, not blocking)

- ~~**Migration to a non-replica node**~~ — **resolved 2026-07-28**: handled in
  v1 via on-demand `AttachDisklessClient` in `activate_volume` (see above).
- **Resource-name sanitization** must be reversible/collision-free across the
  cluster (VM ids are unique, so `pve-<vmid>-<n>` is safe).
- **Auth**: when sds enables `[auth]`/`[rbac]`, the plugin sends a bearer token
  from `storage.cfg`.

## Rough build order

1. sds `SetDualPrimary` endpoint (proto/server/REST/client/MCP + Go tests).
2. Perl REST client + `SDSPlugin.pm` core (alloc/free/activate/deactivate/
   path/resize/list/status) + Perl unit tests.
3. Snapshots in the plugin.
4. Live-migration dual-primary bracketing + HA activate path.
5. Install script + preflight + `storage.cfg` example + docs.
6. Live validation per the environment decision above.

## Phase 2 — whole-VM migration off VMware (scoped, not yet designed)

The driving goal behind this plugin is letting VMware users leave: guests end up
running on Proxmox with their disks on sds.

**Deliberately not self-built:** PVE 8.2+ ships an ESXi import wizard that pulls
a VM from vSphere and `qemu-img convert`s its disks into a chosen target storage.
Once `sds` is a valid target storage, that path exists for free. Rebuilding a
vSphere client inside sds would duplicate work Proxmox already did.

**Therefore phase 2 starts with measurement, not code:** run the PVE import
wizard against a real vSphere VM with `sds` as the target and record what
actually breaks or annoys. Expected gaps, to be confirmed rather than assumed:

- choosing replica count / pool at import time (the wizard only knows "a storage")
- progress + resumability for large disks (an import that dies at 400 GB should
  not restart from zero)
- post-import sanity: is the resource sized/named/replicated as intended

Designing these before the measurement would be guesswork. This section gets
rewritten with real findings once phase 1 is validated.
