# SDS 部署文档

本文档描述了 SDS (Software Defined Storage) 控制器及相关组件的部署流程。

## 目录

- [系统要求](#系统要求)
- [架构概览](#架构概览)
- [部署准备](#部署准备)
- [sds-controller 部署](#sds-controller-部署)
- [drbd-agent 部署](#drbd-agent-部署)
- [配置说明](#配置说明)
- [部署验证](#部署验证)
- [故障排查](#故障排查)

---

## 系统要求

### 硬件要求

- **最小节点数**: 2 个节点（推荐 3 个）
- **内存**: 每节点至少 2GB RAM
- **磁盘**: 每节点至少一块独立磁盘用于 DRBD

### 软件要求

| 软件 | 版本要求 |
|------|----------|
| OS | Ubuntu 20.04+ / Debian 11+ |
| DRBD | 9.x |
| drbd-reactor | 1.10.0+ |
| LVM2 | 2.0+ |
| Go | 1.21+ (编译时) |

---

## 架构概览

```
┌─────────────────────────────────────────────────────────────┐
│                         SDS Client                          │
│                      (sds-cli / API)                        │
└────────────────────────────┬────────────────────────────┘
                             │ gRPC (port 3374)
┌────────────────────────────┴────────────────────────────┐
│                      sds-controller                        │
│  ┌───────────┐ ┌─────────┐ ┌──────────┐ ┌───────────┐  │
│  │ Resource  │ │ Snapshot│ │ Gateway  │ │    HA     │  │
│  │  Manager  │ │ Manager │ │ Manager  │ │  Manager  │  │
│  └─────┬─────┘ └────┬────┘ └────┬─────┘ └────┬─────┘  │
└────────┼──────────┼──────────┼──────────┼──────────┘
         │          │          │          │
         └──────────┴──────────┴──────────┘
                          │
              ┌───────────┴───────────┐
              │     dispatch        │
              │   (SSH-based        │
              │    execution)       │
              └───────────┬───────────┘
                          │ SSH
        ┌───────────────────┼───────────────────┐
        │                   │                   │
   ┌────▼────┐        ┌────▼────┐       ┌────▼────┐
   │ orange1 │        │ orange2 │       │ orange3 │
   │         │        │         │       │         │
   │ DRBD    │        │ DRBD    │       │ DRBD    │
   │ LVM     │        │ LVM     │       │ LVM     │
   │reactor  │        │reactor  │       │reactor  │
   └─────────┘        └─────────┘       └─────────┘
```

---

## 部署准备

### 1. 配置 SSH 免密登录

在控制节点上生成 SSH 密钥并分发到所有存储节点：

```bash
# 生成密钥
ssh-keygen -t rsa -b 4096

# 分发公钥到所有节点
ssh-copy-id root@orange1
ssh-copy-id root@orange2
ssh-copy-id root@orange3
```

### 2. 安装依赖

在所有存储节点上安装必要软件包：

```bash
# Ubuntu/Debian
apt update
apt install -y drbd-utils drbd-dkms lvm2 drbd-reactor

# 加载 DRBD 模块
modprobe drbd
```

### 3. 配置 Dispatch

创建 `/root/.dispatch/config.toml`：

```toml
[default]
ssh_port = 22
ssh_timeout = "30s"
connect_timeout = "10s"
```

---

## sds-controller 部署

### 1. 编译

```bash
cd /path/to/sds
make build
```

生成二进制文件：
- `bin/sds-controller` - 控制器服务
- `bin/sds-cli` - 命令行工具

### 2. 安装

```bash
# 创建用户和目录
useradd -r -s /bin/false sds-controller
mkdir -p /opt/sds/bin
mkdir -p /opt/sds/etc
mkdir -p /var/lib/sds

# 复制二进制文件
install -m 755 bin/sds-controller /opt/sds/bin/
install -m 755 bin/sds-cli /usr/local/bin/

# 创建配置文件
cat > /etc/sds/controller.toml << 'EOF'
[server]
listen_address = "0.0.0.0"
port = 3374

[dispatch]
config_path = "/root/.dispatch/config.toml"
parallel = 10
hosts = ["orange1:192.168.123.219", "orange2:192.168.123.224", "orange3:192.168.123.225"]

[database]
path = "/var/lib/sds/sds.db"

[log]
level = "info"
format = "text"

[storage]
default_pool_type = "vg"
default_snapshot_suffix = "_snap"
EOF
```

### 3. 创建 systemd 服务

```bash
cat > /etc/systemd/system/sds-controller.service << 'EOF'
[Unit]
Description=SDS Controller
After=network.target
Wants=network-online.target

[Service]
Type=simple
User=root
ExecStart=/opt/sds/bin/sds-controller --config /etc/sds/controller.toml
Restart=always
RestartSec=5s

[Install]
WantedBy=multi-user.target
EOF

systemctl daemon-reload
systemctl enable sds-controller
systemctl start sds-controller
```

### 4. 验证部署

```bash
# 检查服务状态
systemctl status sds-controller

# 检查日志
journalctl -u sds-controller -f

# 使用 CLI 测试
sds-cli node list
sds-cli pool list
```

---

## drbd-agent 部署

drbd-agent 已被 dispatch 库取代，无需单独部署。sds-controller 通过 SSH 直接在存储节点上执行 DRBD/LVM 命令。

---

## 配置说明

### 控制器配置

`/etc/sds/controller.toml`:

```toml
[server]
listen_address = "0.0.0.0"  # 监听地址
port = 3374                 # gRPC 端口

[dispatch]
config_path = "/root/.dispatch/config.toml"
parallel = 10               # 并发 SSH 数
hosts = [
    "node1:192.168.1.10",  # 格式: nodename:ip
    "node2:192.168.1.11",
    "node3:192.168.1.12",
]

[database]
path = "/var/lib/sds/sds.db"  # SQLite 数据库路径

[log]
level = "debug"              # debug, info, warn, error
format = "text"              # text, json

[storage]
default_pool_type = "vg"    # vg (LVM), zfs
default_snapshot_suffix = "_snap"
```

### DRBD 资源配置

自动生成的 DRBD 配置位于 `/etc/drbd.d/<resource>.res`:

```
resource postgres_res {
    protocol C;

    on orange1 {
        device    /dev/drbd2;
        disk      /dev/data-pool/postgres_res_data;
        address   192.168.123.219:7002;
        meta-disk internal;
    }

    on orange2 {
        device    /dev/drbd2;
        disk      /dev/data-pool/postgres_res_data;
        address   192.168.123.224:7002;
        meta-disk internal;
    }

    on orange3 {
        device    /dev/drbd2;
        disk      /dev/data-pool/postgres_res_data;
        address   192.168.123.225:7002;
        meta-disk internal;
    }

    connection-mesh {
        hosts orange1, orange2, orange3;
    }
}
```

### drbd-reactor 配置

HA 配置生成的 promoter 配置位于 `/etc/drbd-reactor.d/sds-ha-<resource>.toml`:

```toml
[[promoter]]
[promoter.resources.postgres_res]
runner = "systemd"
start = [
  "var-lib-postgresql.mount",
  "service-ip@192.168.123.243-24.service",
  "postgresql.service"
]
on-drbd-demote-failure = "reboot"
```

---

## 部署验证

### 1. 基础功能测试

```bash
# 列出节点
sds-cli node list

# 列出存储池
sds-cli pool list

# 创建存储池
sds-cli pool create --name data-pool --node orange1 --disks /dev/vdb
```

### 2. DRBD 资源测试

```bash
# 创建资源
sds-cli resource create --name test_res --port 7000 \
  --nodes orange1,orange2,orange3 --size 10GB

# 设置主节点
sds-cli resource primary test_res orange1

# 查看状态
sds-cli resource status test_res
drbdsetup status test_res
```

### 3. HA 功能测试

```bash
# 创建 HA 配置
sds-cli ha create test_res --mount /mnt/data --fstype ext4 \
  --services nginx.service --vip 192.168.1.100/24

# 验证状态
drbd-reactorctl status /etc/drbd-reactor.d/sds-ha-test_res.toml

# 测试故障转移
sds-cli ha evict test_res

# 删除 HA 配置
sds-cli ha delete test_res
```

---

## 故障排查

### sds-controller 无法启动

```bash
# 检查日志
journalctl -u sds-controller -n 50

# 常见问题：
# - 配置文件语法错误
# - 端口被占用
# - 权限不足
```

### DRBD 资源创建失败

```bash
# 检查 LVM 状态
vgdisplay
lvdisplay

# 检查 DRBD 模块
lsmod | grep drbd

# 检查 DRBD 配置
ls /etc/drbd.d/
```

### drbd-reactor 状态异常

```bash
# 重新加载配置
systemctl reload drbd-reactor

# 检查状态
drbd-reactorctl ls

# 查看日志
journalctl -u drbd-reactor -f
```

### SSH 连接问题

```bash
# 测试 SSH 连接
ssh -v root@orange1

# 检查 SSH 密钥
ls -la /root/.ssh/

# 检查 dispatch 配置
cat /root/.dispatch/config.toml
```

---

## 升级部署

### 升级 sds-controller

```bash
# 1. 备份配置
cp /etc/sds/controller.toml /etc/sds/controller.toml.bak

# 2. 停止服务
systemctl stop sds-controller

# 3. 替换二进制文件
cp /path/to/new/sds-controller /opt/sds/bin/sds-controller

# 4. 启动服务
systemctl start sds-controller

# 5. 验证
systemctl status sds-controller
sds-cli node list
```

---

## 卸载

### 完全卸载

```bash
# 1. 停止并禁用服务
systemctl stop sds-controller
systemctl disable sds-controller

# 2. 删除服务文件
rm -f /etc/systemd/system/sds-controller.service
systemctl daemon-reload

# 3. 删除二进制和配置
rm -rf /opt/sds
rm -rf /var/lib/sds
rm -rf /etc/sds

# 4. 删除 CLI
rm -f /usr/local/bin/sds-cli
```

### 清理数据 (可选)

```bash
# 删除 DRBD 资源
drbdadm down <resource>
rm /etc/drbd.d/<resource>.res

# 删除 LVM 卷
lvremove -f <vg>/<lv>

# 删除 HA 配置
rm /etc/drbd-reactor.d/sds-ha-<resource>.toml
systemctl reload drbd-reactor
```

---

## 附录

### 端口使用

| 服务 | 端口 | 协议 |
|------|------|------|
| sds-controller gRPC | 3374 | TCP |
| DRBD (基础端口) | 7000+ | TCP |

### 目录结构

```
/opt/sds/
├── bin/
│   └── sds-controller
└── etc/
    └── controller.toml

/var/lib/sds/
└── sds.db                 # SQLite 数据库

/etc/drbd.d/
├── mysql_res.res
├── redis_res.res
└── postgres_res.res

/etc/drbd-reactor.d/
├── sds-ha-mysql_res.toml
├── sds-ha-redis_res.toml
└── sds-ha-postgres_res.toml
```
