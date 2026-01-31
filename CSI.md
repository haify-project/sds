# CSI Operator 实现设计文档

## 1. CSI 架构概述

Kubernetes CSI (Container Storage Interface) 驱动由以下组件组成：

```
┌─────────────────────────────────────────────────────────────┐
│                     Kubernetes API Server                    │
└─────────────────────────────────────────────────────────────┘
                            ↓
┌─────────────────────────────────────────────────────────────┐
│                  CSI Sidecar Containers                    │
│  ┌──────────────┐  ┌──────────────┐  ┌──────────────┐      │
│  │ external-    │  │ external-    │  │ external-    │      │
│  │ provisioner  │  │ attacher    │  │ snapshotter  │      │
│  └──────────────┘  └──────────────┘  └──────────────┘      │
└─────────────────────────────────────────────────────────────┘
                            ↓
┌─────────────────────────────────────────────────────────────┐
│                    sds-csi-driver                           │
│              (Identity + Controller + Node)                 │
└─────────────────────────────────────────────────────────────┘
                            ↓
┌─────────────────────────────────────────────────────────────┐
│                   sds-controller (gRPC)                      │
└─────────────────────────────────────────────────────────────┘
                            ↓
┌─────────────────────────────────────────────────────────────┐
│              dispatch → SSH → storage nodes                 │
│              drbd-reactor + LVM + DRBD                       │
└─────────────────────────────────────────────────────────────┘
```

## 2. 当前 SDS 架构

| 组件 | 功能 | gRPC API |
|------|------|-----------|
| **Pool管理** | LVM VG管理 | CreatePool, ListPools, AddDiskToPool |
| **Resource管理** | DRBD资源管理 | CreateResource, DeleteResource, SetPrimary |
| **Volume管理** | LVM卷管理 | AddVolume, RemoveVolume, ResizeVolume |
| **Snapshot管理** | LVM快照 | CreateSnapshot, DeleteSnapshot, RestoreSnapshot |
| **Gateway管理** | NFS/iSCSI/NVMe-oF | CreateNFSGateway, CreateISCSIGateway, CreateNVMeGateway |

## 3. CSI 接口映射

| CSI 接口 | SDS 操作 | 实现说明 |
|----------|----------|----------|
| `CreateVolume` | `CreateResource + AddVolume` | 创建DRBD资源 + LVM卷 |
| `DeleteVolume` | `DeleteResource` | 删除DRBD资源和卷 |
| `ControllerPublishVolume` | `SetPrimary + MountResource` | 设置Primary并挂载 |
| `ControllerUnpublishVolume` | `UnmountResource` | 卸载卷 |
| `NodePublishVolume` | 本地mount操作 | 在节点上挂载DRBD设备 |
| `NodeUnpublishVolume` | 本地umount操作 | 在节点上卸载 |
| `CreateSnapshot` | `CreateSnapshot` | LVM快照 |
| `DeleteSnapshot` | `DeleteSnapshot` | 删除快照 |
| `ExpandVolume` | `ResizeVolume` | 扩展卷大小 |

## 4. 推荐的实现方案

### 目录结构
```
sds-csi/
├── cmd/
│   └── csi-driver/          # CSI driver 主程序
├── pkg/
│   ├── csi/
│   │   ├── identity.go      # Identity 服务
│   │   ├── controller.go    # Controller 服务
│   │   └── node.go          # Node 服务
│   ├── driver/
│   │   └── driver.go       # SDS driver 封装
│   └── volume/
│       ├── volume.go       # 卷生命周期管理
│       └── mapper.go       # Volume ID 映射
├── deploy/
│   ├── kubernetes/         # Kubernetes 部署文件
│   │   ├── csidriver.yaml
│   │   ├── rbac.yaml
│   │   └── storageclass.yaml
│   └── charts/
│       └── sds-csi/        # Helm Chart
└── api/
    └── proto/
        └── csi.proto       # CSI gRPC 定义
```

### 核心代码框架

