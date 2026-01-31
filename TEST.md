# SDS HA 测试文档

本文档记录了 SDS (Software Defined Storage) HA 功能的完整测试流程和结果。

## 测试环境

- **节点**: orange1, orange2, orange3
- **OS**: Ubuntu (使用 SSH 免密登录)
- **DRBD**: 9.x
- **drbd-reactor**: 1.10.0

## 部署配置

### 1. sds-controller 配置

sds-controller 只在 orange1 上运行，服务配置文件为 `/etc/systemd/system/sds-controller.service`：

```ini
[Unit]
Description=SDS Controller
After=network.target

[Service]
Type=simple
ExecStart=/usr/local/bin/sds-controller --config /etc/sds-controller/config.toml
Restart=always

[Install]
WantedBy=multi-user.target
```

**注意**: 不指定 `User` 和 `Environment`，服务以默认方式运行（root），通过 SSH config 管理连接。

### 2. controller.toml 配置

配置文件路径: `/etc/sds-controller/config.toml`

```toml
[server]
listen_address = "0.0.0.0"
port = 3374

[dispatch]
parallel = 10
hosts = ["orange1", "orange2", "orange3"]
ssh_user = "liliang"
ssh_key_path = "/root/.ssh/id_rsa"

[database]
path = "/var/lib/sds/sds.db"

[log]
level = "debug"
format = "text"

[storage]
default_pool_type = "vg"
default_snapshot_suffix = "_snap"
```

**重要**:
- `ssh_user` 和 `ssh_key_path` 指定 SSH 连接使用的用户和密钥
- **不使用** dispatch 配置文件 (`~/.dispatch/config.toml`)
- 所有 SSH 配置通过 `~/.ssh/config` 管理

### 3. SSH 配置

在 root 用户的 `~/.ssh/config` 中配置主机：

```config
Host orange1
    HostName 192.168.123.214
    User liliang
    StrictHostKeyChecking no

Host orange2
    HostName 192.168.123.215
    User liliang
    StrictHostKeyChecking no

Host orange3
    HostName 192.168.123.216
    User liliang
    StrictHostKeyChecking no
```

### 4. SSH 密钥配置

- sds-controller 运行在 root 用户
- 使用 `/root/.ssh/id_rsa` 密钥
- 公钥需要添加到所有节点的 `liliang` 用户的 `~/.ssh/authorized_keys`

```bash
# 在 orange1 上获取公钥
sudo cat /root/.ssh/id_rsa.pub

# 添加到 orange2, orange3
ssh orange2 "echo '<公钥内容>' >> ~/.ssh/authorized_keys"
ssh orange3 "echo '<公钥内容>' >> ~/.ssh/authorized_keys"
```

## 部署步骤

```bash
# 1. 构建二进制文件
cd /home/liliang/Codes/projects/HA/sds
make build

# 2. 部署到 orange1
scp bin/sds-controller orange1:/tmp/
scp bin/sds-cli orange1:/tmp/
scp configs/controller.toml orange1:/tmp/

# 3. 安装
ssh orange1 "sudo systemctl stop sds-controller"
ssh orange1 "sudo mv /tmp/sds-controller /usr/local/bin/"
ssh orange1 "sudo mv /tmp/sds-cli /usr/local/bin/"
ssh orange1 "sudo mv /tmp/controller.toml /etc/sds-controller/config.toml"

# 4. 启动服务
ssh orange1 "sudo systemctl start sds-controller"
ssh orange1 "sudo systemctl status sds-controller"
```

## 测试的资源

1. **mysql_res** - MySQL 数据库 HA 配置
2. **redis_res** - Redis 数据 HA 配置
3. **postgres_res** - PostgreSQL 数据库 HA 配置

---

## 1. mysql_res 测试

### 1.1 创建 HA 配置

```bash
sds-cli ha create mysql_res --mount /var/lib/mysql --fstype ext4 \
  --vip 192.168.123.240/24 --services mysql.service
```

**结果**: ✅ 成功

### 1.2 验证状态

```bash
# orange1 (活动节点)
drbd-reactorctl status /etc/drbd-reactor.d/sds-ha-mysql_res.toml
```

