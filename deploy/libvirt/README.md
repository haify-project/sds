# KVM with libvirt

A Haify volume is a block device, `/dev/drbd/by-res/<resource>/<volume>`, and
libvirt runs a guest on it like on any other disk. What libvirt does not know
is that DRBD lets one node write at a time: the host running a guest must hold
its disks Primary, every other host must not, and a live migration needs both
for a moment. Haify resources are created with `auto-promote no`, so without
help `virsh start` fails with `Read-only file system`.

Two pieces cover that:

- **The libvirt hook** (`haify-hook.py`) promotes and demotes a guest's disks
  as libvirt starts, stops and migrates it. `virsh start`, `virsh shutdown`
  and `virsh migrate --live` then work as on shared storage.
- **`haify ha create <resource> --vm <guest>`** hands a guest to drbd-reactor:
  it runs where the resource is Primary and is restarted on another replica
  when that node fails.

## What the hook does

libvirt calls `/etc/libvirt/hooks/qemu.d/haify` at each step of a guest's
life, with the guest's XML. A disk whose source is
`/dev/drbd/by-res/<resource>/<volume>` is a Haify disk; others are ignored.

| libvirt step | on this host |
| --- | --- |
| `prepare begin` (start, restore, incoming migration) | promote each Haify disk, quorum-guarded; a host with no replica first joins as a diskless client |
| `migrate begin` (destination of a live migration) | note the migration, so `prepare` may open the dual-primary window |
| `release end` (stopped, or left in a migration) | demote each disk, close the dual-primary window, detach a diskless client |

While another node holds a disk Primary, the hook opens the dual-primary
window only for a live migration to this host that libvirt has just
announced. Any other Primary is a leftover, and starting a second writer is
refused with the reason; libvirt shows it as the start error. A disk that
fails to promote demotes the ones promoted before it.

Promotion goes through haify-controller. When no controller answers, a disk
already up on the host is promoted or demoted with plain `drbdadm` (never
`--force`), which DRBD's quorum still guards; a live migration then needs the
controller.

A resource that a drbd-reactor promoter manages on the host (`ha create`) is
left alone: drbd-reactor owns its role.

## Install

On every KVM host, as root, with `python3`, DRBD 9 and drbd-utils installed and
the host registered as a Haify node (or reachable by the controller, for a host
that only runs guests):

```bash
./deploy/libvirt/install.sh --controller 192.168.1.10,192.168.1.11 --node kvm1
```

`--controller` lists the controller's REST addresses (port 3375 by default);
list every Self-HA controller host. `--node` is the host's Haify node name
when it differs from its short host name; `--token-file` points at a bearer
token when the controller has `[auth]` or `[rbac]`. The settings go to
`/etc/haify/libvirt.conf`, the hook beside any other qemu hook, and libvirt's
daemon (`virtqemud` or `libvirtd`) is restarted to load it; running guests do
not notice. The hook logs to the journal as `haify-libvirt-hook`.

## Defining a guest

```xml
<domain type='kvm'>
  <name>web1</name>
  <uuid>6f1b2c3d-4e5f-4a6b-8c7d-9e0f1a2b3c4d</uuid>
  ...
  <devices>
    <disk type='block' device='disk'>
      <driver name='qemu' type='raw' cache='none' io='native'/>
      <source dev='/dev/drbd/by-res/web1/0'/>
      <target dev='vda' bus='virtio'/>
    </disk>
    <video><model type='vga'/></video>
    ...
```

- **The disk source is the `by-res` path.** `/dev/drbdN` works for libvirt but
  the hook does not recognise it.
- **`cache='none'`** (or `directsync`): libvirt refuses to live-migrate a guest
  on shared block storage with host caching.
- **Define the guest on every host it may run on, with the same UUID.** Two
  `virsh define` runs of an XML without `<uuid>` give two UUIDs, and a
  migration then fails on the destination's existing definition.
- **Give the guest a display device.** A Debian 13 cloud kernel with no video
  device hung before its first console line in testing; adding `<video>` fixed it.

## Live migration

```bash
virsh migrate --live web1 qemu+ssh://kvm2/system
```

The destination's hook opens the dual-primary window for the two hosts and
promotes; once the guest has moved, the source's hook demotes and closes the
window, leaving one Primary.

## High availability

```bash
virsh define web1.xml        # on every diskful replica, autostart off
haify ha create web1 --vm web1
```

The promoter runs `ocf:heartbeat:VirtualDomain` (from `resource-agents`) with
the definition libvirt keeps in `/etc/libvirt/qemu/web1.xml`. drbd-reactor
promotes the resource and starts the guest on one replica, and when that node
fails another replica with quorum promotes and starts it. That is a restart,
like after a power cut: the guest boots from what was on disk. Do not start
or migrate an HA guest with `virsh`; move it with `haify ha evict web1`.

## Tested

On three Debian 13 hosts (libvirt 11.3, DRBD 9.3.4, drbd-reactor 1.12.0), two
with replicas and one tiebreaker, with a Debian 13 guest:

- `virsh start` without the hook: `Read-only file system`. With it the disk is
  promoted, and `virsh destroy` demotes it.
- `virsh start` on the second host while the guest ran on the first was refused
  with the reason, and the running guest was untouched.
- Live migration both ways: one Primary afterwards, the dual-primary window
  closed on both hosts. A loop in the guest writing and syncing a timestamp
  every 0.1 s saw at most 0.33 s between two writes, and a 32 MiB file written
  before the migrations read back with the same SHA-256.
- With the controller stopped, stop and start fell back to `drbdadm`.
- `ha create --vm`: the guest ran on one replica. After a hard power-off of
  that host it was running on the other after 20 s, reachable over SSH after
  79 s (its own boot), with the file intact. The returning host rejoined as a
  Secondary and resynced, and nothing started the guest twice.
