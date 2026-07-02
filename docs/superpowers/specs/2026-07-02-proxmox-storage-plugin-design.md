# SDS Proxmox VE Storage Plugin — Design

Date: 2026-07-02
Status: **Parked** — design captured, implementation deferred (not scheduled).

## Status & conclusions (2026-07-02)

Design brainstormed and captured; **implementation intentionally deferred**.
Key conclusions to carry forward when this is picked up:

- **The plugin interface MUST be Perl.** Proxmox VE's `pvedaemon` loads storage
  plugins in-process as Perl modules subclassing `PVE::Storage::Plugin`; there is
  no official non-Perl plugin API. Every PVE storage plugin (LINSTOR, Ceph/rbd,
  ZFS-over-iSCSI, …) is a Perl module. Perl cannot be avoided entirely.
- **Perl can be kept thin**, and how thin depends on the integration choice
  (this is the one open decision — pick at implementation time):
  - **(a) Thin Perl → sds REST directly** (a few hundred lines; Perl does the
    HTTP+JSON itself). Only the `.pm` is installed per PVE node. *Tentatively
    preferred during brainstorming.*
  - **(b) Minimal Perl → shell out to `sds-cli`** (~100-line shim; nearly all
    logic stays in Go/sds-cli). Requires `sds-cli` on every PVE node **and** a
    stable `sds-cli --json` output. Choose this if minimizing Perl maintenance
    is the priority.
- **HA needs no new sds work** — it reuses the quorum-guarded `PromoteForNode`
  built for the CSI hard-failover feature.
- **The only new sds capability required is `SetDualPrimary`** (allow-two-primaries
  toggle) for the live-migration window.
- **Live VM test needs sds running on the PVE hosts** (hyperconverged). The
  current `orange1/2/3` are guest VMs on a PVE host, not PVE hosts, so a full
  VM live-migration/HA test needs a separate PVE+sds environment (open item).

The rest of this document is the full design as brainstormed, retained for when
implementation resumes.

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
   `/usr/share/perl5/PVE/Storage/Custom/`, restart `pvedaemon`/`pveproxy`), and
   a `storage.cfg` example. Debian packaging (`libpve-storage-sdsplugin`) is a
   later nicety, not required for the first version.

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
| `activate_volume` | `PromoteForNode` (quorum-guarded) | makes `/dev/drbdN` available on this node |
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
REST route + client. Implementation: `drbdadm net-options --allow-two-primaries={yes|no} <res>`
(or an equivalent adjust) on the resource's nodes, with `AllSuccess()` checks.
Everything else (create/delete/resize/promote/secondary/snapshot) already exists
in the REST surface.

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
- **Live test (environment caveat)**: a full VM live-migration + HA test needs a
  Proxmox environment where **sds runs on the PVE nodes** (hyperconverged:
  PVE host == storage node). The current `orange1/2/3` are guest VMs *on* a PVE
  host, not PVE hosts themselves, so the full VM test requires either installing
  PVE on the orange nodes (heavy) or a separate PVE+sds cluster. Until then we
  validate the plugin's sds-facing calls against the real controller
  (create/activate/resize/snapshot/dual-primary/delete round-trips) without an
  actual guest VM. **This environment decision is deferred to the user.**

## Known nuances (noted, not blocking)

- **Migration to a non-replica node**: the target must have the volume (diskful
  or diskless). First version creates the resource on the configured node set so
  any of them can activate; on-demand diskless-attach on an arbitrary target
  (LINSTOR-style auto-place) is a later enhancement.
- **Resource-name sanitization** must be reversible/collision-free across the
  cluster (VM ids are unique, so `pve-<vmid>-<n>` is safe).
- **Auth**: when sds enables `[auth]`/`[rbac]`, the plugin sends a bearer token
  from `storage.cfg`.

## Rough build order

1. sds `SetDualPrimary` endpoint (proto/server/REST/client + Go tests).
2. Perl REST client + `SDSPlugin.pm` core (alloc/free/activate/deactivate/
   path/resize/list/status) + Perl unit tests.
3. Snapshots in the plugin.
4. Live-migration dual-primary bracketing + HA activate path.
5. Install script + `storage.cfg` example + docs.
6. Live validation per the environment decision above.
