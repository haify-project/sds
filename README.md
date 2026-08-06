# SDS - Software Defined Storage

Based on DRBD & LINSTOR concepts, a lightweight Software Defined Storage controller written in Go. It manages storage pools, replicated DRBD volumes, storage gateways (iSCSI/NFS/NVMe-oF) and high availability, and integrates with Kubernetes via a native CSI driver.

English | [简体中文](README_cn.md)

## Architecture

SDS uses an agent-less architecture for storage nodes. A single controller drives every node over SSH (via the `dispatch` library) to run `drbdadm`, `lvm`, `zfs` and `drbd-reactor` commands and distribute configuration. State is persisted in an embedded BoltDB. Clients talk to the controller over gRPC (`sds-cli`), a REST gateway, an embedded web UI, an MCP server (for AI assistants), and the Kubernetes CSI driver.

```mermaid
graph TD
    CLI[sds-cli] -->|gRPC :3374| CTRL[sds-controller]
    UI[Web UI :3376] --> REST[REST gateway :3375]
    REST -->|gRPC| CTRL
    MCP[sds-mcp / AI assistant] -->|gRPC| CTRL
    K8S[Kubernetes CSI driver] -->|gRPC| CTRL

    subgraph Controller Host
    CTRL
    DB[(BoltDB)]
    end

    CTRL -->|SSH / Dispatch| Node1[Storage Node 1]
    CTRL -->|SSH / Dispatch| Node2[Storage Node 2]
    CTRL -->|SSH / Dispatch| Node3[Storage Node 3]

    subgraph Storage Node
    Node1
    LVM[LVM / ZFS]
    DRBD[DRBD 9 Kernel Module]
    Reactor[drbd-reactor]
    end
```

## Features

- **Storage pools**: LVM (VG), LVM-thin, and ZFS (zpool / thin) pool management.
- **DRBD resources**:
  - Automated resource creation, config distribution, and adjustment.
  - Advanced DRBD options (`on-no-quorum`, `c-plan-ahead`, quorum, etc.).
  - Online volume expansion; add / remove / resize volumes.
- **Placement**: capacity-first automatic replica placement with LINSTOR-style
  constraints — rack/zone-aware (`replicas-on-different`), `replicas-on-same`,
  and `do-not-place-with`. Give `--nodes` to place manually or omit it to
  auto-place.
- **Diskless clients**: a node with no local replica can attach a resource and
  do I/O over the DRBD network (`resource diskless attach`), plus automatic
  quorum tiebreakers.
- **High Availability**:
  - `drbd-reactor` promoter integration for automatic service failover.
  - Floating Virtual IP (VIP) + systemd service ordering.
  - **Self-HA**: the controller itself can run behind a floating VIP.
- **Gateways** (DRBD resource + drbd-reactor promoter config):
  - **iSCSI** (LIO): targets, LUNs, initiator ACLs, CHAP.
  - **NFS** (NFSv4): exports management.
  - **NVMe-oF**: subsystems, namespaces, host ACLs.
- **Kubernetes (CSI)**: dynamic provisioning of DRBD volumes, pool-aware replica
  placement, `WaitForFirstConsumer` topology, and opt-in diskless remote access
  (`allowRemoteVolumeAccess`) so Pods can run on non-replica nodes.
- **Snapshots**: LVM and ZFS snapshots, plus GFS (grandfather-father-son)
  retention schedules.
- **Cross-DC (WAN)**: a TCP proxy for running DRBD replication across NAT/WAN
  where inbound UDP is blocked.
- **Security & Ops**: token auth, RBAC, audit logging, optional TLS, and
  Prometheus metrics.
- **Notifications**: a health detector that raises events for degraded replicas,
  failovers, lost Primaries, unreachable nodes and broken WAN links, delivered
  by Webhook, by gRPC/REST watch stream, by SSE to the web UI's notification
  bell, or by `sds-cli event watch`.
- **Web UI**: an embedded single-page UI served by the controller.
- **AI Integration (MCP)**: `sds-mcp` exposes 44 management tools over the Model
  Context Protocol for AI assistants (Claude Code, Claude Desktop, etc.).

## Interfaces & default ports

| Interface        | Default port | Notes                                   |
| ---------------- | ------------ | --------------------------------------- |
| gRPC API         | 3374         | `sds-cli`, CSI, MCP                     |
| REST gateway     | 3375         | grpc-gateway JSON API                    |
| Web UI           | 3376         | embedded SPA                             |
| Prometheus       | 9433         | `metrics.enabled`                        |

## Project Structure

