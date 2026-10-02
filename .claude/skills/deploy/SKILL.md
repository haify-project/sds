---
name: deploy and test SDS
description: Guide for deploying and testing SDS in a local development and test environment
---

# SDS 部署与测试指南

本文档介绍如何在本地开发环境中构建 SDS，并将其部署到测试环境（`orange1`, `orange2`, `orange3`）进行验证。

## 环境前提

- **本地开发机**: Go 1.26+（`go.mod` 要求）、Node.js/npm（构建内嵌 Web UI）、Make。只有改 `sds.proto` 后跑 `make proto` 才需要 protoc。
- **测试节点**: `orange1`, `orange2`, `orange3`。
- **SSH 配置**: 本地到测试节点、以及测试节点之间（特别是 `orange1` 到其他节点）的 SSH 免密登录已配置完成。
- **存储设备**: 所有节点上均已准备好闲置的 `/dev/sdb` 用于测试。

## 控制器端口

| 端口 | 用途 |
|------|------|
| 3374 | gRPC API（sds、CSI、sds-mcp） |
| 3375 | REST API（gRPC-gateway，HTTP/JSON） |
| 3376 | Web UI（内嵌 SPA） |
| 9433 | Prometheus 指标 |

以上是默认值。gRPC 端口由 `[server] port`、UI 由 `[ui] port`、指标由 `[metrics] port` 决定，以节点上的 `/etc/sds/controller.toml` 为准；REST 由 `[server] rest_port` 决定（默认 3375）。

**API 认证（可选）**: 在 `/etc/sds/controller.toml` 中设置 `[auth] enabled = true` 和 `token`（至少 16 字符）后，gRPC 和 REST 均需要 Bearer Token。sds 按以下顺序解析 token：`--token` 参数 → `SDS_TOKEN` 环境变量 → `~/.sds/token` → `/etc/sds/token`。REST 请求需带 `Authorization: Bearer <token>` 头。

**RBAC（可选，比单 token 更细）**: 在 `controller.toml` 设置 `[rbac] enabled = true` 并声明 `[[rbac.users]]`（name/token/role，role = admin/operator/viewer，token 至少 16 字符）后，`[auth]` 的单 token 不再使用，改用**每用户 token**。`controller.toml` 在各节点的根分区上、不随 sds-meta 复制，开启 self-HA 时要写到**所有可接管节点**。改完重启 active 节点。用户/角色之后可在 Web UI 的 **Access** 页或 `sds rbac user add|remove|set-role` 管理，持久化进 `sds.db`。

## 1. 构建与部署

```bash
# 默认部署到 orange1
./scripts/deploy-all.sh

# 部署到所有测试节点（参数是位置参数，逗号分隔）
./scripts/deploy-all.sh orange1,orange2,orange3

# 或者直接使用 deploy.sh
./scripts/deploy.sh --hosts orange1 --build
./scripts/deploy.sh --hosts orange2,orange3 --cli-only
```

`deploy-all.sh` 等于 `deploy.sh --hosts <hosts> --build`，它会：

1. 以 `GOOS=linux GOARCH=amd64 CGO_ENABLED=0 make build` 交叉编译（`TARGET_OS`/`TARGET_ARCH` 环境变量或 `--target-os`/`--target-arch` 可改）。`make build` 每次都会先 `npm run build` 并把 `web-ui/dist` 强制同步到 `ui/dist`，内嵌的 UI 总是最新的。
2. 把 `sds-controller` 装到 `/opt/sds/bin/`，`sds` 装到 `/usr/local/bin/`，安装 `sds-controller.service`；`/etc/sds/controller.toml` 不存在时才从 `configs/controller.toml.example` 拷一份。
3. 节点上有 drbd-reactor 时启用 `drbd-reactor-reload.path`（配置变更自动 reload）。
4. 重启控制器：节点上存在 `sds-meta`（self-HA）时只重启 sds-meta 为 Primary 的节点；否则在每台主机上 `systemctl enable` + `restart`。

它**不**部署 `sds-mcp`、`service-ip` 和 CSI 镜像。

### 手动部署（不用脚本时）

`make build` 不带 `GOOS`/`GOARCH` 时只编译**本机架构**，Mac 上编出来的二进制在节点上不能执行：

```bash
GOOS=linux GOARCH=amd64 CGO_ENABLED=0 make build
```

`make ui-ensure` 只在 `ui/dist/index.html` 不存在时才同步，改了 web-ui 后不会更新；不要用它代替 `make build` 来准备 UI。若绕过 make 直接 `go build`，先手动同步：

```bash
npm --prefix web-ui run build
rm -rf ui/dist && cp -r web-ui/dist ui/ && touch ui/dist/.gitkeep
GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -o bin/sds-controller ./cmd/controller
```

