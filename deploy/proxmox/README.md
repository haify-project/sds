# SDS storage plugin for Proxmox VE

Back Proxmox VM disks with DRBD resources managed by sds-controller. Guests
get synchronous replication, HA restart on a surviving node, and RAM-only live
migration: the disk is already on every node, so migration copies no data.

This is the Proxmox-side counterpart of the Kubernetes CSI driver in
`deploy/k8s`. Design: `docs/design/proxmox-storage-plugin.md`. Moving VMs off
VMware vSAN onto this storage:
[Evacuating VMware vSAN](../../docs/vsan-evacuation.md).

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

- **An LVM filter that skips DRBD devices.** A guest that uses LVM inside its
  disk writes a PV header to it, and this host sees that disk as `/dev/drbdN`
  (and, for an encrypted resource, as the `/dev/mapper/sds_*` container under
  it). Unfiltered, the host's LVM finds the guest's volume group and may
  activate it, which holds the device open: the VM can then neither migrate nor
  fail over. The package (or `install.sh`) prepends
  `"r|^/dev/drbd|", "r|^/dev/mapper/sds_|"` to `global_filter` in
  `/etc/lvm/lvm.conf`, keeping PVE's own entries (`lvm-filter.sh`;
  `SDS_SKIP_LVM_FILTER=1` skips it). It also needs `devices/scan_lvs = 0`, the
  default, so the backing LVs are not scanned.

`preflight.sh <controller>[,<controller>...]` (the `controller` value from storage.cfg; `SDS_CA=<file>` for a private CA) checks all of the above except
SSH, and exits non-zero if anything required is missing (a missing LVM filter
is a warning: installing adds it). It is in this directory and, once the
package is installed, in `/usr/share/sds-pve-plugin/`.

## Install

### From the package (preferred)

Build the package once, on any Debian/Ubuntu machine with a checkout (it needs
only `dpkg-deb` and Perl, not Go or Node.js):

```bash
make deb-pve-plugin        # dist/sds-pve-plugin_<version>_all.deb
```

`make deb` builds it together with the controller packages. Then, on every PVE
node:

```bash
./preflight.sh 192.168.1.10                  # from the checkout: verify prerequisites first
apt install ./sds-pve-plugin_*_all.deb       # the ./ makes apt install a local file
```

The package replaces `install.sh` and installs exactly the same modules:
`SDSPlugin.pm` in `/usr/share/perl5/PVE/Storage/Custom/` and the helpers in
`.../Custom/SDS/`, plus `lvm-filter.sh` and `preflight.sh` in
`/usr/share/sds-pve-plugin/`. It depends on `libpve-storage-perl`,
`drbd-utils` and `lvm2`; the DRBD 9 kernel module (`drbd-dkms`) is still
yours to install. On installation and on every upgrade it:

1. checks that the module compiles (`perl -c`). If it does not, it stops
   there: `pvedaemon` and `pveproxy` are not restarted and keep running the
   previous plugin, and apt reports the package as not configured;
2. adds the LVM filter with `lvm-filter.sh`, unless `SDS_SKIP_LVM_FILTER=1`
   (`SDS_SKIP_LVM_FILTER=1 apt install ./sds-pve-plugin_*_all.deb`). A
   failure there is a warning, not an error;
3. runs `systemctl try-restart pvedaemon pveproxy`, which does not affect
   running guests.

`apt remove sds-pve-plugin` removes the modules and restarts the two daemons.
It changes nothing else: it warns if `storage.cfg` still has `sds:` entries,
but leaves them (other nodes may still use them), and leaves the LVM filter in
`lvm.conf`.

A node set up with `install.sh` can move to the package directly: dpkg
replaces the files in place. Do not run `install.sh --uninstall` afterwards:
it deletes files the package now owns.

### With install.sh

Without the package, on every PVE node:

```bash
./preflight.sh 192.168.1.10     # verify prerequisites first
sudo ./install.sh               # compile-checks, copies the modules, adds the LVM filter, restarts pvedaemon + pveproxy
```

