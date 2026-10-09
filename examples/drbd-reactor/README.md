# drbd-reactor 网关配置

Haify 的 NFS / iSCSI / NVMe-oF 网关就是一个 DRBD 资源加一份 drbd-reactor promoter 配置。
配置由 controller 生成并下发，**不需要手写**；本文说明它生成的是什么，便于排障。

节点上 `/etc/drbd-reactor.d/` 里的实际文件就是权威样例。

## 文件与下发

| 网关 | 创建命令 | 配置文件 |
| ---- | -------- | -------- |
| NFS | `sds gateway nfs create` | `/etc/drbd-reactor.d/sds-nfs-<resource>.toml` |
| iSCSI | `sds gateway iscsi create` | `/etc/drbd-reactor.d/sds-iscsi-<resource>.toml` |
| NVMe-oF | `sds gateway nvme create` | `/etc/drbd-reactor.d/sds-nvmeof-<resource>.toml` |

（`sds ha create` 生成的通用 HA 配置是 `sds-ha-<resource>.toml`，不属于网关。）

- 配置只写到该资源的 **diskful 副本节点**；其他受管节点（包括 tiebreaker）上的同名配置会被移除。
  diskless 节点也能被 DRBD 9 提升为 Primary，若它也有 promoter，就会在没有数据副本的机器上跑整条网关链。
- 写完后在这些节点上执行 `systemctl reload drbd-reactor`（失败则 restart）。
- `sds gateway stop --resource <r>` 先把配置改名为 `.toml.disabled` 并 reload，再停
  `drbd-services@<r>.target`。只停 target 不够：reactor 几秒内就会重新提升并拉起整条链。
  `gateway start` 把文件改回 `.toml` 并 reload。
- `sds gateway delete --resource <r>` 先执行 stop，再在所有受管节点上删除 `.toml`、`.toml.disabled`、`.toml.pending` 并 reload。
- 增删 LUN / initiator / CHAP / namespace / host / NFS export 是对这份配置的读-改-写：从资源的
  diskful 节点读取（controller 本机的 `/etc/drbd-reactor.d` 不参与），任一节点有 `.toml` 就以它为准，
  只有 `.toml.disabled` 时网关处于停止状态，改动写回 `.toml.disabled`、不会顺带启动网关。
  各节点内容不一致时取多数节点持有的那份（平局取节点名最小者），日志 warning 列出不一致的节点；
  随后的写入会让所有 diskful 节点重新一致。

### 编辑运行中的网关：何时生效

drbd-reactor 无法把改动过的 promoter 配置应用到正在运行的网关上（src/plugin.rs `start_from_config`）：
reload 时配置没变的 plugin 原样保留，变了的就把旧 plugin 停掉、再按新配置起一个新的。Haify 生成的配置都带
`stop-services-on-exit = true`，所以停旧 plugin 会 `systemctl stop drbd-services@<r>.target`——整条链
（文件系统、target、LUN、service IP）停掉并降级，各节点再重新抢提升（实测：一次 initiator 修改触发
切换，最后没有任何节点是 Primary）。reactor 也不监视目录：snippet monitor 只在日志里提示
"reload required"，不会自己 reload。

因此编辑运行中的网关时：

- **运行网关的节点**（`drbd-services@<r>.target` 为 active 的那个）：**不 reload drbd-reactor**。
  `.toml` 保持 reactor 已加载的版本（之后任何原因的 reload 都不会动这个网关），新配置写成
  `sds-<type>-<r>.toml.pending`（reactor 只读 `*.toml`）。同时：
  - 把 reactor 按新配置会生成的 drop-in（`/run/systemd/system/ocf.rs@<agent>_<r>.service.d/reactor.conf`
    和 `drbd-services@<r>.target.d/reactor.conf`）原样写好并 `daemon-reload`，这样单元被 systemd 重启
    或日后切回本节点时用的都是新链；target 的 drop-in 保留原 mtime，免得 snippet monitor 提示 reload。
  - 被删掉的单元单独 `systemctl stop`（新 drop-in 里已没有谁 Requires 它，不会连带停别的）；新增的单元
    （LUN、namespace、export）单独 `systemctl start`。
  - 参数变更直接作用在运行中的 target 上，命令与 OCF agent 启动时相同：iSCSI initiator/CHAP 用
    `targetcli`（先建 ACL 再关 demo mode、最后删 ACL；删 ACL 会断开该 initiator 的会话），NVMe host 用
    configfs 的 `allowed_hosts` 软链（已建立的连接不会被断开，重连时才校验）。无法在线应用的改动
    （如去掉 CHAP、改 portal）在写任何东西之前就拒绝。
  - 在 `drbd-reactor.service` 上装一个 drop-in（`50-sds-pending-gateway-config.conf`，`ExecStartPre`），
    reactor 下次启动（重启/开机）前把 `.toml.pending` 改名为 `.toml`。
