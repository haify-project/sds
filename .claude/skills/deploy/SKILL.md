---
name: deploy and test SDS
description: Guide for deploying and testing SDS in a local development and test environment
---

# SDS 部署与测试指南

本文档介绍如何在本地开发环境中构建 SDS，并将其部署到测试环境（`orange1`, `orange2`, `orange3`）进行验证。

## 环境前提

- **本地开发机**: 安装了 Go 1.22+, Make, Protoc。
- **测试节点**: `orange1`, `orange2`, `orange3`。
- **SSH 配置**: 本地到测试节点、以及测试节点之间（特别是 `orange1` 到其他节点）的 SSH 免密登录已配置完成。
- **存储设备**: 所有节点上均已准备好闲置的 `/dev/sdb` 用于测试。

## 1. 构建与部署

使用一键部署脚本，自动执行编译并将二进制文件及配置分发到目标节点。

```bash
# 默认部署到 orange1
./scripts/deploy-all.sh

# 部署到所有测试节点
./scripts/deploy-all.sh orange1,orange2,orange3
```

该脚本会：

1.  执行 `make build`。
2.  调用 `deploy.sh` 将组件部署到指定主机。
3.  在目标节点配置并启动 `sds-controller.service`。

**验证节点状态：**

```bash
ssh orange1 "sds-cli node list"
```

## 2. 准备存储 (Pool)

使用准备好的 `/dev/sdb` 创建存储池。

### 2.1 创建 LVM Pool (VG)

```bash
# 在各节点上为 /dev/sdb 创建名为 data-pool 的池
ssh orange1 "sds-cli pool create --name data-pool --type vg --node orange1 --devices /dev/sdb"
ssh orange1 "sds-cli pool create --name data-pool --type vg --node orange2 --devices /dev/sdb"
```

### 2.2 创建 ZFS Pool

```bash
# 在各节点上为 /dev/sdb 创建名为 tank 的 ZFS 池
ssh orange1 "sds-cli storage pool create-zfs --name tank --node orange1 --devices /dev/sdb"
ssh orange1 "sds-cli storage pool create-zfs --name tank --node orange2 --devices /dev/sdb"
```

## 3. 测试资源创建 (Resource)

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

## 4. 故障排查

- **控制器日志**: `ssh orange1 "journalctl -u sds-controller -f"`
- **DRBD 状态**: `ssh orange1 "sudo drbdadm status"`
- **清理环境**: `ssh orange1 "sudo rm /etc/drbd.d/res*.res && sudo systemctl reload drbd-reactor"`