```text
sds/
├── cmd/
│   ├── cli/              # Command line interface (sds-cli)
│   ├── controller/       # Controller service (sds-controller)
│   ├── csi-controller/   # Kubernetes CSI controller plugin
│   ├── csi-node/         # Kubernetes CSI node plugin
│   ├── mcp/              # MCP server for AI assistants (sds-mcp)
│   └── sds-ai/           # AI Copilot service
├── pkg/
│   ├── client/           # gRPC client library
│   ├── controller/       # Core controller logic + gRPC/REST/UI servers
│   ├── csi/              # CSI driver (controller + node services)
│   ├── database/         # BoltDB persistence layer
│   ├── deployment/       # SSH execution engine (wraps dispatch)
│   ├── gateway/          # Gateway (iSCSI/NFS/NVMe-oF) managers
│   ├── reactor/          # drbd-reactor promoter config generation
│   ├── mcpserver/        # MCP tool definitions and handlers
│   ├── wanproxy/         # Cross-DC DRBD-over-TCP proxy
│   ├── alert/            # Health detector (degrade, failover, node loss)
│   ├── event/            # Notification bus, history, and Webhook delivery
│   ├── rbac/             # Role-based access control
│   ├── metrics/          # Prometheus metrics
│   ├── config/           # Configuration parsing
│   └── util/             # Utilities
├── api/proto/v1/         # gRPC Protocol Buffers definitions
├── ui/                   # Embedded web UI (go:embed of the built SPA)
├── web-ui/               # Web UI source (React/TypeScript)
├── deploy/k8s/           # Kubernetes CSI manifests
├── configs/              # Configuration examples and systemd units
└── scripts/              # Deployment scripts
```

## Getting Started

### Prerequisites

- **Controller Node**: Go 1.25+, `make`, `protoc` (only needed to regenerate protobufs).
- **Storage Nodes**:
  - Linux (Ubuntu/Debian/RHEL), reachable over SSH from the controller (root recommended).
  - **LVM2** (for LVM pools) and/or **ZFS** (`zfsutils-linux`, for ZFS pools).
  - **DRBD 9** kernel module and **drbd-utils**.
  - **drbd-reactor** (for HA / gateways).
  - **resource-agents** / `resource-agents-extra` (for VIP and service OCF agents).

### Installation

1. **Build**:

   ```bash
   make build
   ```

2. **Deploy** to the controller and distribute the CLI:

   ```bash
   ./scripts/deploy-all.sh --hosts "orange1,orange2,orange3"
   ```

## Configuration

The controller configuration lives at `/etc/sds/controller.toml`:

```toml
[server]
listen_address = "0.0.0.0"
port = 3374              # gRPC (REST 3375 and UI 3376 are derived)

[database]
path = "/var/lib/sds/sds.db"

[auth]
enabled = true
token = "change-me"     # bearer token; also read from /etc/sds/token

[storage]
default_pool_type = "vg"

[metrics]
enabled = true
listen_address = "0.0.0.0"
port = 9433

[resource]
auto_tiebreaker = true  # auto-add a diskless quorum tiebreaker for 2-replica resources

# Optional: run the controller behind a floating VIP
[self_ha]
enabled = false

# Optional: health detection and notifications.
#
# `enabled` starts the detector and the event bus on its own. Delivery is
# separate: with no webhook configured the events are still readable at
# GET /v1/events, streamed at GET /v1/events/watch, pushed to the web UI's
# notification bell over GET /v1/events/stream, and followed with
# `sds-cli event watch`.
[alert]
enabled = false
check_interval_sec = 60
check_nodes = true      # SSH-probe each node per poll; produces node.unreachable
history_size = 500      # events retained for clients that connect late

# Single-receiver shorthand.
webhook_url = ""
webhook_min_severity = "warning"   # info | warning | critical

# Additional receivers, each with its own threshold — a pager on critical,
# a chat channel on everything.
# [[alert.webhooks]]
# url = "https://chat.example.com/hooks/sds"
# min_severity = "info"
# headers = { X-Token = "..." }
```

### Notifications

Event types: `resource.degraded`, `resource.failover`, `resource.no_primary`,
`resource.promoted`, `node.unreachable`, `wan.degraded`. Each carries a
`severity` (`info`/`warning`/`critical`) and a `status` (`firing` when a
condition starts, `resolved` when it clears), so a receiver can pair an alert
with its recovery instead of reading the recovery as a new fault.

