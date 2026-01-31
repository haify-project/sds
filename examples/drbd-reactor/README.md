# DRBD-Reactor Gateway 配置示例

本目录包含 SDS 项目的 drbd-reactor 网关配置示例，基于 linstor-gateway 的实现。

## 目录结构

```
drbd-reactor/
├── iscsi-example.toml     # iSCSI 网关配置示例
├── nfs-example.toml       # NFS 网关配置示例
├── nvmeof-example.toml    # NVMe-oF 网关配置示例
└── README.md              # 本文件
```

## 快速开始

### 1. 选择网关类型

根据你的需求选择合适的网关类型:

- **iSCSI**: 适用于需要块设备访问的场景，支持 VMware、Hyper-V 等虚拟化平台
- **NFS**: 适用于文件共享场景，简单易用
- **NVMe-oF**: 适用于高性能低延迟场景，支持现代存储协议

### 2. 复制配置文件

```bash
# iSCSI 示例
sudo cp iscsi-example.toml /etc/drbd-reactor.d/sds-iscsi-r0.toml

# NFS 示例
sudo cp nfs-example.toml /etc/drbd-reactor.d/sds-nfs-data.toml

# NVMe-oF 示例
sudo cp nvmeof-example.toml /etc/drbd-reactor.d/sds-nvmeof-database.toml
```

### 3. 修改配置

使用文本编辑器打开配置文件，修改以下占位符:

- **Service IP**: 虚拟服务 IP 地址
- **Resource Name**: DRBD 资源名称
- **IQN/NQN**: iSCSI 或 NVMe 限定名称
- **Device Paths**: DRBD 设备路径 (/dev/drbd0, /dev/drbd1, ...)
- **Mount Points**: 挂载点路径
- **Export Paths**: NFS 导出路径

### 4. 重新加载 drbd-reactor

```bash
# 方法 1: 使用 systemctl
sudo systemctl reload drbd-reactor

# 方法 2: 使用信号
sudo killall -HUP drbd-reactor

# 方法 3: 重启服务 (如果重载不生效)
sudo systemctl restart drbd-reactor
```

### 5. 验证配置

```bash
# 查看 drbd-reactor 日志
sudo journalctl -u drbd-reactor -f

# 检查资源状态
sudo drbd-reactor status

# 检查 DRBD 状态
sudo drbdadm status
```

## 配置详解

### iSCSI 网关 (iscsi-example.toml)

**用途**: 导出 DRBD 卷为 iSCSI LUN，供 initiators 连接

**关键组件**:
1. **Cluster Private Volume**: 存储集群状态和 tickle 文件
2. **Portblock (Block)**: 在非活跃节点上阻止端口
3. **IPaddr2**: 配置虚拟服务 IP
4. **iSCSITarget**: 配置 iSCSI Target
5. **iSCSILogicalUnit**: 导出 LUN
6. **Portblock (Unblock)**: 在活跃节点上解除端口阻止

**默认端口**: 3260

**适用场景**:
- 虚拟化平台 (VMware, Hyper-V, KVM)
- 需要块设备访问的应用
- 需要多路径冗余的场景

### NFS 网关 (nfs-example.toml)

**用途**: 导出 DRBD 卷为 NFS 共享，供客户端挂载

**关键组件**:
1. **Portblock (Block)**: 在非活跃节点上阻止端口
2. **Cluster Private Volume**: 存储 NFS 服务器状态
3. **Filesystem**: 挂载数据卷
4. **IPaddr2**: 配置虚拟服务 IP
5. **nfsserver**: 启动 NFS 服务器
6. **exportfs**: 导出文件系统
7. **Portblock (Unblock)**: 在活跃节点上解除端口阻止

**默认端口**: 2049

**适用场景**:
- 文件共享
- Web 服务器内容共享
- 备份存储

**限制**: 集群中只能有一个 NFS 网关资源

### NVMe-oF 网关 (nvmeof-example.toml)

**用途**: 导出 DRBD 卷为 NVMe-oF 命名空间，供主机连接

