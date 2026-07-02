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
