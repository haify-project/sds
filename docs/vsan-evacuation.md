# Evacuating VMware vSAN to Proxmox VE on Haify

A runbook for moving virtual machines off a VMware vSAN cluster onto Proxmox VE
(PVE) whose guest disks live on Haify through the
[Haify storage plugin](../deploy/proxmox/README.md). It strings together
documented behavior of vSphere, PVE and Haify; it is not a product feature, and
nothing in it automates the move.

**Read this first.** The steps marked **[verify]** have not been validated end
to end on a real vSAN-to-PVE migration by the Haify project. Each is listed again
in [What to verify on your cluster](#what-to-verify-on-your-cluster). Rehearse
the whole procedure with a VM you can lose before you move one you cannot.

---

## Why there is a staging step

PVE 8.2 and later ship an ESXi import wizard: PVE connects to an ESXi host
(6.5 to 8.0) and copies a VM's configuration and disks into PVE storage,
optionally as a *live import* that starts the VM in PVE while its disks are
still being copied. The wizard cannot import disks that live on vSAN.

The workaround is to take the disks off vSAN first, onto a datastore the wizard
can read. Haify can provide that datastore itself, as an NFS export:

```
 vSAN datastore
     │  Storage vMotion (vCenter), VM keeps running
     ▼
 NFS datastore "haify-staging"  ◄── Haify NFS gateway (one resource, floating service IP)
     │  PVE ESXi import wizard, or qm disk import
     ▼
 PVE storage "haify0" (type haify) ── one DRBD resource per guest disk
```

The staging datastore is temporary. Once the last VM has been accepted on PVE,
it is unmounted and its resource deleted.

## Target layout

The common small layout is **two storage nodes plus one tiebreaker**:

- **Two storage nodes** hold the replicas (a Haify pool on each) and usually
  are the two PVE nodes running the guests. Separate compute-only PVE nodes also
  work: they attach to each disk as diskless DRBD clients
  (`deploy/proxmox/README.md`).
- **A third machine** runs DRBD 9 and is registered as a Haify node, without a
  pool. Haify makes it the diskless tiebreaker of every two-replica resource
  (`[resource] auto_tiebreaker`, on by default), so one storage node can fail
  without the survivor losing quorum. It can also be the corosync QDevice that
  gives a two-node PVE cluster its third vote. It has to be both: **a QDevice
  alone gives DRBD no quorum vote.** Without DRBD on the third machine, the
  unplanned loss of one storage node leaves every disk without quorum.

What Haify deliberately leaves to PVE, because PVE already does it:

| Concern | Who does it |
| ------- | ----------- |
| Restarting a guest on a surviving node (VM-level HA) | PVE `ha-manager`. Haify only makes the disk promotable there, quorum-guarded |
| Fencing a node that stopped responding | PVE HA's fencing |
| Backing up guests | PVE `vzdump` to Proxmox Backup Server |
| Replicated disks, quorum, live migration without copying data | Haify |

Do not put guest disks under `haify ha create` or a Haify gateway; the plugin's
README explains why both refuse them.

## Prerequisites

- [ ] **PVE 8.2 or later** on every PVE node; source ESXi hosts **6.5 to 8.0**.
- [ ] **A Haify cluster**, set up per the [deployment guide](deployment-guide.md):
      controller running, the two storage nodes and the tiebreaker registered,
      a pool on both storage nodes. `haify node list`, `haify pool list`.
- [ ] **The Haify plugin on every PVE node** ([plugin README](../deploy/proxmox/README.md),
      preferably the `haify-pve-plugin` package) and one `haify:` storage entry in
      `/etc/pve/storage.cfg`, here called `haify0`, with `content images` and
      `shared 1`. `preflight.sh` passes on every PVE node.
- [ ] **NFS gateway packages on both storage nodes**: `resource-agents-extra`
      and `nfs-kernel-server` (deployment guide, section 2).
- [ ] **A free IP address** for the gateway's service IP, in a subnet the ESXi
      hosts' NFS VMkernel interfaces reach, with NFSv3 allowed between them
      (2049, plus rpcbind 111 and mountd if a firewall sits in between).
- [ ] **PVE nodes reach the ESXi hosts' management interface** (HTTPS) for the
      import wizard, and the staging service IP if you use `qm disk import`.
- [ ] **vCenter able to run Storage vMotion** for running VMs. A powered-off VM
      moves with the same "change storage only" migration without it.
- [ ] **Windows guests: VirtIO drivers installed while still on ESXi** (see
      [Guest preparation](#guest-preparation)).
- [ ] A **list of VMs in waves**, each with its provisioned disk sizes, owner
      and acceptable downtime.

## Capacity planning

Two things hold data at the same time: the staging resource and the final
guest disks. Both are replicated, so both cost their size on **each** storage
node.

- **Staging resource**: at least the provisioned size of every disk in the
  largest wave, plus headroom. ESXi often stores disks thin on an NFS
  datastore, which then needs less, but plan for the provisioned size **[verify]**.
  Do not let it fill up in the middle of a wave.
- **Final disks on `haify0`**: each disk's provisioned size, byte for byte (see
  [Sizes](#sizes-exact-by-default)).
- **Per storage node** during a wave: staging size + the final size of every
  VM imported so far. On a thin pool, written data is what counts, but an
  overcommitted thin pool that fills up stops writes on every volume in it;
  watch `haify pool list`.

Example: a wave of six VMs with 2 TiB provisioned in total, on two replicas.
Staging 2.2 TiB, final disks 2 TiB: each storage node needs about 4.2 TiB free
while that wave is in flight, and keeps 2 TiB after the staging space is
reclaimed.

Plan the network as well. Storage vMotion sends every block from ESXi to the
gateway, and DRBD replicates each write to the second storage node; the
import then reads it back from NFS and writes it again into a replicated
`haify0` disk. The replication link is as much a bottleneck as the ESXi uplinks.

## Guest preparation

Do these on ESXi, before the VM's cut-over window:

- **Windows: install the VirtIO drivers** (`virtio-win`) while the VM still
  runs on ESXi. A Windows guest whose boot disk sits on a VirtIO SCSI
  controller without the driver does not boot. If a VM was moved without them,
  give its disk a SATA bus in PVE first, boot, install the drivers, then switch
  to VirtIO SCSI.
- **Linux**: the NIC changes from VMware's to VirtIO, so the interface name and
  MAC may change; note the network configuration so you can fix it on first
  boot. Check that the initramfs contains the `virtio_scsi`/`virtio_blk`
  drivers (most distributions include them).
- **VMware snapshots**: delete or consolidate them first. This runbook has not
  been tried with VMs that carry snapshots **[verify]**.
- **VMware templates**: the plugin has no templates or linked clones, so a
  template becomes an ordinary VM on `haify0`, or goes to another PVE storage.

Proxmox's "Migrate to Proxmox VE" guide covers the guest side (VMware Tools,
drivers, network) in more depth.

## Step 1: Staging resource and NFS gateway (Haify)

Create one resource on the two storage nodes, then an NFS gateway on it. One
gateway per resource; give the resource nothing else to do (no `ha create`, no
manual mounts).

```bash
haify resource create --name vsanstage --port 7100 --size 2300G \
    --nodes stor1,stor2 --pool vg0
haify resource status vsanstage

haify gateway nfs create --resource vsanstage \
    --service-ip 192.0.2.60/24 --export-path /srv/vsan-staging \
    --allowed-ips 192.0.2.0/24
```

- The gateway adds its small state volume itself and formats the data volume
  (`--fs-type`, default `ext4`; `xfs` is accepted).
- `--export-path` must be a dedicated directory (`/srv/...`); it is both the
  mount point on the active node and the path ESXi mounts.
- The export maps every client to root (`rw,all_squash,anonuid=0,anongid=0`),
  which ESXi needs. Restrict it with `--allowed-ips` to the ESXi VMkernel
  subnet (and the PVE nodes, for `qm disk import`); without the flag every
  address may mount it.

**Verify:**

```bash
haify gateway status --resource vsanstage    # running, on which node
ssh <active node> sudo exportfs -v          # the export and its options
```

Then test a switchover **before** any VM depends on the datastore: with the
datastore mounted on ESXi (step 2) and empty, run `haify ha evict vsanstage`
and check that ESXi still shows it accessible afterwards **[verify]**.

## Step 2: Mount it on ESXi

On every ESXi host that runs VMs of the wave (or once in vCenter: *New
Datastore → NFS → NFS 3*, selecting the hosts):

```bash
esxcli storage nfs add --host 192.0.2.60 --share /srv/vsan-staging --volume-name haify-staging
```

Use NFS 3. NFS 4.1 from ESXi against the gateway has not been tried **[verify]**.

**Verify:**

```bash
esxcli storage nfs list          # haify-staging: Accessible true, Mounted true, Read-Only false
vmkping 192.0.2.60
```

and create and delete a folder in the datastore browser.

## Step 3: Storage vMotion off vSAN

In vCenter: *VM → Migrate → Change storage only → haify-staging*, for each VM
of the wave (PowerCLI: `Move-VM -VM <name> -Datastore haify-staging`). Running
VMs keep running. Move a few at a time and avoid production hours: see the
network note under [Capacity planning](#capacity-planning).

**Verify:**

- the migration task completed, and the VM's *Datastores* tab lists only
  `haify-staging`;
- on the gateway's active node, `ls -la /srv/vsan-staging/<vm>/` shows the
  `.vmx` and `.vmdk` files;
- `haify pool list` still has the room the rest of the wave needs.

This step is fully reversible: Storage vMotion the VM back to vSAN.

## Step 4: Import into Proxmox VE

Shut the VM down in vSphere first: importing a running VM copies a disk that
is still changing. From here on, the VM's downtime runs.

### With the import wizard

1. *Datacenter → Storage → Add → ESXi*: the ESXi host that has `haify-staging`
   mounted, with its credentials.
2. Select that storage, the VM, *Import*.
3. Target storage **`haify0`**; format **raw** (the only format the plugin
   supports). Check the CPU, memory, network bridge and disk bus the wizard
   proposes (SATA for a Windows VM without VirtIO drivers).
4. Leave *live import* off for the first VMs. Read Proxmox's notes on live
   import before relying on it, and rehearse it on `haify0` first **[verify]**.

### With qm disk import

For full control, or for a disk the wizard does not handle: mount the staging
export read-only on a PVE node (it must be inside `--allowed-ips`) and import
each disk into an empty VM.

```bash
mkdir -p /mnt/vsan-staging
mount -t nfs -o ro,vers=3 192.0.2.60:/srv/vsan-staging /mnt/vsan-staging

qm create 101 --name app01 --memory 8192 --cores 4 --ostype l26 \
    --scsihw virtio-scsi-single --net0 virtio,bridge=vmbr0
qm disk import 101 /mnt/vsan-staging/app01/app01.vmdk haify0 --format raw
qm set 101 --scsi0 <volume id printed by the import> --boot order=scsi0
```

Pass the descriptor `.vmdk`, not the `-flat.vmdk` beside it **[verify]**.

Either way the disk is created through the plugin's `alloc_image`: one DRBD
resource per disk, named `<resourceprefix>-<vmid>-<n>` (`pve-101-0` with the
default prefix).

### Sizes: exact by default

The plugin gives every new disk **exactly the size PVE asks for**, so a VMware
disk whose size is not a whole number of GiB arrives byte for byte, and online
Move Disk and vzdump restore onto `haify0` both work: each refuses a disk that is
not the source's exact size. A storage set to `exactsize 0` rounds disks up to
whole GiB instead, and loses both.

**Verify:**

```bash
qm config 101                              # disks on haify0, sizes as expected
pvesm list haify0 --vmid 101
haify resource list | grep pve-101-          # one resource per disk, on stor1 and stor2
haify resource status pve-101-0              # UpToDate on both replicas
```

## Step 5: First boot and hand-over to PVE

1. Start the VM from the console. Fix the network configuration if the
   interface changed; on Windows, confirm the VirtIO devices in Device Manager
   and switch a SATA disk to VirtIO SCSI if you used that workaround.
2. Install `qemu-guest-agent` and `qm set 101 --agent 1`; remove VMware Tools
   following Proxmox's guide.
3. Application checks by the VM's owner.
4. Put it under PVE HA (`ha-manager add vm:101`), with the node preference the
   plugin README describes, and into a backup job to PBS.
5. Live-migrate it once between the two storage nodes: it should copy RAM only.

## Step 6: Clean up

After the VM is accepted, delete it in vSphere (*Delete from Disk*), which
frees its space on the staging datastore for the next wave. After the last
wave:

```bash
umount /mnt/vsan-staging                                   # on PVE, if mounted
esxcli storage nfs remove --volume-name haify-staging         # on every ESXi host
haify gateway delete --resource vsanstage
haify resource delete vsanstage
```

## Rollback

The ESXi copy of a VM stays intact until you delete it in step 6, so every
step up to acceptance can be undone:

| Reached | To roll back |
| ------- | ------------ |
| Step 1-2 | Unmount the datastore; delete the gateway and resource |
| Step 3 (on staging, still running on ESXi) | Storage vMotion back to vSAN |
| Step 4 (imported, not started in PVE) | `qm destroy <vmid>` (removes its `haify0` disks); power the VM on in vSphere |
| Step 5 (running in PVE) | Stop the PVE VM, power the vSphere VM on. **Anything written in PVE since the cut-over is lost**; there is no path back from PVE to vSphere in this runbook |

Never run both copies at once: they would claim the same IP addresses, and
the application's data would diverge.

## Cut-over window checklist

Days before:

- [ ] Prerequisites done, gateway switchover tested (step 1), rehearsal VM
      migrated end to end.
- [ ] Wave's VMs on `haify-staging` (step 3); `haify pool list` has room for their
      final disks.
- [ ] VirtIO drivers in Windows guests; VMware snapshots consolidated.
- [ ] Owners know the window, the acceptance checks and the rollback point.
- [ ] `haify resource list` / `haify health-check`: every resource UpToDate on both
      storage nodes; `pvecm status` quorate.

In the window, per VM:

- [ ] Shut down in vSphere.
- [ ] Import to `haify0` (step 4) and verify disks and sizes.
- [ ] First boot, network, guest agent (step 5).
- [ ] Owner accepts, or roll back (table above).
- [ ] HA and backup job added.

After the window:

- [ ] One PBS backup of each migrated VM has completed.
- [ ] Accepted VMs deleted in vSphere; staging space reclaimed.
- [ ] After the last wave: staging datastore, gateway and resource removed.

## Known limitations

- **vSAN disks cannot be imported directly**, which is why the staging
  datastore exists; a VM already on a VMFS or NFS datastore can skip steps 1-3.
- **A storage set to `exactsize 0`** cannot take an online Move Disk or a vzdump
  restore: see [Sizes](#sizes-exact-by-default).
- **Windows guests** need the VirtIO drivers before cut-over, or the SATA
  workaround.
- **Storage vMotion loads the network** and the staging resource's
  replication link; spread waves out.
- **Staging capacity** limits the size of a wave.
- **Plugin limits** apply to imported VMs: raw only, no templates or linked
  clones, snapshots taken by Haify on one node (plugin README,
  [Limitations](../deploy/proxmox/README.md#limitations)).
- **Out of scope for Haify**: VM-level HA, fencing and PBS backups are PVE's.

## What to verify on your cluster

These steps follow from how the parts are documented to behave but have not
been exercised end to end by the Haify project:

1. ESXi keeps an NFS 3 datastore on the Haify gateway accessible across a
   gateway switchover (`haify ha evict`), and through it a running Storage
   vMotion.
2. NFS 4.1 mounts from ESXi (this runbook uses NFS 3).
3. How much staging space Storage vMotion actually uses (thin or provisioned).
4. The PVE import wizard and live import writing to `haify0`, including a live
   import that fails part-way.
5. `qm disk import` from the descriptor `.vmdk` on a read-only NFS mount.
6. VMs that carry VMware snapshots.
7. Your guests' behavior on first boot (drivers, interface names).