**预期输出**:
```
Promoter: Currently active on this node
● drbd-services@mysql_res.target
● ├─ drbd-promote@mysql_res.service
● ├─ var-lib-mysql.mount
● ├─ service-ip@192.168.123.240-24.service
● └─ mysql.service
```

### 1.3 Evict 故障转移测试

```bash
sds-cli ha evict mysql_res
```

**结果**: ✅ 成功从 orange1 转移到 orange3

**验证**:
- DRBD Primary: orange3
- mysql.service: orange3 上 active
- VIP 192.168.123.240: orange3 上存在

### 1.4 删除测试

```bash
sds-cli ha delete mysql_res
```

**结果**: ✅ 配置从所有节点删除

---

## 2. redis_res 测试

### 2.1 创建 HA 配置

```bash
sds-cli ha create redis_res --mount /var/lib/redis --fstype ext4 \
  --vip 192.168.123.242/24
```

### 2.2 写入测试数据

```bash
# 在活动节点上
echo 'test-data-$(date)' | sudo tee /var/lib/redis/test.txt
```

### 2.3 Evict 测试

```bash
sds-cli ha evict redis_res
```

**测试序列**:
1. 初始: orange2 (Primary)
2. 第一次 evict: orange2 → orange3 ✅
3. 第二次 evict: orange3 → orange2 ✅

**数据验证**: 每次转移后 `/var/lib/redis/test.txt` 数据完整

---

## 3. postgres_res 测试

### 3.1 创建 DRBD 资源

```bash
sds-cli resource create --name postgres_res --port 7002 \
  --nodes orange1,orange2,orange3 --size 10GB
```

### 3.2 设置主节点并创建文件系统

```bash
sds-cli resource primary postgres_res orange1 --force
sds-cli resource fs postgres_res 0 ext4 --node orange1
```

### 3.3 初始化 PostgreSQL 数据库

```bash
# 停止原有服务
sudo systemctl stop postgresql

# 删除旧数据
sudo rm -rf /var/lib/postgresql/16/main/*

# 初始化新数据库
sudo -u postgres /usr/lib/postgresql/16/bin/initdb -D /var/lib/postgresql/16/main
```

### 3.4 创建 HA 配置

```bash
sds-cli ha create postgres_res --mount /var/lib/postgresql \
  --fstype ext4 --services postgresql.service --vip 192.168.123.243/24
```

### 3.5 写入测试数据

```bash
sudo -u postgres psql -c "CREATE TABLE test_table (id SERIAL PRIMARY KEY, name VARCHAR(100), created_at TIMESTAMP DEFAULT NOW());"
sudo -u postgres psql -c "INSERT INTO test_table (name) VALUES ('test-data-1'), ('test-data-2'), ('test-data-3');"
```

### 3.6 Evict 测试

```bash
sds-cli ha evict postgres_res
```

**结果**: ✅ orange1 → orange2

**验证**:
- DRBD Primary: orange2
- VIP 192.168.123.243: orange2 上存在
- 数据库查询: 5 条记录全部完整

---

## drbd-reactorctl 状态参考

### 活动节点状态

```
Promoter: Currently active on this node
● drbd-services@<resource>.target
● ├─ drbd-promote@<resource>.service
● ├─ <mount>.mount
● ├─ service-ip@<IP>.service
● └─ <custom-service>.service
```

### 待机节点状态

```
Promoter: Currently active on node '<active-node>'
○ drbd-services@<resource>.target
○ ├─ drbd-promote@<resource>.service
○ ├─ <mount>.mount
○ ├─ service-ip@<IP>.service
○ └─ <custom-service>.service
```

---

## 命令参考

### HA 管理

| 命令 | 说明 |
|------|------|
| `sds-cli ha create <resource> [flags]` | 创建 HA 配置 |
| `sds-cli ha delete <resource>` | 删除 HA 配置 |
| `sds-cli ha list` | 列出所有 HA 配置 |
| `sds-cli ha status <resource>` | 查看 HA 配置状态 |
| `sds-cli ha evict <resource>` | 驱逐资源（触发故障转移） |

### 标志