```go
// pkg/csi/controller.go
type SDSControllerServer struct {
    driver  *SDSDriver
    client  sdsproto.SDSControllerClient
    nodeID  string
}

func (s *SDSControllerServer) CreateVolume(
    ctx context.Context,
    req *csi.CreateVolumeRequest) (*csi.CreateVolumeResponse, error) {

    volumeID := req.GetVolumeId()
    size := req.GetCapacityRange().GetRequiredBytes()

    // 调用 SDS API 创建资源
    createResp, err := s.client.CreateResource(ctx, &sdsproto.CreateResourceRequest{
        Name:     fmt.Sprintf("csi-%s", volumeID),
        Pool:     s.getStorageClass(req.GetParameters()),
        Nodes:    s.driver.GetClusterNodes(),
        VolumeSize: &sdsproto.VolumeSize{
            SizeGb: size / (1024*1024*1024),
        },
    })

    return &csi.CreateVolumeResponse{
        Volume: &csi.Volume{
            VolumeId:      volumeID,
            CapacityBytes: size,
            VolumeContext: map[string]string{
                "drbd-device": fmt.Sprintf("/dev/drbd/by-res/csi-%s/0", volumeID),
            },
        },
    }, nil
}

func (s *SDSControllerServer) ControllerPublishVolume(
    ctx context.Context,
    req *csi.ControllerPublishVolumeRequest) (*csi.ControllerPublishVolumeResponse, error) {

    volumeID := req.GetVolumeId()
    nodeID := req.GetNodeId()

    // 设置为 Primary
    _, err := s.client.SetPrimary(ctx, &sdsproto.SetPrimaryRequest{
        Resource: fmt.Sprintf("csi-%s", volumeID),
        Node:     nodeID,
    })

    return &csi.ControllerPublishVolumeResponse{}, nil
}
```

## 5. Kubernetes 部署清单

```yaml
# deploy/kubernetes/csidriver.yaml
apiVersion: storage.k8s.io/v1
kind: CSIDriver
metadata:
  name: sds.csi.linbit.com
spec:
  attachRequired: true
  podInfoOnMount: false
  volumeLifecycleModes:
  - Persistent
  storageCapacity: false
  fsGroupPolicy: File

---
apiVersion: v1
kind: ServiceAccount
metadata:
  name: csi-sds-controller

---
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRole
metadata:
  name: csi-sds-controller
rules:
- apiGroups: [""]
  resources: ["secrets"]
  verbs: ["get", "list"]
- apiGroups: ["storage.k8s.io"]
  resources: ["csistoragecapacities"]
  verbs: ["get", "list", "watch"]

---
apiVersion: apps/v1
kind: Deployment
metadata:
  name: csi-sds-controller
spec:
  replicas: 1
  selector:
    matchLabels:
      app: csi-sds-controller
  template:
    metadata:
      labels:
        app: csi-sds-controller
    spec:
      serviceAccount: csi-sds-controller
      containers:
      - name: csi-provisioner
        image: registry.k8s.io/sig-storage/csi-provisioner:v3.6.3
        args:
        - --csi-address=$(ADDRESS)
        - --leader-election
        env:
        - name: ADDRESS
          value: /run/csi/socket
        volumeMounts:
        - name: socket-dir
          mountPath: /run/csi
      - name: csi-attacher
        image: registry.k8s.io/sig-storage/csi-attacher:v4.5.1
        args:
        - --csi-address=$(ADDRESS)
        - --leader-election
        env:
        - name: ADDRESS
          value: /run/csi/socket
        volumeMounts:
        - name: socket-dir
          mountPath: /run/csi
      - name: csi-sds-driver
        image: linbit/sds-csi-driver:latest
        args:
        - --endpoint=$(CSI_ENDPOINT)
        - --nodeid=$(KUBE_NODE_NAME)
        - --controller-address=$(SDS_CONTROLLER)
        env:
        - name: CSI_ENDPOINT
          value: unix:///run/csi/socket
        - name: KUBE_NODE_NAME
          valueFrom:
            fieldRef:
              fieldPath: spec.nodeName
        - name: SDS_CONTROLLER
          value: sds-controller.default.svc.cluster.local:3374
        volumeMounts:
        - name: socket-dir
          mountPath: /run/csi
      volumes:
      - name: socket-dir
        emptyDir: {}

---
apiVersion: apps/v1
kind: DaemonSet
metadata:
  name: csi-sds-node
spec:
  selector:
    matchLabels:
      app: csi-sds-node
  template:
    metadata:
      labels:
        app: csi-sds-node
    spec:
      containers:
      - name: liveness-probe
        image: registry.k8s.io/sig-storage/livenessprobe:v2.12.0
        args:
        - --csi-address=$(ADDRESS)
        env:
        - name: ADDRESS
          value: /run/csi/socket
        volumeMounts:
        - name: socket-dir
          mountPath: /run/csi
      - name: node-driver-registrar
        image: registry.k8s.io/sig-storage/node-driver-registrar:v2.10.1
        args:
        - --csi-address=$(ADDRESS)
        - --kubelet-registration-path=/var/lib/kubelet/plugins/sds.csi.linbit.com/socket
        env:
        - name: ADDRESS
          value: /run/csi/socket
        volumeMounts:
        - name: socket-dir
          mountPath: /run/csi
        - name: registration-dir
          mountPath: /registration
      - name: csi-sds-driver
        image: linbit/sds-csi-driver:latest
        args:
        - --endpoint=$(CSI_ENDPOINT)
        - --nodeid=$(KUBE_NODE_NAME)
        env:
        - name: CSI_ENDPOINT
          value: unix:///run/csi/socket
        - name: KUBE_NODE_NAME
          valueFrom:
            fieldRef:
              fieldPath: spec.nodeName
        securityContext:
          privileged: true
        volumeMounts:
        - name: socket-dir
          mountPath: /run/csi
        - name: pods-mount-dir
          mountPath: /var/lib/kubelet/pods
          mountPropagation: Bidirectional
        - name: registration-dir
          mountPath: /registration
      volumes:
      - name: socket-dir
        hostPath:
          path: /var/lib/kubelet/plugins/sds.csi.linbit.com
          type: DirectoryOrCreate
      - name: registration-dir
        hostPath:
          path: /var/lib/kubelet/plugins_registry/
          type: DirectoryOrCreate
      - name: pods-mount-dir
        hostPath:
          path: /var/lib/kubelet/pods
          type: DirectoryOrCreate
```