- **其他 diskful 节点**：写 `.toml`、删掉残留的 `.pending`、reload drbd-reactor。网关不在这些节点上运行，
  新 plugin 看到 Primary 在别处就不会去启动（`try_initial_target_start`），reload 只是重新生成单元，
  故障转移时直接用新配置。
- **停止的网关**（只有 `.toml.disabled`）：只改 `.toml.disabled`，不碰任何运行状态，`gateway start` 时生效。
- **启用但没有节点在跑**：照旧在所有 diskful 节点写 `.toml` 并 reload。
- 状态处于 activating/deactivating、或有节点没应答时拒绝编辑，什么都不写。

`gateway stop` 会把 `.toml.pending` 变成 `.toml.disabled`；`gateway delete` 和移除 promoter 时一并删除。
**不要为了"让改动生效"在运行节点上手动 `systemctl reload drbd-reactor`**——那会重启整个网关。
在线应用失败时命令会报错：配置已在所有节点保存、故障转移会用它；要立刻生效就 `sds gateway stop` 再
`sds gateway start`（会中断客户端）。

## 创建前的检查

创建时 controller 在资源的每个 diskful 节点上检查 OCF agent 和工具，缺了直接报错（列出缺什么、装哪个包），不写配置：

| 网关 | OCF agent（`ocf:heartbeat:`） | 工具 | 安装 |
| ---- | ---------------------------- | ---- | ---- |
| NFS | Filesystem, IPaddr2, nfsserver, exportfs | rpc.nfsd, exportfs | `nfs-kernel-server`（Debian/Ubuntu）/ `nfs-utils`（EL） |
| iSCSI | Filesystem, IPaddr2, iSCSITarget, iSCSILogicalUnit | targetcli | `targetcli-fb`（Debian/Ubuntu）/ `targetcli`（EL） |
| NVMe-oF | Filesystem, IPaddr2, nvmet-subsystem, nvmet-namespace, nvmet-port | | |

OCF agent 来自 `resource-agents-extra`（Debian/Ubuntu）或 `resource-agents`（EL）。
NVMe-oF 随后加载 `nvmet` 和传输对应的模块（`nvmet-tcp` 或 `nvmet-rdma`），追加到
`/etc/modules-load.d/nvmet.conf`；`rdma` 还要求 `/sys/class/infiniband` 下有 RDMA 设备。

随后在资源自己的节点上提升一次，给 cluster-private 卷（NFS 还有导出卷）做 `mkfs`
（仅当 `blkid` 查不到文件系统时），因为 Filesystem agent 只挂载不格式化。

## cluster-private 卷

每个网关需要一个小的状态卷，挂在 `/var/lib/sds-gateway/<resource>`（NFS 的
`nfs_shared_infodir` 是其下的 `nfs/`）。资源只有一个卷时，controller 自动追加名为
`<resource>_state<N>` 的卷，由 `controller.toml` 控制：

```toml
[gateway]
auto_state_volume = true     # 默认
state_volume_size_gb = 1     # 默认
```

哪个卷是状态卷按**卷名**（`_state` 后缀）判断，不按卷号：`resource create --size`
把用户数据放在卷 0（`<resource>_data`），状态卷是后追加的。没有 `_state` 卷的资源按
linstor 布局处理（卷 0 是状态卷，其余是数据）。

旧版本把状态卷挂在 `/var/lib/sds/<resource>`，会被 controller Self-HA 的挂载点遮住。
对已停止的网关执行 `gateway start` 时会自动改到新路径。

## 生成的配置

公共部分：

```toml
[[promoter]]

  [promoter.metadata]
    linstor-gateway-schema-version = 1

  [promoter.resources]

    [promoter.resources.<resource>]
      on-drbd-demote-failure = "reboot-immediate"
      runner = "systemd"
      stop-services-on-exit = true
      target-as = "BindsTo"      # NFS；iSCSI 和 NVMe-oF 为 "Requires"
      start = [ ... ]
```

drbd-reactor 按 `start` 顺序启动，**逆序**停止。不再使用 portblock/portunblock：
故障切换后 unblock 不能可靠清掉新主节点上的 DROP 规则，会把客户端挡在端口外。

### NFS

```toml
start = [
  "ocf:heartbeat:Filesystem fs_cluster_private device=<状态卷> directory=/var/lib/sds-gateway/data fstype=ext4 run_fsck=no",
  "ocf:heartbeat:Filesystem fs_export device=<数据卷> directory=/data fstype=ext4 run_fsck=no",
  "ocf:heartbeat:nfsserver nfsserver nfs_ip=192.168.1.200 nfs_shared_infodir=/var/lib/sds-gateway/data/nfs nfs_server_scope=192.168.1.200",
  "ocf:heartbeat:exportfs export_0 directory=/data fsid=<uuid> clientspec=0.0.0.0/0.0.0.0 options=rw,all_squash,anonuid=0,anongid=0",
  "ocf:heartbeat:IPaddr2 service_ip ip=192.168.1.200 cidr_netmask=24",
]
```