| 标志 | 说明 |
|------|------|
| `--mount <path>` | 挂载点路径 |
| `--fstype <type>` | 文件系统类型 (ext4, xfs) |
| `--services <svc>` | systemd 服务 (逗号分隔) |
| `--vip <cidr>` | 虚拟 IP (CIDR 格式) |

---

## 注意事项

1. **服务名格式**: 使用完整的服务名，如 `mysql.service`, `postgresql.service`
2. **VIP 格式**: 使用 CIDR 格式，如 `192.168.123.240/24`
3. **资源命名**: 建议使用 `<name>_res` 格式以避免与 drbd-reactor 的命名推断冲突
4. **服务验证**: HA 创建时会验证服务在所有节点上存在

## 已知问题

### postgresql 模板服务

`postgresql.service` 是一个聚合服务，实际数据库服务是 `postgresql@16-main.service`。

drbd-reactor 对 `postgresql@16-main` 的资源名推断为 `postgres`，与 `postgres_res` 不匹配。

**解决方案**: 使用 `postgresql.service` 而不是 `postgresql@16-main.service`。

---

## 代码修改记录

### 修复的 Bug

1. **`findActiveNode()`** - 之前返回检查 DRBD 状态的节点，现在正确解析输出找到真正的 Primary 节点
2. **`getNodeHost()`** - 修复了对 "nodename:ip" 格式的支持
3. **服务验证命令** - 使用 `grep -w` 进行精确匹配

### 修改的文件

- `sds/pkg/controller/resources.go` - HA 管理、evict、删除功能
- `sds/cmd/cli/ha.go` - CLI 命令实现、删除功能 RPC 调用

---

## 2026-01-11 测试记录

### 测试环境

- **节点**: orange1 (192.168.123.214), orange2 (192.168.123.215), orange3 (192.168.123.216)
- **sds-controller**: 只在 orange1 上运行

### 完整测试步骤

#### 1. 注册节点

```bash
sds-cli node register --name orange1 --address 192.168.123.214
sds-cli node register --name orange2 --address 192.168.123.215
sds-cli node register --name orange3 --address 192.168.123.216
```

#### 2. 创建存储池

```bash
sds-cli pool create --name vg0 --node orange1 --disks /dev/sdb
sds-cli pool create --name vg0 --node orange2 --disks /dev/sdb
sds-cli pool create --name vg0 --node orange3 --disks /dev/sdb
```

#### 3. 创建 DRBD 资源

```bash
sds-cli resource create --name mysql_res --port 7001 \
  --nodes orange1,orange2,orange3 --pool vg0 --size 2GiB
```

#### 4. 设置主节点并创建文件系统

```bash
sds-cli resource primary mysql_res orange1 --force
sds-cli resource fs mysql_res 0 ext4
```

#### 5. 创建 HA 配置

```bash
sds-cli ha create mysql_res --mount /var/lib/mysql \
  --fstype ext4 --vip 192.168.123.240/24
```

#### 6. 安装和配置 MySQL

在所有节点上：

```bash
# 安装 MySQL
sudo apt install -y mysql-server

# 配置监听所有接口
sudo sed -i 's/bind-address.*/bind-address = 0.0.0.0/' /etc/mysql/mysql.conf.d/mysqld.cnf

# 创建远程用户
sudo mysql -e "CREATE USER 'sds'@'%' IDENTIFIED BY 'sds123'; \
  GRANT ALL PRIVILEGES ON *.* TO 'sds'@'%'; FLUSH PRIVILEGES;"
```

#### 7. 测试 MySQL 连接

```bash
# 通过 VIP 连接
mysql -h 192.168.123.240 -u sds -psds123 -e "SELECT VERSION();"

# 插入测试数据
mysql -h 192.168.123.240 -u sds -psds123 -e "
  CREATE DATABASE test_ha;
  USE test_ha;
  CREATE TABLE test_table (id INT AUTO_INCREMENT PRIMARY KEY, data VARCHAR(100));
  INSERT INTO test_table (data) VALUES ('test-1'), ('test-2'), ('test-3');
"
```

#### 8. 测试故障转移

