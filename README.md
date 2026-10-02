# SDS - Software Defined Storage

A lightweight DRBD 9 storage controller written in Go. It manages storage pools, replicated volumes, iSCSI / NFS / NVMe-oF gateways and high availability, and plugs into Kubernetes (CSI), Proxmox VE and AI assistants (MCP).

English | [简体中文](README_cn.md) | [Documentation site](https://haify-project.github.io/sds/)

![SDS architecture](docs/img/architecture-en.png)

No agent runs on the storage nodes: one controller drives them over SSH and keeps its state in an embedded BoltDB. The controller itself can run behind a floating VIP (Self-HA).

## Features

- **Pools**: LVM, LVM-thin, ZFS. SSD caching for thin pools.
- **Resources**: replicated DRBD volumes with automatic placement, online resize, diskless clients, quorum tiebreakers, LUKS2 encryption at rest, TLS-encrypted replication.
- **Gateways**: iSCSI, NFS, NVMe-oF, each failing over with drbd-reactor and a floating IP.
- **Snapshots and backups**: LVM / ZFS snapshots with retention schedules; scheduled, incremental backups to S3, SMB or WebDAV, restorable on a rebuilt or different cluster.
- **Cross-site**: asynchronous DR replicas over a TCP tunnel that works through NAT.
- **Integrity**: scheduled DRBD verify with resync, alerts on degraded replicas, lost Primaries, out-of-sync data and full pools.
- **Kubernetes**: CSI driver with snapshots, clones, expansion, raw block and remote (diskless) access.
- **AI**: MCP server for Claude Code, ChatGPT and others (local or remote with role-scoped tokens and OAuth), plus a Copilot in the web UI.
- **Operations**: token auth, RBAC, audit log, TLS, Prometheus metrics, notifications to Feishu / Slack / WeCom / DingTalk / webhooks.

## Requirements

Storage nodes: Linux with DRBD 9 (kernel module and `drbd-utils`), `drbd-reactor`, `resource-agents`, and LVM and/or ZFS. The full per-node list, with what breaks when each piece is missing, is in [docs/node-prerequisites.md](docs/node-prerequisites.md).

The controller host (a storage node or a separate machine) needs root SSH to every node.

## Quickstart

Three nodes, `node1`..`node3` at `10.0.0.11`..`10.0.0.13`, each with an empty disk `/dev/sdb`. The controller runs on `node1`.

**1. Get the binaries.** Releases carry a linux/amd64 archive with `sds-controller`, `sds`, `sds-mcp`, `service-ip`, the systemd units and `controller.toml.example`:

```bash
curl -LO https://github.com/haify-project/sds/releases/latest/download/sds-linux-amd64.tar.gz
tar -xzf sds-linux-amd64.tar.gz
```

Or build from source (Go 1.26+, Node.js for the embedded web UI); `make build` writes the binaries to `bin/`:

```bash
git clone https://github.com/haify-project/sds.git && cd sds
(cd web-ui && npm ci) && make build
```

`make build` targets the host it runs on. To build Linux binaries elsewhere, see [docs/deployment-guide.md](docs/deployment-guide.md#1-build-the-binaries).

**2. SSH and dispatch.** The controller runs node commands over SSH with the [dispatch](https://github.com/liliang-cn/dispatch) library. On `node1`, as root, create a key and append its public half to `/root/.ssh/authorized_keys` on every node, `node1` included:

```bash
ssh-keygen -t ed25519 -N "" -f /root/.ssh/id_ed25519
```

Then write `/root/.dispatch/config.toml`:

```toml
[ssh]
user = "root"
port = 22
key_path = "/root/.ssh/id_ed25519"
strict_host_key = false   # record unknown host keys on first contact
timeout = "30s"
```

A node that needs a different user, port or key gets its own section keyed by its IP address (`[hosts."10.0.0.12"]`); see [docs/deployment-guide.md](docs/deployment-guide.md#3-ssh-trust-and-dispatch-config).

**3. Install and start the controller** on `node1`, from the unpacked archive (in a source checkout the binaries are in `bin/` and the units in `configs/`):

```bash
install -d /opt/sds/bin /etc/sds
install -m 755 sds-controller service-ip /opt/sds/bin/
install -m 755 sds /usr/local/bin/
cp sds-controller.service service-ip@.service /etc/systemd/system/
cat > /etc/sds/controller.toml <<'TOML'
[dispatch]
config_path = "/root/.dispatch/config.toml"
TOML
systemctl daemon-reload
systemctl enable --now sds-controller
```

Every other key has a default; all of them are listed in [`configs/controller.toml.example`](configs/controller.toml.example).

**4. Register the nodes and check them:**

```bash
sds node register --name node1 --address 10.0.0.11
sds node register --name node2 --address 10.0.0.12
sds node register --name node3 --address 10.0.0.13
sds health-check
```

**5. Create a pool** (an LVM thin pool by default; the VG is named `sds_pool0`):

```bash
sds pool create --name pool0 --nodes node1,node2,node3 --devices /dev/sdb
```

**6. Create a replicated resource** and watch it sync:

```bash
sds resource create --name data --port 7001 --size 10G --nodes node1,node2 --pool pool0
sds resource status data
```

The volume is `/dev/drbd/by-res/data/0` on both nodes. To export it, add a gateway:

```bash
sds gateway nfs create --resource data --service-ip 10.0.0.200/24 --export-path /data
```

The web UI is at `http://node1:3376`. Self-HA for the controller, WAN replicas, backups and the rest are in [docs/deployment-guide.md](docs/deployment-guide.md) and [docs/user-guide.md](docs/user-guide.md).

## Integrations

Kubernetes: put the controller address in `deploy/k8s/00-sds-controller-endpoint.yaml`, `kubectl apply -f deploy/k8s/`, then use the `sds-drbd` StorageClass ([deploy/k8s/README.md](deploy/k8s/README.md)). Proxmox VE: [deploy/proxmox/README.md](deploy/proxmox/README.md). Prometheus and Grafana: [deploy/monitoring/README.md](deploy/monitoring/README.md).

AI assistants:

```bash
claude mcp add sds -- sds-mcp --controller node1:3374            # local
claude mcp add --transport http sds https://<host>/mcp \
    --header "Authorization: Bearer <token>"                     # remote, sds-mcp serve
```

Tools, roles and tokens: [docs/mcp.md](docs/mcp.md).

## Ports

| Port | Service |
| ---- | ------- |
| 3374 | gRPC (`sds`, CSI, MCP) |
| 3375 | REST |
| 3376 | Web UI |
| 9433 | Prometheus |

Controller config: `/etc/sds/controller.toml`.

## Documentation

| Guide | For |
| ----- | --- |
| [docs/user-guide.md](docs/user-guide.md) | Day-to-day use and troubleshooting. Start here. |
| [docs/deployment-guide.md](docs/deployment-guide.md) | Building a cluster from nothing. |
| [docs/node-prerequisites.md](docs/node-prerequisites.md) | What each node needs installed. |
| [docs/mcp.md](docs/mcp.md) | Local and remote MCP, tokens, runbooks. |

## License

Apache License 2.0
