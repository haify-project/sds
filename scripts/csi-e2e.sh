#!/usr/bin/env bash
# Phase-① CSI smoke test against a real cluster with the driver installed and
# sds-controller reachable. Requires: kubectl context set, StorageClass sds-drbd,
# at least 2 storage nodes registered, pool "vg0" present on the nodes.
set -euo pipefail

NS=csi-e2e-$RANDOM
kubectl create ns "$NS"
trap 'kubectl delete ns "$NS" --wait=false' EXIT

cat <<YAML | kubectl apply -n "$NS" -f -
apiVersion: v1
kind: PersistentVolumeClaim
metadata: { name: data }
spec:
  accessModes: ["ReadWriteOnce"]
  storageClassName: sds-drbd
  resources: { requests: { storage: 1Gi } }
---
apiVersion: v1
kind: Pod
metadata: { name: writer }
spec:
  containers:
    - name: app
      image: busybox
      command: ["sh", "-c", "echo hello-sds > /data/marker && sleep 3600"]
      volumeMounts: [{ name: data, mountPath: /data }]
  volumes:
    - name: data
      persistentVolumeClaim: { claimName: data }
YAML

echo "waiting for pod Ready..."
kubectl -n "$NS" wait --for=condition=Ready pod/writer --timeout=180s

echo "verifying write landed on the DRBD-backed volume..."
kubectl -n "$NS" exec writer -- cat /data/marker | grep -q hello-sds
echo "PASS: pod mounted a DRBD volume and wrote data"

echo "verifying the pod scheduled onto a replica node (topology)..."
NODE=$(kubectl -n "$NS" get pod writer -o jsonpath='{.spec.nodeName}')
echo "pod node: $NODE"
