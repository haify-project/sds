# Haify CSI Driver

Driver name: `haify.csi.liliang-cn.com`. Everything is deployed into
`kube-system` except the optional Copilot RBAC (`60-haify-ai-apps-rbac.yaml`,
namespace `haify-ai`).

| File | What it creates |
| ---- | --------------- |
| `00-csidriver.yaml` | `CSIDriver` (`attachRequired: false`, `storageCapacity: true`, `fsGroupPolicy: File`) |
| `00-haify-controller-endpoint.yaml` | Selectorless `Service` + manual `Endpoints` pointing at the external haify-controller |
| `10-rbac.yaml` | ServiceAccounts `haify-csi-controller`, `haify-csi-node`; ClusterRoles for provisioner, snapshotter, resizer and the health reporter |
| `20-controller.yaml` | Deployment `haify-csi-controller`: csi-provisioner, csi-resizer, csi-snapshotter, plugin (`csi-controller`), livenessprobe |
| `30-node.yaml` | DaemonSet `haify-csi-node`: node-driver-registrar, plugin (`csi-node`, privileged) |
| `40-storageclass.yaml` | StorageClass `haify-drbd` |
| `50-volumesnapshotclass.yaml` | VolumeSnapshotClass `haify-drbd-snapshot` (needs the snapshot CRDs, see below) |
| `60-haify-ai-apps-rbac.yaml` | ServiceAccount + token Secret for the Haify Copilot's `haify_k8s_app_create` tool |

## Prerequisites

- Every Kubernetes node that runs volumes is a registered Haify node with the
  DRBD 9 kernel module and `drbd-utils`, and LVM or ZFS. **The Kubernetes node
  name must equal the Haify node name**: the node plugin reports
  `spec.nodeName` as its topology, and the controller plugin matches it against
  `haify node list`. At startup the node plugin registers its node with the
  controller under that name and `status.hostIP` (`--node-name`/`--node-ip`,
  from `NODE_NAME`/`NODE_IP`); registration is idempotent, and the controller
  still needs SSH to that address.
- The Haify pool named in the StorageClass exists on at least `replicas` of
  those nodes (`haify pool create --name vg0 ...`).

## Build the image

Both plugin containers use `haify-csi:latest` with `imagePullPolicy: IfNotPresent`,
so the image must exist on every node or be pushed to a registry you then put
in `20-controller.yaml` / `30-node.yaml`:

    docker build -f Dockerfile.csi -t haify-csi:latest .
    docker save haify-csi:latest | sudo k3s ctr images import -    # on each k3s node

`Dockerfile.csi` cross-compiles for `--platform`, so `docker buildx build
--platform linux/amd64,linux/arm64 ...` works from either architecture.

## Point the CSI at your haify-controller VIP

The haify-controller runs **outside** the cluster on the storage hosts, HA'd
behind a floating VIP. `00-haify-controller-endpoint.yaml` ships a selectorless
`Service` named `haify-controller` (port `3374`, named `grpc`) plus a manual
`Endpoints` carrying the VIP, so the in-cluster name `haify-controller:3374`
reaches the external controller.

**You must set the VIP.** Replace the `192.0.2.10` placeholder (marked
`# CHANGE ME`) with your real VIP, shown as `VIP:` in:

    haify ha self status

The plugins' `--haify-controller=haify-controller:3374` (also the binaries'
default) then resolves without editing the Deployment or DaemonSet. An
`ExternalName` Service will not work here: the target is an IP, not a DNS name.

Both the controller Deployment and the node DaemonSet run with
`hostNetwork: true` and `dnsPolicy: ClusterFirstWithHostNet`: host networking
lets the controller pod reach a VIP that lives on its own node, and the DNS
policy keeps the Service name resolvable.

Then:

    kubectl apply -f deploy/k8s/

## StorageClass parameters

| Parameter | Default | Meaning |
| --------- | ------- | ------- |
| `pool` | required unless `resourceProfile` sets it | Haify pool name as given to `haify pool create --name` (`vg0` and `haify_vg0` both match) |
| `replicas` | `2` | Diskful copies |
| `storageType` | `lvm` | `lvm` or `zfs` |
| `allowRemoteVolumeAccess` | `false` | `true` lets a Pod run on a node with no replica: the node plugin attaches a diskless DRBD client there at stage time and detaches it at unstage. Without it, Pods are pinned to replica nodes |
| `faultDomainLabel` | `host` | Haify node label whose values replicas are spread across (`haify node label <node> host=<name>`); nodes without the label count as their own domain |
| `resourceProfile` | none | Haify resource profile; its pool, replica count and storage type apply unless the StorageClass sets them explicitly |
| `resourceLabels` | none | `key=value,key=value` labels put on the Haify resource. `haify.csi/managed-by=csi` is always added |

Placement: the node the scheduler picked (`WaitForFirstConsumer` with
`--strict-topology`) is seated first, the rest go to the nodes whose pool has
the most free space, preferring fault domains no replica uses yet. A volume
that cannot be placed fails with `ResourceExhausted`, which makes
external-provisioner reschedule the Pod.

The filesystem is `ext4` unless the StorageClass sets
`csi.storage.k8s.io/fstype` (the image ships `e2fsprogs` and `xfsprogs`).

## What the driver supports

- **Expansion.** `allowVolumeExpansion: true` plus the `csi-resizer` sidecar:
  raise a PVC's request and the LV, DRBD device and filesystem grow online.