**关键组件**:
1. **Portblock (Block)**: 在非活跃节点上阻止端口
2. **Cluster Private Volume**: 存储 NVMe-oF 状态
3. **IPaddr2**: 配置虚拟服务 IP
4. **nvmet-subsystem**: 创建 NVMe 子系统
5. **nvmet-namespace**: 创建命名空间
6. **nvmet-port**: 创建监听端口
7. **Portblock (Unblock)**: 在活跃节点上解除端口阻止

**默认端口**: 4420

**适用场景**:
- 高性能数据库
- 低延迟存储需求
- 现代 NVMe 协议支持

## 命名规范

### Resource Name (资源名称)

- 使用小写字母、数字和连字符
- 示例: `database-vms`, `backup-data`, `web-content`

### IQN (iSCSI Qualified Name)

格式: `iqn.<year>-<month>.<domain-reversed>:<unique-id>`

示例:
```
iqn.2024-01.com.example:storage.target1
iqn.2024-01.org.linbit:sds.vm-disks
```

### NQN (NVMe Qualified Name)

格式: `nqn.<year>-<month>.<domain-reversed>:<unique-id>`

示例:
```
nqn.2024-01.com.example:subsystem.database
nqn.2024-01.org.linbit:sds.nvme.data
```

### UUID 生成

用于 NVMe-oF namespaces 和 NFS exports:

```bash
# 方法 1: 使用 uuidgen
uuidgen

# 方法 2: 使用 SHA256 哈希
echo -n "your-unique-string" | sha256sum | cut -c1-36 | \
  sed 's/\(.\{8\}\)\(.\{4\}\)\(.\{4\}\)\(.\{4\}\)\(.\{12\}\)/\1-\2-\3-\4-\5/'
```

## 常见问题

### 1. 端口已被占用

**错误**: `ERROR: ocf:heartbeat:portblock - unable to block port`

**解决**:
```bash
# 检查端口占用
sudo ss -tlnp | grep 3260

# 停止冲突的服务
sudo systemctl stop tgt
sudo systemctl stop iscsid
sudo systemctl stop nfs-server
```

### 2. IP 地址已存在

**错误**: `ERROR: ocf:heartbeat:IPaddr2 - IP address already in use`

**解决**:
```bash
# 检查 IP 是否存在
sudo ip addr show | grep 192.168.1.100

# 或使用 arping
arping -c 1 192.168.1.100
```

### 3. 设备不存在

**错误**: `ERROR: ocf:heartbeat:Filesystem - device /dev/drbd1 not found`

**解决**:
```bash
# 检查 DRBD 状态
sudo drbdadm status

# 启动 DRBD 资源
sudo drbdadm up r0
```

### 4. 配置语法错误

**错误**: `ERROR: failed to parse config file`

**解决**:
```bash
# 检查 TOML 语法
cat /etc/drbd-reactor.d/sds-iscsi-r0.toml | tomlq

# 或使用 drbd-reactor 检查
sudo drbd-reactor --check-config
```

## 性能优化

### iSCSI 多路径配置

使用多个 Service IP 实现多路径:

```toml
# 配置两个 Service IP
"ocf:heartbeat:portblock pblock0 ip=192.168.1.100 portno=3260 action=block protocol=tcp",
"ocf:heartbeat:portblock pblock1 ip=192.168.1.101 portno=3260 action=block protocol=tcp",

"ocf:heartbeat:IPaddr2 service_ip0 ip=192.168.1.100 cidr_netmask=24",
"ocf:heartbeat:IPaddr2 service_ip1 ip=192.168.1.101 cidr_netmask=24",

"ocf:heartbeat:iSCSITarget target iqn=... portals=192.168.1.100:3260 192.168.1.101:3260 ..."
```

### NFS 客户端优化

在 NFS 客户端挂载时使用优化选项:

