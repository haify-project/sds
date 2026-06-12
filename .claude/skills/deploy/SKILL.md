---
name: deploy and test SDS
description: Guide for deploying and testing SDS in a local development and test environment
---

# SDS 部署与测试指南

本文档介绍如何在本地开发环境中构建 SDS，并将其部署到测试环境（`orange1`, `orange2`, `orange3`）进行验证。

## 环境前提

- **本地开发机**: 安装了 Go 1.24+, Make, Protoc。
- **测试节点**: `orange1`, `orange2`, `orange3`。
- **SSH 配置**: 本地到测试节点、以及测试节点之间（特别是 `orange1` 到其他节点）的 SSH 免密登录已配置完成。
- **存储设备**: 所有节点上均已准备好闲置的 `/dev/sdb` 用于测试。

## 控制器端口

| 端口 | 用途 |
|------|------|
| 3374 | gRPC API（sds-cli） |
| 3375 | REST API（gRPC-gateway，HTTP/JSON） |
| 3376 | Web UI（内嵌 SPA） |
| 9433 | Prometheus 指标 |

**API 认证（可选）**: 在 `/etc/sds/controller.toml` 中设置 `[auth] enabled = true` 和 `token`（至少 16 字符）后，gRPC 和 REST 均需要 Bearer Token。sds-cli 按以下顺序解析 token：`--token` 参数 → `SDS_TOKEN` 环境变量 → `~/.sds/token` → `/etc/sds/token`。REST 请求需带 `Authorization: Bearer <token>` 头。

**RBAC（可选，比单 token 更细）**: 在 `controller.toml` 设置 `[rbac] enabled = true` 并声明 `[[rbac.users]]`（name/token/role，role = admin/operator/viewer）后，单 token 失效，改用**每用户 token**。开启时配置要写到**所有节点**（self-HA 会故障转移）。改完重启 active 节点。用户/角色之后可在 Web UI 的 **Access** 页或 `sds-cli rbac user ...` 管理，持久化进 `sds.db`。

## 1. 构建与部署

使用一键部署脚本，自动执行编译并将二进制文件及配置分发到目标节点。

```bash
# 默认部署到 orange1
./scripts/deploy-all.sh

# 部署到所有测试节点
./scripts/deploy-all.sh orange1,orange2,orange3

# 或者直接使用 deploy.sh（支持 --build / --cli-only）
./scripts/deploy.sh --hosts orange1 --build
./scripts/deploy.sh --hosts orange2,orange3 --cli-only
```

该脚本会：

1.  执行 `make build`。
2.  调用 `deploy.sh` 将组件部署到指定主机（controller → `/opt/sds/bin/`，cli → `/usr/local/bin/`）。
3.  在目标节点配置并启动 `sds-controller.service`，并自动启用 drbd-reactor 自动 reload。

> ⚠️ `deploy-all.sh` / `deploy.sh` 假设的是**单控制器**模型,**不适用于控制器 self-HA 集群**,也不做交叉编译。两个常见坑见下。

### ⚠️ 坑一:交叉编译(开发机非 linux/amd64 时)

`make build` 只编译**本机架构**。开发机是 macOS(darwin/arm64)、节点是 linux/x86_64 时,直接 scp 过去的二进制**根本不能执行**。必须交叉编译:

```bash
GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -o bin/sds-controller ./cmd/controller
GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -o bin/sds-cli ./cmd/cli
```

### ⚠️ 坑二:Web UI 嵌入是**旧的**

`make ui-ensure` **只在 `ui/dist` 不存在时**才同步,改了 web-ui 后它不会更新,导致二进制里嵌的是旧 UI。每次重新构建控制器前要**强制同步**:

```bash
cd web-ui && npm run build && cd ..
rm -rf ui/dist && cp -r web-ui/dist ui/dist    # 强制,别用 make ui-ensure
GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -o bin/sds-controller ./cmd/controller
```

### 控制器 self-HA 集群的正确部署(重要)

本集群用 **drbd-reactor 托管控制器**:`sds-controller` **只在 active(`sds-meta` 资源为 Primary 的)节点运行**,standby 节点的服务是 `inactive` 且 `enabled=disabled`(由 reactor 管,不是 systemd)。所以**不能**在每个节点 `systemctl enable/start`(standby 上会因 DRBD 依赖失败)。正确做法:**所有节点都换二进制,只重启 active 节点**。

```bash
# 0) 交叉编译 + 强制同步 UI(见上)

# 1) 找出 active 节点(sds-meta Primary)
for h in orange1 orange2 orange3; do echo -n "$h: "; ssh $h "drbdadm role sds-meta 2>/dev/null"; done

# 2) 把新二进制推到所有节点(原地替换,不动 standby 的服务状态)
for h in orange1 orange2 orange3; do
  scp -q bin/sds-controller bin/sds-cli $h:/tmp/
  ssh $h 'sudo install -m755 /tmp/sds-controller /opt/sds/bin/sds-controller && \
          sudo install -m755 /tmp/sds-cli /usr/local/bin/sds-cli && rm -f /tmp/sds-*'
done

# 3) 只重启 active 节点(假设是 orange3),reactor 会让它保持 Primary
ssh orange3 'sudo systemctl restart sds-controller && sleep 3; systemctl is-active sds-controller'
```

> 注意:如果 active 节点是 orange1/2,把第 3 步换成对应节点。standby 节点拿到新二进制后,将来故障转移过去时自然就是新版本。

### 访问地址(self-HA VIP)

控制器 self-HA 会有一个**浮动 VIP**(配在 drbd-reactor 的 promoter 里,如 `192.168.123.51`),始终跟随 active 节点。用 VIP 访问最稳:

- Web UI: `http://<VIP>:3376/`
- REST: `http://<VIP>:3375`
- gRPC(CLI): `sds-cli -c <VIP>:3374`

