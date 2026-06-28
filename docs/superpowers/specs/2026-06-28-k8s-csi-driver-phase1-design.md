# SDS Kubernetes CSI Driver — Phase ① Design

Date: 2026-06-28
Status: Approved (design), pending implementation plan

## Context

SDS is a DRBD-based storage controller. Today it runs outside Kubernetes and
drives storage nodes over SSH (`dispatch`) + drbd-reactor. The goal is to let
Kubernetes workloads consume DRBD-replicated storage in a **hyperconverged**
topology (storage runs on the k8s worker nodes themselves), matching the
LINSTOR/Piraeus model.

The full "Piraeus-parity" goal is decomposed into four sub-projects, each with
its own spec → plan → implementation cycle:

1. **① CSI core** — dynamic provisioning + local mount + replica-node topology (this spec)
2. **② Diskless attach** — pods schedule onto non-replica nodes via a DRBD diskless client
3. **③ CSI snapshots + volume expansion** — reuse existing snapshot/resize RPCs
4. **④ Packaging / Operator** — CRDs, RBAC, drbd-module-loader DaemonSet, leader election

This document covers **sub-project ① only**.

## Decisions

- **Scope target:** Piraeus-parity overall; this phase delivers the foundation
  (provisioning + mount + replica-pinned topology). Diskless attach is ②.
- **Topology:** hyperconverged — storage on the k8s worker nodes.
- **Architecture:** Approach A — a thin CSI translation layer that reuses the
  existing `sds-controller` gRPC API; SSH execution is retained. The CSI
  boundary is stable, so a later move off SSH (Approach C/B) only swaps the
  controller's execution backend without touching the CSI layer.
- **Repo:** monorepo — code lives in the existing `sds` repo, reusing
  `pkg/client` and `api/proto/v1`.

## Architecture

```
 k8s: PVC ──> external-provisioner ──┐
                                     ▼
              csi-controller (Deployment, 1 replica)
                   │ gRPC (reuses pkg/client)
                   ▼
              sds-controller (Deployment, BBolt on PVC)
                   │ SSH (dispatch) — unchanged
                   ▼
              each worker node: DRBD / LVM / ZFS

 Pod mount:  kubelet ──> csi-node (DaemonSet, privileged)
                           ├─ calls sds-controller.SetPrimary(this node)
                           └─ local mkfs + mount into the Pod
```

The controller's own drbd-reactor self-HA is dropped in k8s; the
`sds-controller` runs as a Deployment (single replica for ①; leader election
deferred to ④). The "active-controller-only" logic (e.g. the snapshot
scheduler) maps to k8s leader election later.

## New Components (monorepo)

- `cmd/csi-controller` — CSI Controller plugin binary
- `cmd/csi-node` — CSI Node plugin binary
- `pkg/csi/`
  - `driver.go` — driver name/version, unix-socket gRPC server, wiring of the
    reused `pkg/client` connection to `sds-controller`
  - `identity.go` — Identity service: GetPluginInfo, GetPluginCapabilities, Probe
  - `controller.go` — Controller service: CreateVolume, DeleteVolume,
    ControllerGetCapabilities, ValidateVolumeCapabilities
  - `node.go` — Node service: NodeStageVolume, NodeUnstageVolume,
    NodePublishVolume, NodeUnpublishVolume, NodeGetCapabilities, NodeGetInfo

No `external-attacher` / ControllerPublishVolume: the attach step is folded into
NodeStageVolume.

Driver name (proposed, reverse-DNS, may be revisited): `sds.csi.liliang-cn.com`.

## Bridges / Gaps to Implement

### 1. k8s node name ↔ sds node IP mapping

sds keys nodes by IP (`Address`); k8s topology uses node names. On startup,
`csi-node` auto-registers its own node into sds via
`RegisterNode(localIP, k8sNodeName)` and reports topology with the k8s node
name. `csi-controller` cross-references via `ListNodes` (which carries both name
and address). Side benefit: manual `sds-cli node register` is no longer
required.

### 2. Automatic port allocation (existing API gap)

`CreateResource` currently requires an explicit port; CSI has no human to pick
one. Add server-side auto-allocation in the controller: when `port == 0`, pick a
free port using the existing `findPortConflict` logic. Minor numbers are already
auto-allocated (`nextGlobalMinor`). Small controller-side change, in scope of ①.

### 3. DRBD resource name ↔ CSI volume mapping