- `--export-path`：绝对路径原样使用（拒绝 `/`、`/etc`、`/usr`、`/var/lib/sds` 等系统目录）；
  相对路径放在 `/srv/gateway-exports/<resource>/` 下；不给时就是 `/srv/gateway-exports/<resource>`。
- `--allowed-ips` 每项生成一行 `exportfs`（`export_0`、`export_1`…）；不给时 `0.0.0.0/0.0.0.0`。
- `--fs-type` 默认 `ext4`。
- service IP 放在最后，停止时最先摘掉：客户端只看到服务器不响应并重试，而不是在
  unexport 与摘 IP 之间被拒绝。因此 nfsserver 启动时 IP 尚未存在，controller 在 NFS 节点上设置
  `net.ipv4.ip_nonlocal_bind=1`（`/etc/sysctl.d/90-sds-nfs-gateway.conf`），让 sm-notify
  能绑定 service IP；并给 `fsidd`、`nfsdcld` 加 `PartOf=nfs-server.service`，否则它们占住
  `/var/lib/nfs`，umount 失败，网关切走后切不回来。
- 后续增删导出：`sds gateway nfs export add|list|remove`。

### iSCSI

```toml
start = [
  "ocf:heartbeat:Filesystem fs_cluster_private device=<状态卷> directory=/var/lib/sds-gateway/r0 fstype=ext4 run_fsck=no",
  "ocf:heartbeat:iSCSITarget target iqn=iqn.2024-01.com.example:sds.r0 portals=192.168.1.100:3260 allowed_initiators= implementation=lio-t",
  "ocf:heartbeat:iSCSILogicalUnit lu1 target_iqn=iqn.2024-01.com.example:sds.r0 lun=1 path=<数据卷> product_id=<serial> scsi_sn=<serial> implementation=lio-t",
  "ocf:heartbeat:IPaddr2 service_ip0 ip=192.168.1.100 cidr_netmask=24",
]
```

- 每个数据卷一个 LUN，从 1 编号（与 DRBD 卷号无关）。`serial` 是 `md5("<IQN>-<LUN>")`
  前 8 字节的十六进制，只由 IQN 和 LUN 号决定，切换后不变。
- `--implementation lio`（默认）写成 `lio-t`（targetcli），target 和每个 LUN 行都写明，
  否则 iSCSILogicalUnit 会自行选择（ietadm、tgtadm 优先于 targetcli）。只支持 LIO：
  `tgt`、`iet` 下 iSCSITarget 忽略 `portals`、不绑定 service IP，tgt 还需要 promoter
  链不会启动的 tgtd，因此创建时直接拒绝。
- 只有同时给了 `--username` 和 `--password` 才写 `incoming_username=… incoming_password=…`。
- `--allowed-initiators` 以空格连接写入 `allowed_initiators`；为空表示不限制。
- service IP 放在最后：否则停止时 LUN 先被删而 portal 仍可达，initiator 收到
  "LUN not supported" 硬错误，客户端文件系统变只读。
- 后续管理：`sds gateway iscsi lun|initiator|chap ...`。

### NVMe-oF

```toml
start = [
  "ocf:heartbeat:Filesystem fs_cluster_private device=<状态卷> directory=/var/lib/sds-gateway/db fstype=ext4 run_fsck=no",
  "ocf:heartbeat:IPaddr2 service_ip ip=192.168.1.150 cidr_netmask=24",
  "ocf:heartbeat:nvmet-subsystem subsys nqn=nqn.2024-01.com.example:sds.db serial=<serial>",
  "ocf:heartbeat:nvmet-namespace ns_1 nqn=nqn.2024-01.com.example:sds.db namespace_id=1 backing_path=<数据卷> uuid=<uuid> nguid=<uuid>",
  "ocf:heartbeat:nvmet-port port nqns=nqn.2024-01.com.example:sds.db addr=192.168.1.150 type=tcp",
]
```

- `serial` 是 `sha256(NQN)` 前 8 字节的十六进制。namespace 从 1 编号；`uuid`/`nguid`
  在创建时随机生成一次，之后只从配置文件读回。
- `--transport`：`tcp`（默认）或 `rdma`，写入 `type=`。监听端口 4420。
- 后续管理：`sds gateway nvme namespace|host ...`。

## 排障

```bash
drbd-reactorctl status                              # promoter 与各 OCF 单元状态
systemctl status drbd-services@<resource>.target
journalctl -u drbd-reactor -n 100
drbdadm status <resource>
sds gateway status --resource <resource>
```

资源名含 `-` 时 systemd 单元名里写作 `\x2d`，例如 `drbd-services@nfs\x2ddata.target`。

## 参考

- [drbd-reactor](https://github.com/LINBIT/drbd-reactor)
- [OCF resource agents](https://github.com/ClusterLabs/resource-agents)