**先确认节点上的 unit 跑的是哪个路径**：`systemctl cat sds-controller | grep ExecStart`。仓库里的 unit 是 `/opt/sds/bin/sds-controller`，但已部署的集群不一定一致，写错路径等于什么都没部署。

### 控制器 self-HA 集群的部署

self-HA 下 `sds-controller` 由 drbd-reactor 托管：**只在 active（`sds-meta` 为 Primary 的）节点运行**，standby 节点的服务是 `inactive` 且未 enable。所以**不能**在每个节点 `systemctl enable/start`（standby 上会因 DRBD 依赖失败）。做法是**所有节点都换二进制，只重启 active 节点**（`deploy.sh` 自动这样做）：

```bash
# 0) 交叉编译（见上）

# 1) 找出 active 节点(sds-meta Primary)
for h in orange1 orange2 orange3; do echo -n "$h: "; ssh $h "drbdadm role sds-meta 2>/dev/null"; done

# 2) 把新二进制推到所有节点(原地替换,不动 standby 的服务状态)
for h in orange1 orange2 orange3; do
  scp -q bin/sds-controller bin/sds $h:/tmp/
  ssh $h 'sudo install -m755 /tmp/sds-controller /opt/sds/bin/sds-controller && \
          sudo install -m755 /tmp/sds /usr/local/bin/sds && rm -f /tmp/sds-controller /tmp/sds'
done

# 3) 只重启 active 节点(假设是 orange3)
ssh orange3 'sudo systemctl restart sds-controller && sleep 3; systemctl is-active sds-controller'

# 4) 确认控制器落在哪个节点
for h in orange1 orange2 orange3; do echo -n "$h: "; ssh $h "drbdadm role sds-meta 2>/dev/null"; done
```

`sds-controller.service` 是 sds-meta promoter 启动链的一员，`systemctl restart` 可能把整组服务拆掉，由 reactor 在另一个节点上拉起（openclaw 集群上发生过）。这是 self-HA 正常行为，但重启后要用第 4 步确认 active 节点，而不是假定它还在原地。要做计划内切换，用 `sds ha evict sds-meta`。standby 节点拿到新二进制后，将来故障转移过去时自然就是新版本。

### 访问地址(self-HA VIP)

self-HA 的**浮动 VIP**（`ha self enable --vip` 指定）始终跟随 active 节点，用 VIP 访问最稳：

- Web UI: `http://<VIP>:3376/`
- REST: `http://<VIP>:3375`
- gRPC(CLI): `sds -c <VIP>:3374`

**验证节点状态：**

```bash
ssh orange1 "sds node list"
ssh orange1 "sds health-check"
```

## 2. 准备存储 (Pool)

使用准备好的 `/dev/sdb` 创建存储池。`--type` 支持：`lvm`、`lvm-thin`、`zfs`；省略时取控制器 `[storage] default_pool_type`（默认 thin pool）。

### 2.1 创建 LVM Pool

```bash
# --nodes 支持逗号分隔的多个节点，一条命令即可
ssh orange1 "sds pool create --name data-pool --type lvm --nodes orange1,orange2 --devices /dev/sdb"
```

### 2.2 创建 LVM Thin Pool

```bash
# --size 指定 VG 内 thin pool 的大小
ssh orange1 "sds pool create --name thin-pool --type lvm-thin --nodes orange1,orange2 --devices /dev/sdb --size 10G"
```

### 2.3 创建 ZFS Pool

```bash
ssh orange1 "sds pool create --name tank --type zfs --nodes orange1,orange2 --devices /dev/sdb"
```

## 3. 测试资源创建 (Resource)

> **端口**: `--port` 和 `--size` 必填；每个 DRBD 资源的 `--port` 必须全局唯一。`resource create` 会预检端口，撞了报 `DRBD port N is already in use by resource "X"; choose a different port`。已用端口看 `/etc/drbd.d/*.res`。
>
> **改 DRBD options**: 不用重建资源，用 `sds resource set-options <res> --drbd-options 'net/max-buffers=8000,on-no-quorum=suspend-io,disk/on-io-error=detach'`（就地改 `.res` + `drbdadm adjust`），或 Web UI 资源页的 **Edit DRBD Options**。格式 `section/key=value`，裸 key 进 options 段。

### 3.1 创建 LVM 资源

```bash
# 创建资源 res01（--pool 默认 data-pool；pool 里有 thin pool 时自动建 thin LV）
ssh orange1 "sds resource create --name res01 --port 7001 --size 1G --nodes orange1,orange2 --pool data-pool"

# 启用并挂载
ssh orange1 "sds resource primary res01 orange1 --force"
ssh orange1 "sds resource fs res01 0 ext4 --node orange1"
ssh orange1 "sds resource mount res01 0 /mnt/res01 --node orange1"

# 收回
ssh orange1 "sds resource unmount res01 0 --node orange1"
ssh orange1 "sds resource secondary res01 orange1"
```