`install.sh` does what the package does, from the checkout and without dpkg
knowing about the files. Uninstall with `sudo ./install.sh --uninstall` after
removing the `sds:` entries from `storage.cfg`.

### Storage entry

With the plugin on every node, add a storage entry once (`/etc/pve/storage.cfg`
is cluster-wide):

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
| `controller` | Required. Comma-separated addresses, each `host`, `host:port` or `[v6]:port` (port defaults to 3375), optionally prefixed with `https://`. Under Self-HA list every node that can run the controller: an address that refuses the connection is skipped and the next tried; a request that reached a controller and then failed is never resent elsewhere. `pvesm set` cannot change it (it is a fixed option); edit `/etc/pve/storage.cfg` to add addresses |
| `controllerca` | PEM CA bundle that signs the controller's certificate, for `https://` addresses, e.g. kept in `/etc/pve` so every node has it. Unset: the system trust store. The certificate is verified, names included, so it must cover the addresses listed. `https://` needs `[tls] rest = true` on the controller |
| `sdspool` | sds pool new volumes are carved from, as `sds pool list` prints it (`sds_vg0`) or without the prefix (`vg0`) |
| `sdsnodes` | Comma-separated sds nodes to place replicas on. Takes precedence over `replicas` |
| `replicas` | Replica count for auto-placement by free space (1-16) |
| `storagetype` | `lvm`, `lvm-thin` or `zfs`. Unset: the controller's default |
| `resourceprefix` | Prefix for generated resource names (default `pve`). Give each PVE cluster its own when several share one sds cluster: VM ids are only unique within a PVE cluster |
| `apitoken` | Bearer token when sds `[auth]`/`[rbac]` is enabled. `storage.cfg` is readable cluster-wide, so use a token scoped to what the plugin needs rather than an admin token |
| `onnoquorum` | What a new disk does when its node loses quorum or every UpToDate copy: `suspend-io` (default; the guest's I/O freezes and carries on when quorum returns) or `io-error` (the guest sees I/O errors and typically remounts read-only). Applies to disks created from then on; change an existing one with `sds resource set-options <resource> --drbd-options on-no-quorum=<value>,on-no-data-accessible=<value>` |
| `exactsize` | `1` gives each new or resized disk exactly the size PVE asks for, rounded up to a 512-byte sector, instead of the next whole GiB (the backing volume is still allocated in GiB; the DRBD device is capped at the exact size). Needed for online Move Disk onto this storage, which refuses a target that is not byte-for-byte the source's size. Default `0` |

Standard PVE options `nodes`, `disable`, `content`, `shared` and `bwlimit` are
also accepted. `content` may be `images` and `rootdir`; the only format is
`raw`. See `storage.cfg.example` for commented entries.

## How it maps

| Proxmox | sds REST call |
| --- | --- |
| one VM disk `vm-<vmid>-disk-<n>` | one DRBD resource `<prefix>-<vmid>-<n>` |
| another volume of the VM: `vm-<vmid>-cloudinit`, `vm-<vmid>-state-<snap>` (a snapshot's RAM), `vm-<vmid>-fleece-<n>` (backup fleecing) | one DRBD resource `<prefix>-<vmid>-<name>` |
| `alloc_image` | `POST /v1/resources` (size rounded up to whole GiB, or exact with `exactsize`; protocol C) |
| `free_image` | `DELETE /v1/resources/<res>` (cascade teardown) |
| `list_images` | `GET /v1/resources`, filtered by `<prefix>-` |
| `activate_volume` | `POST .../diskless-clients` if this node is not in the resource, then a quorum-guarded `POST .../primary` |
| `deactivate_volume` | `POST .../secondary`, then close any dual-primary window, then `DELETE .../diskless-clients/<node>` if this node is only a diskless client |
| `volume_resize` | `PATCH /v1/resources/<res>/volumes/0` (online grow) |
| `volume_snapshot` / rollback / delete | `POST/DELETE /v1/resources/<res>/snapshots[/<name>[/rollback]]`: a snapshot on every replica |
| a template's disk `base-<vmid>-disk-<n>` | DRBD resource `<prefix>-base-<vmid>-<n>` |
| `create_base` / `rename_volume` | `POST /v1/resources/<res>/rename` |
| `clone_image` | a new disk on the template's nodes, filled with `POST .../populate` |
| `status` | `GET /v1/pools`; reports the smallest node's copy of `sdspool` |

One disk per resource means each disk resizes, snapshots and deletes
independently, the same model the CSI driver uses for a PVC.

Capacity is the smallest node's because a replica must fit on every node that
holds one; the sum would let PVE accept a disk that cannot be placed. On a thin
pool it is the thin pool's own size and unused data space (`thinSizeBytes`,
`thinDataPercent`), not the volume group's: the group is nearly all thin pool,
so its free space reads ~0 however empty the pool is. A storage set to
`storagetype lvm` allocates thick LVs from the group and reports the group.

Snapshots are taken on **every** diskful replica at once, with I/O suspended
across them for the moment it takes (a timer on each node resumes I/O after a
minute whatever happens to the controller). Each copy carries its replica's
DRBD metadata too, so rolling back restores every replica together and DRBD
resyncs nothing. Losing a node loses one copy, not the snapshot. A snapshot
taken by an earlier version of the plugin lives on one replica only and is
rolled back the old way: that replica is restored and the others resync the
whole disk from it.

A snapshot can be opened read-only on a node that holds a replica, which is
what `vzdump` in snapshot mode needs for a container. On a node without one
the backup fails, naming the replica nodes to run it on.

**Templates and clones.** Converting a VM to a template renames its disks to
`base-<vmid>-disk-<n>`; the data stays where it is. A linked clone of a
template is a full copy — a DRBD device has no image-level copy-on-write — made
on the template's replica nodes at the template's exact size, so it is
independent of the template. **Reassigning** a disk to another VM renames it.
Both renames need the disk stopped and without snapshots; the error says what
is in the way.

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

PVE decides where a guest runs, and with it where its disks are Primary. So
`sds ha create` and the sds gateways, which would put a drbd-reactor promoter
on the disk to fight PVE for the role, refuse a disk the plugin created (it
labels each `sds.pve/managed-by=pve`) and any resource with diskless clients.
Use PVE HA for guests; do not use drbd-reactor for them.

### Where HA should restart a guest

A guest runs on any PVE node: one without a replica attaches as a diskless
client and reads and writes over the network. That makes every node a valid HA
target, but the replica nodes are the fast ones. Tell `ha-manager` to prefer
them, without forbidding the rest (a strict rule would leave the guest down
when both replica nodes are):

```bash
sds resource list | grep pve-100-        # the guest's disks and their nodes

# PVE 9: node affinity rules
ha-manager rules add node-affinity sds-vm-100 --resources vm:100 \
    --nodes pve1:2,pve2:2,pve3:1 --strict 0

# PVE 8: HA groups
ha-manager groupadd sds-pve1-pve2 --nodes pve1:2,pve2:2,pve3:1
ha-manager set vm:100 --group sds-pve1-pve2
```

Guests whose disks share replica nodes can share a rule or group. A guest
whose disks sit on different replica pairs prefers the nodes common to all of
them. In a "two storage nodes plus one tiebreaker" cluster, the third machine
is a DRBD tiebreaker for sds and a QDevice for corosync — both roles, since a
QDevice alone gives DRBD no quorum vote. It is usually not a PVE node at all;
when it is one, give it the lowest priority.

## Limitations

- **Raw only.** DRBD exports a raw block device; qcow2 is not supported and not
  needed (snapshots come from sds, not the image format).
- **Linked clones are full copies.** They take the template's full size and
  the time to copy it.
- **Volume names.** `vm-<vmid>-<name>` with `<name>` starting with a letter
  and made of letters, digits, `_` and `-` (at most 64), and
  `base-<vmid>-disk-<n>`; PVE's own names all fit.
- **Snapshot access** only on a node holding a replica, and only for LVM.
- **Whole-gigabyte allocation** unless `exactsize` is set.
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