```bash
# 触发故障转移
sudo drbd-reactorctl evict /etc/drbd-reactor.d/sds-ha-mysql_res.toml

# 验证数据完整性
mysql -h 192.168.123.240 -u sds -psds123 -e "SELECT * FROM test_ha.test_table;"
```

### 测试结果

| 项目 | 结果 | 说明 |
|------|------|------|
| 节点注册 | ✅ | 3个节点全部注册成功 |
| Pool创建 | ✅ | 3个节点vg0 pool创建成功 |
| DRBD资源创建 | ✅ | mysql_res (2GiB, Protocol C) |
| 主节点设置 | ✅ | orange1 → Primary |
| 文件系统创建 | ✅ | ext4 on /dev/drbd1 |
| HA配置创建 | ✅ | drbd-reactor配置已分发 |
| MySQL安装 | ✅ | 3个节点安装成功 |
| VIP分配 | ✅ | 192.168.123.240 |
| 数据插入 | ✅ | 5条记录 |
| 故障转移 | ✅ | orange1 → orange3 |
| 数据完整性 | ✅ | 故障转移后数据完整 |

### 遇到的问题和解决方案

#### 问题1: SSH 认证失败

**错误**:
```
ssh: handshake failed: ssh: unable to authenticate, attempted methods [none], no supported methods remain
```

**原因**: dispatch 默认使用 `root` 用户和 `id_rsa` 密钥，但实际需要使用 `liliang` 用户。

**解决方案**:
1. 在 `controller.toml` 中添加 SSH 配置：
```toml
[dispatch]
ssh_user = "liliang"
ssh_key_path = "/root/.ssh/id_rsa"
```
2. 在代码中添加 `SSHUser` 和 `SSHKeyPath` 配置支持

**修改的文件**:
- `sds/pkg/config/config.go` - 添加 `SSHUser` 和 `SSHKeyPath` 字段
- `sds/pkg/deployment/deployment.go` - 添加 SSH 配置传递
- `sds/pkg/controller/controller.go` - 传递 SSH 配置到 deployment

#### 问题2: service-ip 服务单元不存在

**错误**:
```
Unit service-ip@192.168.123.240-24.service not found
```

**原因**: service-ip 的 systemd 服务模板未安装。

**解决方案**: 在所有节点上安装服务模板
```bash
scp service-ip/deployment/service-ip@.service orange1:/tmp/
ssh orange1 "sudo mv /tmp/service-ip@.service /etc/systemd/system/ && sudo systemctl daemon-reload"
```

#### 问题3: MySQL 只监听 localhost

**错误**:
```
ERROR 2003 (HY000): Can't connect to MySQL server on '192.168.123.240:3306' (111)
```

**原因**: MySQL 默认只监听 127.0.0.1。

**解决方案**:
```bash
sudo sed -i 's/bind-address.*/bind-address = 0.0.0.0/' /etc/mysql/mysql.conf.d/mysqld.cnf
sudo systemctl restart mysql
```

#### 问题4: 故障转移后挂载点状态失败

**现象**:
```
× var-lib-mysql.mount
Failed unmounting /var/lib/mysql: target is busy
```

**原因**: MySQL 进程占用挂载点，导致卸载失败。

**解决方案**: 重置失败状态
```bash
sudo systemctl reset-failed var-lib-mysql.mount
sudo systemctl daemon-reload
```

### 验证命令

```bash
# 检查 DRBD 状态
sudo drbdadm status mysql_res

# 检查 drbd-reactor 状态
drbd-reactorctl status /etc/drbd-reactor.d/sds-ha-mysql_res.toml

# 检查 VIP
ip addr show | grep 192.168.123.240

# 检查挂载点
mount | grep mysql

# 检查 MySQL 连接
mysql -h 192.168.123.240 -u sds -psds123 -e "SELECT @@hostname;"

# 触发故障转移
sudo drbd-reactorctl evict /etc/drbd-reactor.d/sds-ha-mysql_res.toml
```

### 当前节点状态

| 节点 | DRBD 角色 | HA 状态 | VIP |
|------|----------|---------|-----|
| orange1 | Secondary | 待机 (○) | - |
| orange2 | Secondary | 待机 (○) | - |
| orange3 | Primary | 活动 (●) | 192.168.123.240 |
