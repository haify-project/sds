# SDS CSI Driver — Phase ① deploy

Prerequisites on every worker node: DRBD kernel module loaded, `drbd-utils`,
and LVM (or ZFS) installed. (drbd-module-loader DaemonSet is sub-project ④.)

    kubectl apply -f deploy/k8s/

## Point the CSI at your sds-controller VIP

The sds-controller runs **outside** the cluster on the storage hosts, HA'd
behind a floating VIP. There is no pod for it, so `00-sds-controller-endpoint.yaml`
ships a selectorless `Service` named `sds-controller` (port `3374`, named
`grpc`) plus a manual `Endpoints` that carries the VIP. This makes the
in-cluster name `sds-controller:3374` resolve to the external controller.

**You must set the VIP.** Edit `00-sds-controller-endpoint.yaml` and replace the
`192.168.123.250` placeholder (marked `# CHANGE ME`) with your real VIP. Find it
with:

    sds-cli ha self status

With this Service in place, the shipped default
`--sds-controller=sds-controller:3374` in `20-controller.yaml` /
`30-node.yaml` resolves and connects **without editing the Deployment or
DaemonSet args**. Only edit those args if you intentionally use a different
Service name or port. (An `ExternalName` Service will not work here — the
target is an IP address, not a DNS name.)

Adjust the StorageClass `pool` to a real VG/zpool name on the nodes.

## Volume snapshots

The driver implements the CSI `CREATE_DELETE_SNAPSHOT` capability, so a
`VolumeSnapshot` of an SDS volume becomes an LVM/ZFS snapshot of that volume's
backing store. This is what Kubernetes-native backup tools (Velero, Kasten)
orchestrate.

`LIST_SNAPSHOTS` is deliberately not advertised: snapshots live on individual
storage nodes with no cluster-wide index, so the driver cannot enumerate them.

### Cluster prerequisites (install once)

k3s and most distributions do **not** ship the snapshot machinery. Install the
CRDs and the snapshot-controller from
[kubernetes-csi/external-snapshotter](https://github.com/kubernetes-csi/external-snapshotter)
before applying `50-volumesnapshotclass.yaml`:

```bash
V=v8.2.0
B=https://raw.githubusercontent.com/kubernetes-csi/external-snapshotter/$V
kubectl apply -f $B/client/config/crd/snapshot.storage.k8s.io_volumesnapshotclasses.yaml \
              -f $B/client/config/crd/snapshot.storage.k8s.io_volumesnapshotcontents.yaml \
              -f $B/client/config/crd/snapshot.storage.k8s.io_volumesnapshots.yaml
kubectl apply -f $B/deploy/kubernetes/snapshot-controller/rbac-snapshot-controller.yaml \
              -f $B/deploy/kubernetes/snapshot-controller/setup-snapshot-controller.yaml
```

### Taking a snapshot

```yaml
apiVersion: snapshot.storage.k8s.io/v1
kind: VolumeSnapshot
metadata: { name: data-snap }
spec:
  volumeSnapshotClassName: sds-drbd-snapshot
  source:
    persistentVolumeClaimName: data
```

### What a snapshot is (and is not)

A snapshot is taken on **one** node's local backing volume — it is not
replicated by DRBD. It protects against logical faults (bad writes, accidental
deletion, a failed migration), not against losing that node: DRBD faithfully
replicates every write, including destructive ones, so replication is not a
backup. For protection against losing the cluster, ship snapshots off-site.

### Restoring and cloning

Both are supported via a PVC `dataSource`:

```yaml
# restore a snapshot into a new volume
spec:
  dataSource: { name: my-snap, kind: VolumeSnapshot, apiGroup: snapshot.storage.k8s.io }

# clone an existing volume
spec:
  dataSource: { name: my-pvc, kind: PersistentVolumeClaim }
```

The new volume is created empty, then filled by copying the source into its
**DRBD device** on a node that holds one of its replicas — so DRBD replicates
the data to every peer as part of the normal write path. Placement is forced to
include the node holding the source so the copy stays local.

Two consequences worth knowing:

- **Cloning snapshots the source first.** Reading a mounted volume's backing
  store directly would capture a torn image, so a clone takes an internal
  snapshot, copies from that, and drops it again.
- **`CreateVolume` blocks for the duration of the copy.** For large volumes,
  raise the external-provisioner `--timeout` accordingly. If the copy fails the
  half-written volume is destroyed so a retry starts clean; a controller crash
  mid-copy is the one window that can leave an empty volume behind.

Application-level consistency is still the application's job: snapshot a
database after a `CHECKPOINT`/`FSYNC` (or quiesce it) if you want more than
crash consistency. PostgreSQL and Redis both replay cleanly from a
crash-consistent snapshot in practice.
