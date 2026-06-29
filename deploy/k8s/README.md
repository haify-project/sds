# SDS CSI Driver — Phase ① deploy

Prerequisites on every worker node: DRBD kernel module loaded, `drbd-utils`,
and LVM (or ZFS) installed. (drbd-module-loader DaemonSet is sub-project ④.)

    kubectl apply -f deploy/k8s/

Set `--sds-controller` in `20-controller.yaml` / `30-node.yaml` to your
sds-controller gRPC address (default `sds-controller:3374`). Adjust the
StorageClass `pool` to a real VG/zpool name on the nodes.