## 6. StorageClass 示例

```yaml
apiVersion: storage.k8s.io/v1
kind: StorageClass
metadata:
  name: sds-ha-storage
provisioner: sds.csi.linbit.com
parameters:
  pool: "ha_pool"           # SDS 存储池名称
  replicas: "3"             # 副本数
  fsType: "ext4"           # 文件系统类型
  mountOptions: "noatime"   # 挂载选项
  enableHa: "true"         # 启用HA
volumeBindingMode: WaitForFirstConsumer
allowVolumeExpansion: true
```

## 7. PVC 使用示例

```yaml
apiVersion: v1
kind: PersistentVolumeClaim
metadata:
  name: mysql-pv-claim
spec:
  accessModes:
  - ReadWriteOnce
  storageClassName: sds-ha-storage
  resources:
    requests:
      storage: 10Gi
---
apiVersion: apps/v1
kind: StatefulSet
metadata:
  name: mysql
spec:
  serviceName: mysql
  replicas: 1
  template:
    metadata:
      labels:
        app: mysql
    spec:
      containers:
      - name: mysql
        image: mysql:8.0
        ports:
        - containerPort: 3306
        volumeMounts:
        - name: mysql-data
          mountPath: /var/lib/mysql
  volumeClaimTemplates:
  - metadata:
      name: mysql-data
    spec:
      accessModes: ["ReadWriteOnce"]
      storageClassName: sds-ha-storage
      resources:
        requests:
          storage: 10Gi
```

## 8. 实现任务清单

### Phase 1: 核心 CSI Driver
- [ ] 创建 `sds-csi-driver` 包和 CSI gRPC server
- [ ] 实现 Identity 服务 (GetPluginInfo, GetPluginCapabilities, Probe)
- [ ] 实现 Controller 服务 (CreateVolume, DeleteVolume, ControllerPublishVolume)
- [ ] 实现 Node 服务 (NodePublishVolume, NodeUnpublishVolume)
- [ ] 实现卷命名规范和 Volume ID 映射
- [ ] 处理挂载选项 (readonly, fsType 等)

### Phase 2: Kubernetes 集成
- [ ] 创建 Kubernetes Deployment manifests
- [ ] 实现 RBAC 规则
- [ ] 创建 StorageClass 参数定义
- [ ] 添加 CSI driver 到 Kubernetes node
- [ ] 测试 PVC/PV 生命周期

### Phase 3: 高级特性
- [ ] 实现卷快照 (CSI CreateSnapshot)
- [ ] 添加卷扩展支持
- [ ] 实现卷克隆
- [ ] 添加存储健康监控
- [ ] 实现卷迁移

## 9. 参考文档

- [Kubernetes CSI External Attacher](https://github.com/kubernetes-csi/external-attacher)
- [Kubernetes CSI 简介：工作流程和原理](https://rifewang.github.io/k8s-csi/)
- [CSI 驱动开发指南 - 云原生社区](https://cloudnative.jimmyong.io/blog/develop-a-csi-driver/)
- [JuiceFS CSI Driver 架构设计详解](https://juicefs.com/zh-cn/blog/engineering/juicefs-csi-driver-arch-design)
- [LINBIT Kubernetes Storage](https://linbit.com/kubernetes/)
- [LINBIT SDS for Persistent Storage](https://www.cncf.io/blog/2024/11/28/kubernetes-at-the-edge-using-linbit-sds-for-persistent-storage/)