**验证节点状态：**

```bash
ssh orange1 "sds-cli node list"
ssh orange1 "sds-cli health-check"
```

## 2. 准备存储 (Pool)

使用准备好的 `/dev/sdb` 创建存储池。`--type` 支持：`lvm`、`lvm-thin`、`zfs`、`zfs-thin`。

### 2.1 创建 LVM Pool

```bash
# --nodes 支持逗号分隔的多个节点，一条命令即可
ssh orange1 "sds-cli pool create --name data-pool --type lvm --nodes orange1,orange2 --devices /dev/sdb"
```

### 2.2 创建 LVM Thin Pool

```bash
# --size 指定 VG 内 thin pool 的大小
ssh orange1 "sds-cli pool create --name thin-pool --type lvm-thin --nodes orange1,orange2 --devices /dev/sdb --size 10G"
```

### 2.3 创建 ZFS Pool

```bash
ssh orange1 "sds-cli pool create --name tank --type zfs --nodes orange1,orange2 --devices /dev/sdb"
```

## 3. 测试资源创建 (Resource)

> **端口**:每个 DRBD 资源的 `--port` 必须全局唯一。从 controller v1.4 起,`resource create` 会**预检端口**,撞了会清晰报错(`port N is already in use by resource X`),而不是含糊的 `metadata creation failed`。已用端口看 `/etc/drbd.d/*.res`。
>
> **改 DRBD options**:不用重建资源,用 `sds-cli resource set-options <res> --drbd-options 'net/max-buffers=8000,on-no-quorum=suspend-io,disk/on-io-error=detach'`(就地改 `.res` + `drbdadm adjust`),或 Web UI 资源页的 **Edit DRBD Options**。格式 `section/key=value`,裸 key 进 options 段。

### 3.1 创建 LVM 资源 (默认)

```bash
# 创建资源 res01
ssh orange1 "sds-cli resource create --name res01 --port 7001 --size 1G --nodes orange1,orange2 --pool data-pool"

# 启用并挂载
ssh orange1 "sds-cli resource primary res01 orange1 --force"
ssh orange1 "sds-cli resource fs res01 0 ext4 --node orange1"
ssh orange1 "sds-cli resource mount res01 0 /mnt/res01 --node orange1"
```

### 3.2 创建带自定义选项的资源

```bash
ssh orange1 "sds-cli resource create --name res-opt --port 7002 --size 1G --nodes orange1,orange2 --pool data-pool \
  --drbd-options 'options/on-no-quorum=suspend-io,net/max-buffers=8000,disk/on-io-error=detach'"
```

### 3.3 创建 ZFS 资源

```bash
ssh orange1 "sds-cli resource create --name res-zfs --port 7003 --size 1G --nodes orange1,orange2 --pool tank --storage-type zfs"
```

## 4. 快照测试 (Snapshot)

快照按存储类型区分：LVM 使用 COW 快照（restore 通过 `lvconvert --merge`），ZFS 使用原生 snapshot/rollback。

```bash
# LVM 快照（--size 为 COW 空间预留）
ssh orange1 "sds-cli resource snapshot create --resource res01 --name snap1 --node orange1 --pool data-pool --size 1G"
ssh orange1 "sds-cli resource snapshot list --resource res01 --node orange1 --pool data-pool"
ssh orange1 "sds-cli resource snapshot restore --resource res01 --name snap1 --node orange1 --pool data-pool"
ssh orange1 "sds-cli resource snapshot delete --resource res01 --name snap1 --node orange1 --pool data-pool"

# ZFS 快照：追加 --storage-type zfs --pool tank
```

## 5. 网关测试 (Gateway)

> 网关需要资源有 **≥2 个卷**(volume 0 存故障转移状态,volume 1+ 存数据)。从 controller v1.4 起,`[gateway] auto_state_volume`(默认开)会在建网关时**自动补**那个 state 卷,所以单卷资源也能直接建网关,不用先手动 `add-volume`。

```bash
# 创建 NFS 网关（export 路径按传入值原样导出）
ssh orange1 "sds-cli gateway nfs create --resource res01 --service-ip 192.168.123.51/24 --export-path /mnt/res01"

# 网关生命周期管理
ssh orange1 "sds-cli gateway status --resource res01"
ssh orange1 "sds-cli gateway stop --resource res01"
ssh orange1 "sds-cli gateway start --resource res01"
ssh orange1 "sds-cli gateway delete --resource res01"
```

**注意**: iSCSI/NVMe-oF 网关配置包含 portblock OCF agent（在非活动节点上 DROP 网关端口流量）。创建/删除网关时会自动清理残留的 portblock 规则；若客户端连接异常，可用 `sudo iptables -L -n | grep <port>` 检查。

## 6. 控制器自身 HA (可选)

```bash
# 启用：创建 sds-meta DRBD 资源存放数据库，控制器分发到所有节点并由 drbd-reactor 托管
ssh orange1 "sds-cli ha self enable --vip 192.168.123.60/24 --pool data-pool"

# 之后通过 VIP 访问
ssh orange1 "sds-cli --controller 192.168.123.60:3374 ha self status"

# 恢复单机模式
ssh orange1 "sds-cli --controller 192.168.123.60:3374 ha self disable --node orange1"
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
- **节点健康检查**: `ssh orange1 "sds-cli health-check"`
- **DRBD 状态**: `ssh orange1 "sds-cli resource status res01"`（优先于直接 `drbdadm status`）
- **清理环境**: `ssh orange1 "sudo rm /etc/drbd.d/res*.res && sudo systemctl reload drbd-reactor"`
- **残留 portblock 规则**: `ssh orange1 "sudo iptables -L -n | grep -E '3260|4420'"`