```bash
# 增加读写块大小
sudo mount -t nfs -o rw,sync,hard,rsize=1048576,wsize=1048576 192.168.1.200:/srv/gateway-exports/nfs-data/data /mnt/data

# 使用 NFSv4
sudo mount -t nfs4 -o rw,sync,hard 192.168.1.200:/srv/gateway-exports/nfs-data/data /mnt/data
```

### NVMe-oF 使用 RDMA

如果硬件支持 RDMA，修改传输类型:

```toml
"ocf:heartbeat:nvmet-port port nqns=... addr=192.168.1.150 type=rdma",
```

## 安全配置

### iSCSI CHAP 认证

```toml
# 启用 CHAP 认证
"ocf:heartbeat:iSCSITarget target iqn=iqn.2024-01.com.example:storage.r0 portals=192.168.1.100:3260 incoming_username=admin incoming_password=secretpass allowed_initiators= implementation=lio"
```

### NFS 访问控制

```toml
# 限制特定网络访问
"ocf:heartbeat:exportfs export_1_0 directory=/srv/gateway-exports/nfs-data/data fsid=... clientspec=192.168.1.0/24 options=rw,no_root_squash",

# 只读导出
"ocf:heartbeat:exportfs export_2_0 directory=/srv/gateway-exports/nfs-data/backup fsid=... clientspec=0.0.0.0/0.0.0.0 options=ro"
```

## 监控和日志

### 查看 drbd-reactor 日志

```bash
# 实时查看
sudo journalctl -u drbd-reactor -f

# 查看最近 100 行
sudo journalctl -u drbd-reactor -n 100

# 只看错误
sudo journalctl -u drbd-reactor -p err
```

### 检查 DRBD 状态

```bash
# 完整状态
sudo drbdadm status

# 详细状态
sudo drbdadm status -v

# 查看连接状态
sudo drbdadm cstatus
```

### 监控资源代理

```bash
# 使用 Pacemaker/CRM
sudo crm_mon -1

# 查看特定资源
sudo crm_resource --resource r0 --locate
```

## 自动重载配置

配置自动重载路径单元 (推荐):

```bash
# 复制自动重载配置
sudo cp /usr/share/doc/drbd-reactor/examples/drbd-reactor-reload.path /etc/systemd/system/
sudo cp /usr/share/doc/drbd-reactor/examples/drbd-reactor-reload.service /etc/systemd/system/

# 或创建手动创建 (见主文档)

# 启用并启动
sudo systemctl daemon-reload
sudo systemctl enable --now drbd-reactor-reload.path
```

## 与 SDS 集成

这些配置文件应该由 SDS Controller 自动生成。当使用 SDS CLI 创建网关时:

```bash
# 创建 iSCSI 网关
sds-cli gateway iscsi create \
  --resource r0 \
  --iqn iqn.2024-01.com.example:storage.r0 \
  --service-ip 192.168.1.100/24 \
  --luns 1,2

# 创建 NFS 网关
sds-cli gateway nfs create \
  --resource nfs-data \
  --service-ip 192.168.1.200/24 \
  --exports /data,/backup

# 创建 NVMe-oF 网关
sds-cli gateway nvme create \
  --resource database \
  --nqn nqn.2024-01.com.example:subsystem.database \
  --service-ip 192.168.1.150/24 \
  --namespaces 1,2
```

SDS Controller 会:
1. 生成适当的 drbd-reactor 配置
2. 写入 `/etc/drbd-reactor.d/sds-<type>-<resource>.toml`
3. 自动重新加载 drbd-reactor
4. 验证配置生效

## 参考文档

完整的配置说明和高级选项，请参考:

- [DRBD-Reactor Gateway 配置完整文档](../../docs/drbd-reactor-gateway-examples.md)
- [LINSTOR Gateway 文档](https://linbit.com/drbd-user-guide/linstor-gateway-docs/)
- [DRBD Reactor GitHub](https://github.com/LINBIT/drbd-reactor)
- [OCF 资源代理](https://github.com/ClusterLabs/resource-agents)

## 版本

- **文档版本**: 1.0
- **更新日期**: 2024-01-04
- **兼容性**: drbd-reactor 1.2+, SDS 1.0+