```bash
# Follow live, or replay what the controller still holds
sds-cli event watch
sds-cli event watch --min-severity critical --type resource.failover
sds-cli event list --replay --json

# Same events over REST (newline-delimited JSON)
curl -N http://controller:3375/v1/events/watch

# Browser-friendly SSE, which is what the web UI's bell subscribes to
curl -N http://controller:3375/v1/events/stream
```

Event ids are monotonic, so a client that sees a gap knows it fell behind rather
than that nothing happened; reconnecting with `since_id` resumes without
replaying what was already seen. A subscriber that stops reading loses its own
events rather than blocking the detector.

SSH access to storage nodes is **not** configured here — the `dispatch` library
reads its own `~/.dispatch/config.toml` (SSH user, key, and host→address map).
Hosts can also be managed at runtime with `sds-cli node register`.

## Usage Examples

### 1. Node Management

```bash
sds-cli node register --name orange1 --address 192.168.123.214
sds-cli node register --name orange2 --address 192.168.123.215
sds-cli node list
sds-cli health-check
```

### 2. Storage Pool Management

```bash
# LVM VG / thin / ZFS
sds-cli pool create --name data-pool --type lvm      --nodes orange1 --devices /dev/sdb
sds-cli pool create --name thin-pool --type lvm-thin --nodes orange1 --devices /dev/sdc
sds-cli pool create --name tank      --type zfs      --nodes orange1 --devices /dev/sdd
sds-cli pool list
```

### 3. Resource Management

```bash
# Create a replicated DRBD resource (omit --nodes to auto-place by free space)
sds-cli resource create --name res01 --port 7001 --size 10G --nodes orange1,orange2 --pool data-pool

# ZFS-backed resource
sds-cli resource create --name res-zfs --port 7002 --size 10G --nodes orange1,orange2 --pool tank --storage-type zfs

# Promote, make a filesystem, mount
sds-cli resource primary res01 orange1 --force
sds-cli resource fs res01 0 ext4 --node orange1
sds-cli resource mount res01 0 /mnt/res01 --node orange1

# Online expansion
sds-cli resource resize-volume res01 0 20G
```

### 4. Diskless Clients

```bash
# Let a node with no local replica mount the resource over the network
sds-cli resource diskless attach res01 orange3
sds-cli resource diskless detach res01 orange3
```

### 5. Gateways & HA

```bash
# iSCSI gateway with HA
sds-cli gateway iscsi create \
    --resource iscsi-gw \
    --service-ip 192.168.123.200/24 \
    --iqn iqn.2024-01.com.example:storage.target01

# NFS gateway with HA
sds-cli gateway nfs create \
    --resource nfs-gw \
    --service-ip 192.168.123.201/24 \
    --export-path /data/share

# NVMe-oF gateway
sds-cli gateway nvme create \
    --resource nvme-gw \
    --service-ip 192.168.123.202/24 \
    --nqn nqn.2024-01.com.example:storage.subsys01
```

### 6. Snapshots

```bash
sds-cli resource snapshot create --resource res01 --name res01_snap --node orange1
sds-cli resource snapshot list   --resource res01 --node orange1
# GFS-retention schedule
sds-cli resource snapshot schedule create --resource res01 --cron "0 * * * *" --keep-hourly 6 --keep-daily 7
```

### 7. Kubernetes (CSI)

The CSI driver provisions DRBD volumes as PersistentVolumes. Apply the manifests
in `deploy/k8s/` (set the endpoint to your controller's address), then use the
`sds-drbd` StorageClass:

```yaml
apiVersion: storage.k8s.io/v1
kind: StorageClass
metadata: { name: sds-drbd }
provisioner: sds.csi.liliang-cn.com
parameters:
  pool: "vg0"
  replicas: "2"
  storageType: "lvm"
  # allowRemoteVolumeAccess: "true"   # opt-in: run Pods on non-replica nodes (diskless)
volumeBindingMode: WaitForFirstConsumer
reclaimPolicy: Delete
```

Replica placement is pool-aware: volumes only land on nodes that host the
requested pool.

### 8. AI Assistants (MCP)

`sds-mcp` serves the full management surface (44 tools: pools, resources,
snapshots, gateways, HA) over the Model Context Protocol on stdio. Destructive
operations are annotated so MCP clients ask for confirmation, and `--read-only`
restricts the server to list/status/health tools.

```bash
# Register with Claude Code
claude mcp add sds -- sds-mcp --controller orange1:3374

# Monitoring-only access
claude mcp add sds-ro -- sds-mcp --controller orange1:3374 --read-only
```

The API token is resolved like sds-cli: `--token` flag, `SDS_TOKEN` env,
`~/.sds/token`, then `/etc/sds/token`.

## License

Apache License 2.0
