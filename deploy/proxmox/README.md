# SDS storage plugin for Proxmox VE

Back Proxmox VM disks with DRBD resources managed by sds-controller. Guests
get synchronous replication, HA restart on a surviving node, and RAM-only live
migration: the disk is already on every node, so migration copies no data.

This is the Proxmox-side counterpart of the Kubernetes CSI driver in
`deploy/k8s`. Design: `docs/design/proxmox-storage-plugin.md`.

## What it is

A Perl module (`SDSPlugin.pm`, storage type `sds`, with its REST client and
naming helpers under `PVE/Storage/Custom/SDS/`) that translates Proxmox storage
API calls into sds-controller REST calls. It holds no storage logic of its own.
It uses `HTTP::Tiny` + `JSON::PP`, both of which ship with Proxmox VE, so a PVE
node needs no extra packages and no sds binaries.

The plugin is written against storage API version 11. On a PVE release whose
accepted window does not include 11, `api()` reports the nearest version that
release accepts, so the storage still loads.

## Requirements

Each PVE node that will run guests off SDS storage needs:

- **DRBD 9 kernel module + `drbd-utils`.** The hypervisor has to see
  `/dev/drbdN` locally to back a VM disk. Install LINBIT's `drbd-dkms` and
  `drbd-utils`.
- **`sudo`**, and SSH access from the controller: the controller runs
  `drbdadm` and writes `/etc/drbd.d` on this node over SSH through `sudo`. A
  minimal Debian/PVE install may not ship `sudo`.
- **Registration as an sds node** under its PVE node name (`hostname`), e.g.
  `sds node register --name pve1 --address <ip>`. The plugin attaches and
  promotes by node name.
- **Network reach to the controller's REST port** (default 3375).

A PVE node does **not** need to contribute any disks. A compute-only hypervisor
attaches to each volume as a diskless client, which is the normal topology:
storage nodes hold the replicas, PVE nodes run the guests.

`./preflight.sh <controller-host>[:<rest-port>]` checks all of the above except
SSH, and exits non-zero if anything required is missing.

## Install

On every PVE node:

```bash
./preflight.sh 192.168.1.10     # verify prerequisites first
sudo ./install.sh               # compile-checks, copies the modules, restarts pvedaemon + pveproxy
```

`install.sh` puts `SDSPlugin.pm` in `/usr/share/perl5/PVE/Storage/Custom/` and
the helpers in `.../Custom/SDS/`. Restarting `pvedaemon` and `pveproxy` does
not affect running guests. Uninstall with `sudo ./install.sh --uninstall`
after removing the `sds:` entries from `storage.cfg`.

Then add a storage entry once (`/etc/pve/storage.cfg` is cluster-wide):

```
sds: sds0
        controller 192.168.1.10
        sdspool sds_vg0
        replicas 2
        content images
        shared 1
```

`shared 1` is what makes PVE treat the disk as reachable from every node, so
live migration copies only RAM and `ha-manager` may restart a guest anywhere.

### Options

| Option | Meaning |
| ------ | ------- |
| `controller` | Required, cannot be changed after creation. `host` or `host:port`; the REST port defaults to 3375. Plain HTTP |
| `sdspool` | sds pool new volumes are carved from, as `sds pool list` prints it (`sds_vg0`) or without the prefix (`vg0`) |
| `sdsnodes` | Comma-separated sds nodes to place replicas on. Takes precedence over `replicas` |
| `replicas` | Replica count for auto-placement by free space (1-16) |
| `storagetype` | `lvm`, `lvm-thin` or `zfs`. Unset: the controller's default |
| `resourceprefix` | Prefix for generated resource names (default `pve`). Give each PVE cluster its own when several share one sds cluster: VM ids are only unique within a PVE cluster |
| `apitoken` | Bearer token when sds `[auth]`/`[rbac]` is enabled. `storage.cfg` is readable cluster-wide, so use a token scoped to what the plugin needs rather than an admin token |
| `onnoquorum` | What a new disk does when its node loses quorum or every UpToDate copy: `suspend-io` (default; the guest's I/O freezes and carries on when quorum returns) or `io-error` (the guest sees I/O errors and typically remounts read-only). Applies to disks created from then on; change an existing one with `sds resource set-options <resource> --drbd-options on-no-quorum=<value>,on-no-data-accessible=<value>` |

Standard PVE options `nodes`, `disable`, `content`, `shared` and `bwlimit` are
also accepted. `content` may be `images` and `rootdir`; the only format is
`raw`. See `storage.cfg.example` for commented entries.

## How it maps

| Proxmox | sds REST call |
| --- | --- |
| one VM disk `vm-<vmid>-disk-<n>` | one DRBD resource `<prefix>-<vmid>-<n>` |
| `alloc_image` | `POST /v1/resources` (size rounded up to whole GiB, protocol C) |
| `free_image` | `DELETE /v1/resources/<res>` (cascade teardown) |
| `list_images` | `GET /v1/resources`, filtered by `<prefix>-` |
| `activate_volume` | `POST .../diskless-clients` if this node is not in the resource, then a quorum-guarded `POST .../primary` |
| `deactivate_volume` | `POST .../secondary`, then close any dual-primary window, then `DELETE .../diskless-clients/<node>` if this node is only a diskless client |
| `volume_resize` | `PATCH /v1/resources/<res>/volumes/0` (online grow) |
| `volume_snapshot` / rollback / delete | sds snapshot of the backing `<pool>/<lv>` |
| `status` | `GET /v1/pools`; reports the smallest node's copy of `sdspool` |

One disk per resource means each disk resizes, snapshots and deletes
independently, the same model the CSI driver uses for a PVC.

Capacity is the smallest node's because a replica must fit on every node that
holds one; the sum would let PVE accept a disk that cannot be placed. On a thin
pool it is the thin pool's own size and unused data space (`thinSizeBytes`,
`thinDataPercent`), not the volume group's: the group is nearly all thin pool,
so its free space reads ~0 however empty the pool is. A storage set to
`storagetype lvm` allocates thick LVs from the group and reports the group.