- **Capacity-aware scheduling.** `storageCapacity: true` and the provisioner's
  `--enable-capacity` publish one `CSIStorageCapacity` per node from its pool's
  free space, so the scheduler does not pick a node that cannot hold the volume.
- **Usage metrics.** `NodeGetVolumeStats` feeds `kubelet_volume_stats_*` for
  filesystem volumes. Kubelet does not collect usage for raw block volumes.
- **Raw block.** `volumeMode: Block` hands the Pod the DRBD device itself.
- **Access modes.** `ReadWriteOnce` and `ReadWriteOncePod`. `ReadWriteMany`
  and the other multi-node modes are refused at provisioning: a DRBD resource
  has one Primary.
- **Replica health on the PVC.** The controller plugin checks each volume's
  replicas every `--health-interval` (default `1m`) and posts
  `Warning VolumeDegraded` / `Normal VolumeRecovered` events on its PVC when a
  replica disconnects, loses its disk, falls out of date or resyncs. A new
  volume's initial sync is not reported.

## Volume snapshots

The driver implements `CREATE_DELETE_SNAPSHOT` and `LIST_SNAPSHOTS`: a
`VolumeSnapshot` of a Haify volume becomes an LVM/ZFS snapshot of that volume's
backing store, which is what Kubernetes backup tools (Velero, Kasten)
orchestrate.

Snapshots live on individual storage nodes with no cluster-wide index, so
`ListSnapshots` walks the driver's volumes and asks each replica node. Only
snapshots the driver created (`haifysnap_*`) are listed; the controller's own
scheduled snapshots of the same LV are not.

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
  volumeSnapshotClassName: haify-drbd-snapshot
  source:
    persistentVolumeClaimName: data
```

`deletionPolicy: Delete` in the shipped class removes the LVM/ZFS snapshot
when the `VolumeSnapshot` is deleted; use `Retain` if it must outlive the
Kubernetes objects.

### What a snapshot is (and is not)

A snapshot is taken on **one** node's local backing volume; it is not
replicated by DRBD. It protects against logical faults (bad writes, accidental
deletion, a failed migration), not against losing that node. DRBD replicates
every write, including destructive ones, so replication is not a backup. For
protection against losing the cluster, ship snapshots off-site.

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
**DRBD device** on the node holding the source, so DRBD replicates the data to
every peer through the normal write path. Placement is forced to include that
node; if it cannot host a replica, provisioning fails with `ResourceExhausted`.

- **Cloning snapshots the source first.** Reading a mounted volume's backing
  store directly would capture a torn image, so a clone takes an internal
  snapshot, copies from that, and drops it again.
- **`CreateVolume` blocks for the duration of the copy.** The shipped
  provisioner `--timeout` is `120s`; raise it for large volumes. If the copy
  fails the half-written volume is destroyed so a retry starts clean; a
  controller crash mid-copy is the one window that can leave an empty volume
  behind.

Application-level consistency is the application's job: snapshot a database
after a `CHECKPOINT`/`FSYNC` (or quiesce it) if you need more than crash
consistency.

## When a node dies

Measured on a three-node k3s (2026-09-30): the VM was powered off, Kubernetes
took about 75 seconds to declare the node lost, and the replacement pod was
running on a surviving replica node about 9 seconds after that: 80 seconds of
outage for a Deployment, data intact, nothing to clean up on the Haify side. The
old node's replica resynchronised on its own when it came back, with no
split-brain.

Almost all of that time is Kubernetes deciding the node is gone. To shorten it
for a workload, lower the `not-ready` and `unreachable` tolerations on the pod:

```yaml
tolerations:
- {key: node.kubernetes.io/not-ready,   operator: Exists, effect: NoExecute, tolerationSeconds: 15}
- {key: node.kubernetes.io/unreachable, operator: Exists, effect: NoExecute, tolerationSeconds: 15}
```

Only controller-managed pods (Deployment, StatefulSet) are recreated. A bare
`Pod` on a dead node is deleted and stays gone; its volume is kept.

## Haify Copilot access (optional)

`60-haify-ai-apps-rbac.yaml` lets `haify-ai` create databases on Haify volumes
(the `haify_k8s_app_create` tool). It may create namespaces, Secrets, PVCs, Services
and Deployments and read Pods, PVs and StorageClasses; it cannot update or
delete anything. haify-ai runs outside the cluster and reads its kubeconfig from
`HAIFY_AI_KUBECONFIG`. Build one from the token Secret:

```bash
TOKEN=$(kubectl -n haify-ai get secret haify-ai-token -o jsonpath='{.data.token}' | base64 -d)
kubectl config view --minify --raw --flatten > haify-ai.kubeconfig
KUBECONFIG=haify-ai.kubeconfig kubectl config set-credentials haify-ai --token="$TOKEN"
KUBECONFIG=haify-ai.kubeconfig kubectl config set-context --current --user=haify-ai
```

Without `storageClass` in the request, it picks a Haify StorageClass that does
not set `allowRemoteVolumeAccess: "true"`, falling back to one that does.

## When registry.k8s.io is unreachable

`registry.k8s.io` redirects to regional Google Artifact Registry hosts, which
some networks (mainland China among them) cannot reach. Point k3s at a mirror in
`/etc/rancher/k3s/registries.yaml` on every node and restart k3s:

```yaml
mirrors:
  registry.k8s.io:
    endpoint: ["https://k8s.m.daocloud.io"]
```
