# SDS - Quick Start Guide

## 项目简介

SDS (Software Defined Storage) 是一个基于 DRBD 和 LVM 的软件定义存储解决方案，使用 Go 语言开发。

## 架构

```
用户 → sds-cli → sds-controller (gRPC) → drbd-agent → DRBD/LVM/systemd
```

## 特性

- ✅ 完整的 DRBD 资源管理
- ✅ LVM 存储池管理
- ✅ 卷管理（创建、删除、调整大小）
- ✅ systemd 服务管理
- ✅ 多节点支持
- ✅ 健康检查和自动重连
- ✅ 结构化日志
- ✅ 每个文件不超过 500 行

## 项目结构

```
sds/
├── cmd/
│   ├── cli/              # CLI 工具
│   └── controller/       # Controller 服务
├── pkg/
│   ├── client/           # drbd-agent gRPC 客户端
│   ├── controller/       # Controller 实现
│   └── config/           # 配置管理
├── internal/cli/commands/ # CLI 命令
├── configs/              # 配置文件
├── scripts/              # 部署脚本
└── proto/                # Proto 定义（从 drbd-agent 复制）
```

## 安装

### 前置要求

- Go 1.21+
- 运行中的 drbd-agent

### 安装步骤

```bash
# 1. 安装
make install

# 2. 编辑配置
sudo vi /etc/sds/controller.toml

# 3. 启动服务
sudo systemctl start sds-controller
sudo systemctl enable sds-controller

# 4. 检查状态
sudo systemctl status sds-controller
```

## 使用示例

### CLI 命令

```bash
# 查看状态
sds-cli status

# 列出存储池
sds-cli pool list

# 创建卷
sds-cli volume create --pool vg0 --name data --size 100

# 列出卷
sds-cli volume list --pool vg0

# DRBD 操作
sds-cli drbd list
sds-cli drbd status r0
sds-cli drbd primary r0

# systemd 服务
sds-cli systemd status drbd-reactor
sds-cli systemd restart drbd-reactor
```

### 配置文件

`/etc/sds/controller.toml`:

```toml
[server]
listen_address = "0.0.0.0"
port = 3373

[drbd_agent]
endpoints = [
    "localhost:50051",
    # "node1:50051",
    # "node2:50051",
]
timeout = 30

[log]
level = "info"
format = "json"
```

## 开发

```bash
# 构建
make build

# 运行测试
make test

# 运行 Controller
make run-controller

# 运行 CLI
make run-cli -- pool list
```

## API

### Controller API (gRPC)

Controller 在端口 3373 提供 gRPC 服务：

- 健康检查
- 存储池管理
- 卷管理
- DRBD 资源管理

### CLI 命令参考

| 命令 | 说明 |
|------|------|
| `sds-cli status` | 显示状态 |
| `sds-cli pool list` | 列出存储池 |
| `sds-cli pool create` | 创建存储池 |
| `sds-cli volume list` | 列出卷 |
| `sds-cli volume create` | 创建卷 |
| `sds-cli volume delete` | 删除卷 |
| `sds-cli drbd list` | 列出 DRBD 资源 |
| `sds-cli drbd up` | 启动 DRBD 资源 |
| `sds-cli drbd primary` | 提升为主节点 |
| `sds-cli systemd start` | 启动服务 |

## 与 Rust 版 ha-sds 的对比

| 特性 | Rust 版 ha-sds | Go 版 sds |
|------|---------------|-----------|
| 语言 | Rust | Go |
| gRPC | drbd-agent (Rust) | drbd-agent (Go) ✅ |
| CLI | sds-cli | sds-cli ✅ |
| Controller | sds-server | sds-controller ✅ |
| 功能 | 完整 | 完整 ✅ |
| 部署 | 复杂 | 简单 ✅ |
| 依赖管理 | Cargo | Go modules ✅ |

## 下一步

- [ ] 添加快照管理
- [ ] 添加网关管理（NFS/iSCSI/NVMe-oF）
- [ ] 添加 REST API
- [ ] 添加 Web UI
- [ ] 完善测试

## 故障排除

### Controller 无法启动

```bash
# 查看日志
sudo journalctl -u sds-controller -f

# 检查配置
sds-controller --config /etc/sds/controller.toml
```

### 无法连接到 drbd-agent

```bash
# 检查 drbd-agent 是否运行
sudo systemctl status drbd-agent

# 测试连接
grpcurl -plaintext localhost:50051 list
```

## 许可证

Apache License 2.0