Snapshots are taken on **one** diskful node, the current Primary when there is
one, otherwise the first replica. They live on that node's backing volume only
and are not replicated by DRBD.

## Live migration and the dual-primary window

During a live migration the source and target both hold the disk open for a
moment. DRBD forbids two Primaries unless `allow-two-primaries` is set, so the
plugin brackets the hand-off:

- `activate_volume` opens the window **only** when another node currently holds
  Primary **and** PVE is live-migrating the VM from there: the VM's config still
  sits on the source node with `lock: migrate`. Any other Primary is a leftover
  (typically an earlier `deactivate_volume` that could not reach the
  controller), and activation fails naming the node, instead of letting the
  guest run with two writers allowed. Demote the leftover with
  `sds resource secondary <resource> <node>` and start the guest again.
  Containers never qualify: they migrate by restart.
- The window is opened on the migration's source and target only — the ends of
  the one connection that carries two Primaries. A host that is down elsewhere
  in the resource does not block the migration.
- The window is closed again on every exit path: a failed promote, a device that
  does not appear within 20 seconds, and `deactivate_volume` (which closes
  unconditionally, since the source deactivates after hand-off).

The toggle is runtime-only (`drbdadm net-options`) and is never written to the
`.res` file, so a crashed controller cannot strand it: a reboot or
`drbdadm adjust` restores single-primary. To check or repair by hand:

```bash
sds resource dual-primary <resource> off
```

**This is not a way to use one volume from two machines.** An ordinary
filesystem mounted twice will corrupt regardless of what DRBD permits.

## Diskless clients come and go

A host with no replica of a disk attaches to it as a diskless client when the
guest starts there, and detaches when the guest stops or migrates away. Hosts
used to stay attached to every disk of every guest they had ever run, so one of
them being down blocked operations on all of those disks. A detach that fails
is logged and leaves the host attached, as before. A client that is a
resource's last quorum vote besides two replicas is not removed but becomes its
tiebreaker.

## When the controller is unreachable

Starting and stopping a guest go through the controller, and an unreachable
controller used to mean no guest could start and HA could restart none. Now,
when no controller answers and the disk is **already up on this node**:

- `activate_volume` promotes it with plain `drbdadm primary` — never
  `--force`, so DRBD still refuses without quorum and an UpToDate copy within
  reach. It is refused while another node holds the disk Primary: only the
  controller can tell a live migration from a leftover.
- the disk's path resolves locally (`/dev/drbd/by-res/<resource>/0`), so qemu
  can be started.
- `deactivate_volume` demotes with `drbdadm secondary` and clears this node's
  side of any dual-primary window. Run `sds resource dual-primary <resource>
  off` once the controller is back if a migration was under way.

A disk that is not up on the node, a first attach, and a live migration still
need the controller. An error from a controller that answered is never
bypassed.

## HA

`ha-manager` restarting a guest elsewhere calls `activate_volume` on the new
node, which promotes **quorum-guarded**: sds force-promotes only if that node
holds DRBD quorum and refuses otherwise. A partitioned node therefore cannot
take over, so HA failover cannot split-brain. The trade-off is deliberate: a
resource that has lost quorum will not fail over automatically.

## Limitations

- **Raw only.** DRBD exports a raw block device; qcow2 is not supported and not
  needed (snapshots come from sds, not the image format).
- **No linked clones, templates or volume renames.** `clone_image`,
  `create_base` and `rename_volume` refuse; the first two need image-level
  copy-on-write. Reassigning a disk to another VM is therefore not possible.
- **No snapshot access.** A snapshot cannot be activated or addressed by path.
- **Whole-gigabyte allocation.** Disk sizes round *up* to the next GiB.
- **WAN resources are refused for dual-primary**, so a guest cannot live-migrate
  across a WAN-replicated (asynchronous) resource.

## Tests

```bash
cd deploy/proxmox && prove t/
```

The suite stubs the PVE modules and the REST client, so it runs on any machine
with a plain Perl. It covers the naming round trip, size rounding, allocation
payloads, capacity reporting, the REST error semantics (the controller reports
failures as HTTP 200 + `success=false`), and every path that opens or closes
the dual-primary window.