### 3.2 创建带自定义选项的资源

```bash
ssh orange1 "sds resource create --name res-opt --port 7002 --size 1G --nodes orange1,orange2 --pool data-pool \
  --drbd-options 'options/on-no-quorum=suspend-io,net/max-buffers=8000,disk/on-io-error=detach'"
```

### 3.3 创建 ZFS 资源

```bash
ssh orange1 "sds resource create --name res-zfs --port 7003 --size 1G --nodes orange1,orange2 --pool tank --storage-type zfs"
```

## 4. 快照测试 (Snapshot)

LVM：thin LV 建 thin 快照；厚 LV 建 COW 快照，`--size`（默认 1G）是 COW 预留，restore 用 `lvconvert --merge`。ZFS 用原生 snapshot/rollback。`--pool` 默认取资源自己的 pool。

restore 前资源在任何节点上都不能是 Primary（先 unmount + secondary，见 3.1）；restore 会把资源 down 掉，回滚后让其他副本从该节点重新同步。

```bash
ssh orange1 "sds resource snapshot create --resource res01 --name snap1 --node orange1"
ssh orange1 "sds resource snapshot list --resource res01 --node orange1"
ssh orange1 "sds resource snapshot restore --resource res01 --name snap1 --node orange1"
ssh orange1 "sds resource snapshot delete --resource res01 --name snap1 --node orange1"

# ZFS 快照：追加 --storage-type zfs
```

## 5. 网关测试 (Gateway)

> 网关需要资源有 **≥2 个卷**：一个小的 cluster-private state 卷加数据卷。`[gateway] auto_state_volume`（默认开）会在建网关时自动补 state 卷（大小 `state_volume_size_gb`，默认 1G），所以单卷资源也能直接建网关。网关的 Filesystem agent 自己挂载，卷上没有文件系统时建网关会先 `mkfs.ext4`；不要事先手动 mount 该资源。

```bash
# 用一个没手动挂载的资源（这里新建 res-nfs）
ssh orange1 "sds resource create --name res-nfs --port 7004 --size 1G --nodes orange1,orange2 --pool data-pool"

# 创建 NFS 网关（export 路径就是网关的挂载点；/etc、/var/lib/sds 等系统目录会被拒绝）
ssh orange1 "sds gateway nfs create --resource res-nfs --service-ip <service-ip>/24 --export-path /srv/res-nfs"

# 网关生命周期管理
ssh orange1 "sds gateway status --resource res-nfs"
ssh orange1 "sds gateway stop --resource res-nfs"
ssh orange1 "sds gateway start --resource res-nfs"
ssh orange1 "sds gateway delete --resource res-nfs"
```

`--service-ip` 要用一个空闲地址，不能和 self-HA VIP 重复。

**注意**: 网关配置里已不再使用 portblock OCF agent（故障转移后会在新 active 节点上残留 DROP 规则）；NFS 和 iSCSI 的服务 IP 排在启动链最后、停止时最先撤掉。建网关和删网关时仍会清理旧版本残留的 portblock 规则；若客户端连接异常，可用 `sudo iptables -L -n | grep -E '2049|3260|4420'` 检查。

## 6. 控制器自身 HA (可选)

```bash
# 启用：创建 sds-meta DRBD 资源（默认端口 7999、1G）存放数据库，控制器分发到所有节点并由 drbd-reactor 托管
ssh orange1 "sds ha self enable --vip <VIP>/24 --pool data-pool"

# 之后通过 VIP 访问（enable 过程中控制器会重启，原连接断开属正常）
ssh orange1 "sds --controller <VIP>:3374 ha self status"

# 恢复单机模式（sds-meta 资源保留，之后可用 resource delete sds-meta 删除）
ssh orange1 "sds --controller <VIP>:3374 ha self disable --node orange1"
```

## 7. REST API 与 Web UI

```bash
# REST API（端口 3375）
curl -s http://orange1:3375/v1/pools | jq
curl -s http://orange1:3375/v1/resources | jq

# 启用认证后
curl -s -H "Authorization: Bearer $(cat ~/.sds/token)" http://orange1:3375/v1/pools | jq

# Web UI（端口 3376）
# 浏览器访问 http://orange1:3376/
```

## 8. 故障排查

- **控制器日志**: `ssh orange1 "journalctl -u sds-controller -f"`
- **节点健康检查**: `ssh orange1 "sds health-check"`
- **DRBD 状态**: `ssh orange1 "sds resource status res01"`（优先于直接 `drbdadm status`）
- **控制器实际在跑哪个二进制**: `readlink /proc/$(systemctl show -p MainPID --value sds-controller)/exe`（带 `(deleted)` 说明二进制被替换后还没重启）
- **清理测试资源**: 用 `sds resource delete <res>`，不要直接删 `/etc/drbd.d/*.res`（数据库和 LV 会残留）