CSI volume names are `pvc-<uuid>` (40 chars, hyphens). We return a `volume_id`
(volumeHandle) we control = the chosen DRBD resource name (sanitized for length
and charset). The mapping needs no separate store: the volumeHandle is persisted
by Kubernetes in the PV object, and the reverse lookup is `GetResource` by name.
CreateVolume is idempotent: on a repeated name, `GetResource` first and return
the existing volume if found.

## Data Flow

### CreateVolume (called by external-provisioner)

1. Parse StorageClass parameters: `pool` (VG/zpool), `replicas` (diskful copy
   count, e.g. 2), `storageType` (lvm/zfs), `fsType` (default ext4).
2. Select replica nodes from `ListNodes`, preferring nodes that satisfy the
   request's topology `requisite`. With `volumeBindingMode:
   WaitForFirstConsumer` the provisioner passes the target node's topology,
   guaranteeing one replica lands on the node the Pod will schedule to.
3. Call `CreateResource(name=sanitized, port=0 (auto), nodes=selected, pool,
   sizeGB, storageType)` — reused. With `replicas=2` the controller
   auto-adds a diskless tiebreaker.
4. Return `volume_id` = resource name; `accessible_topology` = one
   `{sds.io/node: <nodeName>}` segment per diskful replica node.

### DeleteVolume

`GetResource` not found → success (idempotent). Found → `DeleteResource`
(already includes tiebreaker teardown).

### NodeStageVolume (called by kubelet on the Pod's node)

1. Determine this node; `GetResource` to obtain the `/dev/drbdX` device path.
2. `SetPrimary(resource, thisNode, force=false)` — controller SSHes over and
   runs `drbdadm primary`.
3. `blkid` check; if unformatted, `mkfs.<fsType>`; mount the device at
   `staging_target_path`.

### NodePublishVolume / NodeUnpublishVolume / NodeUnstageVolume

- NodePublishVolume: bind-mount `staging_target_path` → Pod `target_path`.
- NodeUnpublishVolume: unmount `target_path`.
- NodeUnstageVolume: unmount `staging_target_path`, then
  `SetSecondary(resource, thisNode)` so the device can be promoted elsewhere.

Access mode: RWO (SINGLE_NODE_WRITER) — DRBD single-node Primary enforces it
naturally. All calls are idempotent (already-mounted → return success).

## Topology (replica-node pinning)

- `csi-node` reports `sds.io/node=<nodeName>` in `NodeGetInfo`.
- CreateVolume returns `accessible_topology` = the diskful replica node set, so
  the scheduler pins the Pod to a replica node.
- Requires: topology enabled on external-provisioner and StorageClass
  `volumeBindingMode: WaitForFirstConsumer`.
- ② (diskless attach) relaxes this constraint later.

## Minimal Deployment (① ships a runnable form; full Operator is ④)

- `sds-controller`: Dockerfile + Deployment + Service + Secret (SSH key) +
  ConfigMap (dispatch config + controller.toml) + PVC (BBolt).
- `csi-controller` Deployment: container + sidecars `external-provisioner`,
  `livenessprobe`.
- `csi-node` DaemonSet: privileged container with hostPath `/var/lib/kubelet`
  (mountPropagation: Bidirectional) and `/dev`; sidecar `node-driver-registrar`.
- `CSIDriver`, `StorageClass` (WaitForFirstConsumer) objects.
- Packaging: Helm chart / plain manifests for ①.

**Prerequisites (assumed satisfied in ①):** worker nodes already have the DRBD
kernel module, drbd-utils, and LVM/ZFS installed. The `drbd-module-loader`
DaemonSet is ④.

## Error Handling

- Map sds errors to CSI gRPC codes: NotFound, AlreadyExists,
  ResourceExhausted (no eligible node or free port), Internal.
- CreateResource already rolls back partial failures.
- NodeStageVolume failures must leave clean state (no half-mount, role reverted
  if mount fails).

## Testing

- **Unit:** name sanitization, topology computation, parameter parsing,
  idempotency logic — table-driven with a fake sds gRPC client.
- **csi-sanity** (kubernetes-csi/csi-test) for CSI conformance.
- **e2e on the real 3-node cluster:** install → create PVC → run a Pod → verify
  it mounts a DRBD Primary, write data → delete and confirm clean teardown.

## Out of Scope for ①

- Diskless attach (②) — Pods are pinned to replica nodes via topology.
- Snapshots and volume expansion (③).
- Full Operator / CRDs and the drbd-module-loader DaemonSet (④).
- RWX and raw block mode — ① is filesystem RWO only.
