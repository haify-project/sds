# SDS storage plugin for Proxmox VE

Back Proxmox VM/CT disks with DRBD resources managed by sds-controller. Guests
get synchronous replication, HA restart on a surviving node, and RAM-only live
migration — the disk is already on every node, so migration copies no data.

This is the Proxmox-side counterpart of the Kubernetes CSI driver in
`deploy/k8s`. Design: `docs/superpowers/specs/2026-07-02-proxmox-storage-plugin-design.md`.

## What it is

A single Perl module (`SDSPlugin.pm`) that translates Proxmox storage API calls
into sds-controller REST calls. It holds no storage logic of its own. It uses
`HTTP::Tiny` + `JSON::PP`, both of which ship with Proxmox VE, so a PVE node
gains no new packages and needs no sds binaries.

## Requirements

Each PVE node that will run guests off SDS storage needs:

- **DRBD 9 kernel module + `drbd-utils`.** The hypervisor has to see
  `/dev/drbdN` locally to back a VM disk. On Debian 12 (PVE 8.x) install
  LINBIT's `drbd-dkms` and `drbd-utils`.
- **Registration as an sds node**, under the same name as its PVE node name
  (`hostname`) — the plugin promotes and attaches by node name.
- **Network reach to the controller's REST port** (default 3375).

A PVE node does **not** need to contribute any disks. A compute-only hypervisor
attaches to each volume as a diskless client, which is the normal topology:
storage nodes hold the replicas, PVE nodes run the guests.

`./preflight.sh <controller-host>` checks all of the above.

## Install

On every PVE node:

```bash
./preflight.sh 192.168.1.10     # verify prerequisites first
sudo ./install.sh               # copies the .pm, restarts pvedaemon + pveproxy
```

Then add a storage entry once (storage.cfg is cluster-wide):

```
sds: sds0
        controller 192.168.1.10
        sdspool vg0
        replicas 2
        content images,rootdir
        shared 1
```

See `storage.cfg.example` for every option. Uninstall with
`sudo ./install.sh --uninstall`.

## How it maps

| Proxmox | sds |
| --- | --- |
| one VM disk `vm-<vmid>-disk-<n>` | one DRBD resource `<prefix>-<vmid>-<n>` |
| `alloc_image` | `CreateResource` |
| `free_image` | `DeleteResource` (cascade teardown) |
| `activate_volume` | `AttachDisklessClient` if needed, then a quorum-guarded promote |
| `deactivate_volume` | demote, then close any dual-primary window |
| `volume_resize` | `ResizeVolume` (online grow) |
| `volume_snapshot` / rollback / delete | sds snapshots on the backing `<pool>/<lv>` |
| `status` | `ListPools` |

One disk per resource means each disk resizes, snapshots and deletes
independently — the same model the CSI driver uses for a PVC.

## Live migration and the dual-primary window

During a live migration the source and target both hold the disk open for a
moment. DRBD forbids two Primaries unless `allow-two-primaries` is set, so the
plugin brackets the hand-off:

- `activate_volume` opens the window **only** when another node currently holds
  Primary — that is precisely a migration, and nothing else.
- The window is closed again on every exit path: a failed promote, a device that
  never appears, and `deactivate_volume` (which closes unconditionally, since
  the source deactivates after hand-off).

The toggle is runtime-only (`drbdadm net-options`) and is never written to the
`.res` file, so even a crashed controller cannot strand it: a reboot or
`drbdadm adjust` restores single-primary. To check or repair by hand:

```bash
sds-cli resource dual-primary <resource> off
```

**This is not a way to use one volume from two machines.** An ordinary
filesystem mounted twice will corrupt regardless of what DRBD permits.

## HA

`ha-manager` restarting a guest elsewhere calls `activate_volume` on the new
node, which promotes **quorum-guarded**: sds force-promotes only if that node
holds DRBD quorum and refuses otherwise. A partitioned node therefore cannot
take over, so HA failover cannot split-brain. The trade-off is deliberate: a
resource that has lost quorum will not auto-fail-over.

## Limitations

- **Raw only.** DRBD exports a raw block device; qcow2-on-DRBD is unsupported
  and unnecessary (snapshots come from sds, not the image format).
- **No linked clones or templates** — those need image-level copy-on-write.
- **Whole-gigabyte allocation.** Disk sizes round *up* to the next GB.
- **WAN resources are refused for dual-primary**, so a guest cannot live-migrate
  across a WAN-replicated (asynchronous) resource.

## Tests

```bash
prove t/
```

The suite stubs the PVE modules and the REST client, so it runs on any machine
with a plain Perl — no Proxmox needed. It covers the naming round trip, size
rounding, allocation payloads, capacity reporting, the REST error semantics
(the controller reports failures as HTTP 200 + `success=false`), and every path
that opens or closes the dual-primary window.
