# OpenStack

Two pieces, in one Python package:

- `haify_cinder`, a Cinder volume driver;
- `haify_horizon`, a Horizon plugin that shows where each volume's replicas
  are ([below](#horizon)).

`haify_cinder` is a Cinder volume driver. A Cinder volume is a Haify
resource, replicated by DRBD across the storage nodes, and a compute host
uses it as the block device `/dev/drbd/by-res/<resource>/0`. A host that
holds a replica reads and writes its own disk. A host that holds none joins
the resource as a DRBD diskless client and reaches the replicas over the
network. No iSCSI target sits in the data path, and no gateway node can fail
under a running instance.

## What it does

| Cinder operation | Haify |
| --- | --- |
| create, delete, extend | a resource with one volume, of exactly the size asked for, labelled `haify.openstack/managed-by=cinder` |
| snapshot, delete snapshot | a resource snapshot, taken on every replica at once with I/O suspended across them |
| revert to snapshot | rolls every replica back together; a volume extended after the snapshot keeps its size |
| volume from snapshot, clone | a new resource on the source's replica nodes, filled from the snapshot on one of them; a clone goes through a snapshot taken for it |
| attach | the host joins as a diskless client if it holds no replica; the volume is made Primary there, guarded by quorum |
| detach | Secondary on the host; a diskless client is detached |
| capacity | the pool's room for new volumes, divided by the copies each volume keeps |

The label tells the controller that OpenStack decides where the volume is
Primary: `haify ha create` and gateways are refused on it, so drbd-reactor
never competes with Nova for the role.

### One writer, and live migration

DRBD lets one host write at a time, and Haify resources do not promote
themselves. When a volume is attached while another host holds it Primary:

- **That host also has an attachment of the volume**: a live migration.
  Nova attaches the volume to the destination before the move and detaches
  it from the source afterwards, so for that interval both hosts hold it
  Primary. When Nova detaches the source, the window closes.
- **No attachment there**: something left it Primary, such as an image copy
  or a failed detach. It is made Secondary. If something on that host still
  has it open, DRBD refuses the demotion, and the attach fails with the
  reason instead of starting a second writer.

An evacuation deletes the old host's attachment before attaching the new
host, so it never opens the window. If the old host is down, its role does
not matter. If it is up and its guest still runs, the guest holds the device
open, and the new host is refused.

Detaching from a host that cannot be reached succeeds. The host's guest is
gone, and DRBD comes back up Secondary there.

## Requirements

- haify-controller, with a pool on the storage nodes.
- On **every compute host and the cinder-volume host**, DRBD 9 and drbd-utils.
  Each must be reachable by the controller like a storage node: registered
  with `haify node register`, with the controller's SSH key. A compute host
  does not need a pool or a replica.
  - The cinder-volume host attaches volumes itself, to copy images into them
    and out of them.
- On every compute host, **the libvirt hook** (`deploy/libvirt`, `install.sh`):
  - When an instance starts it promotes the instance's disks again, for
    example after the host rebooted. Nova does not ask Cinder at that point.
  - It is the libvirt-side half of live migration.

## Install

On the cinder-volume host, into the Python environment Cinder runs from:

```bash
pip install ./deploy/openstack
```

`cinder.conf`:

```ini
[DEFAULT]
enabled_backends = haify

[haify]
volume_driver = haify_cinder.driver.HaifyDriver
volume_backend_name = haify
haify_controller = 192.168.1.10,192.168.1.11
haify_pool = pool0
```

Then restart cinder-volume, and create a volume type for the backend:

```bash
openstack volume type create haify --property volume_backend_name=haify
```

| Option | Default | |
| --- | --- | --- |
| `haify_controller` | | REST addresses, `host` or `host:port` (3375) or `http(s)://…`. List every node that can run the controller under Self-HA |
| `haify_token_file` | | bearer token, with `[auth]` or `[rbac]` on the controller |
| `haify_pool` | | pool for new volumes; empty lets the controller choose |
| `haify_storage_type` | | `lvm`, `lvm-thin` or `zfs`; empty is the controller's default |
| `haify_replicas` | 2 | replicas per volume, placed by the controller |
| `haify_nodes` | | put every volume on exactly these nodes instead |
| `haify_resource_prefix` | `cinder-` | the resource of volume `<id>` is `<prefix><id>` |
| `haify_node_map` | | `compute1:node-a,…` when Nova's host names differ from the Haify node names; otherwise the short host name is used |
| `haify_on_no_quorum` | `suspend-io` | a guest's I/O waits while its host has lost quorum, rather than failing (`io-error`) |
| `haify_copy_timeout` | 3600 | seconds allowed for calls that copy or resync data |

A volume from a snapshot, and a clone, need an LVM pool. On ZFS they are
refused.

cinder-volume starts even when the controller does not answer, for example
while Self-HA is moving it. The backend reports itself down until the
controller answers, and an operation in that window fails with the reason.

The driver and the plugin reach the controller directly, never through
`http_proxy`. The controller is on the storage network, and urllib does not
honour CIDR ranges in `no_proxy`.

## Horizon

The plugin adds two things for admins:

- A **Haify** panel under Admin › Volume, with two tables:
  - each Cinder volume on Haify: the instance it is attached to, the node it
    is Primary on, every replica with its state, and whether it is healthy,
    syncing or degraded;
  - each node: its pool's room, and how many replicas, tiebreakers, diskless
    clients and Primaries it holds.
- A **Replication** tab on the details of a Haify volume. It shows the
  volume's members and their roles, disk states and sync progress, the
  snapshots on the replicas under their Cinder names, the resource's labels,
  and a link that opens the resource in Haify's web UI.

Project users see neither.

On each Horizon host, into Horizon's Python environment:

```bash
pip install ./deploy/openstack
cp "$(python -c 'import haify_horizon, os; print(os.path.dirname(haify_horizon.__file__))')/enabled/_2225_admin_haify_panel.py" \
   /path/to/openstack_dashboard/local/enabled/
cat > /path/to/openstack_dashboard/local/local_settings.d/_90_haify.py <<'EOF'
HAIFY_CONTROLLER = "192.168.1.10,192.168.1.11"
HAIFY_POOL = "pool0"                          # as haify_pool
HAIFY_UI_URL = "http://192.168.1.10:3376"     # optional: links into Haify
# HAIFY_TOKEN_FILE, HAIFY_RESOURCE_PREFIX ("cinder-"), HAIFY_BACKENDS (["haify"])
EOF
systemctl reload apache2
```

The panel asks the controller for the volumes' statuses eight at a time.
Without the controller it says so and lists nothing.

## Tested

Setup:

- devstack master (2027.1) on Ubuntu 24.04, with two compute hosts (os1 runs
  the control plane and cinder-volume as well).
- Both hosts are Haify storage nodes. A third node runs haify-controller and
  is the tiebreaker.
- DRBD 9.3.4 and an LVM thin pool.
- A cirros instance booted from a Haify volume, with a second Haify volume
  attached.

Results:

- **Basic operations.** A bootable volume created from an image, a boot from
  it, and a hot-plugged data volume all worked. The guest's disks were the
  `/dev/drbd/by-res/…` devices, Primary on its host.
- **Data checked in the guest.** In each case the guest read the data back and
  its SHA-256 matched:
  - a snapshot of the in-use volume;
  - a volume created from that snapshot, which held the data as at the
    snapshot;
  - a clone of the in-use volume, which held its current data;
  - extend;
  - revert to snapshot.
- **Live migration, os1 → os2 → os1, with the guest writing.**
  - Both disks were Primary on both hosts during each move.
  - Afterwards only the destination was Primary, and two-primaries was off on
    both hosts.
  - The guest did not restart, and its data was unchanged.
- **A live migration that failed** (libvirt's own SSH to the destination was
  refused): Nova rolled back, the destination was made Secondary again and the
  window closed. The guest kept running on the source.

- **Horizon.**
  - The panel and the tab showed the replicas, roles and health that Haify
    reported.
  - A replica taken down showed its volume as degraded, and as healthy again
    once it came back.
  - An LVM volume had no tab and was not listed.
  - The demo user had no panel; opening its URL directly returned 403.
  - With the controller stopped, both pages showed the reason instead of an
    error page.
  - The links to the volume, the instance, the snapshot and Haify's UI opened
    the right page.
- **cinder-volume restarted while the controller was stopped.** The driver
  initialized. Once the controller was back, a volume was created and deleted
  without restarting cinder-volume.

Live migration needs SSH from root on each compute host to the user in
`live_migration_uri` on the others: Nova migrates peer to peer, so it is
libvirt's daemon, running as root, that connects.

## Tests

```bash
python3 -m unittest discover -s deploy/openstack/tests -p 'test_*.py'
```

The tests replace the controller with a fake and stub the few Cinder modules
the driver imports, so they run without an OpenStack install. With Django
installed they also compile the plugin's templates; `make ci` runs them that
way.
