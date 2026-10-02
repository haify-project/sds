# CLAUDE.md - SDS Controller Development Guide

This file provides guidance to Claude Code when working on the SDS (Software Defined Storage) controller project.

## Project Overview

SDS is a DRBD-based storage management system built in Go. It provides centralized management for storage pools, DRBD resources, snapshots, backups, storage gateways (NFS, iSCSI, NVMe-oF), and HA, with a CSI driver, a Proxmox VE plugin, an MCP server and an embedded web UI on top.

**Architecture:**

```
sds / web UI / sds-mcp / CSI / Proxmox plugin
        |  gRPC :3374, REST :3375 (grpc-gateway), UI :3376
        v
sds-controller --> pkg/deployment --> dispatch --> SSH --> storage nodes
                                                              |
                                                              v
                                                  DRBD + drbd-reactor (failover)
```

Binaries (`make build` puts them in `bin/`):

| Binary | Source |
| ------ | ------ |
| `sds-controller` | `cmd/controller` |
| `sds` | `cmd/cli` |
| `sds-mcp` | `cmd/mcp` |
| `service-ip` | `cmd/service-ip` (Linux only; brings a floating IP up/down on a node) |
| `csi-controller`, `csi-node` | `cmd/csi-controller`, `cmd/csi-node` |
| `sds-ai` | `cmd/sds-ai` — separate Go module (own `go.mod`), not built by `make build` |

**Key Design:**

- **SSH-Based Execution**: Uses `dispatch` library for SSH-based operations directly on storage nodes.
- **drbd-reactor integration**: Creates promoter configs for automatic failover
- **Gateway pattern**: Gateways are DRBD resources + drbd-reactor configs that export storage via NFS/iSCSI/NVMe-oF

## Build and Test

**Before pushing, run `make ci`** — it runs what `.github/workflows/ci.yml`
runs (file size → gofmt → vet → golangci-lint → build → race tests →
govulncheck → web-ui build), plus a second vet/lint/build pass with
`GOOS=linux` so Linux-only code is checked on a Mac. CI gates on `gofmt`
*before* it ever runs the tests, so a green `go test` locally says nothing
about whether CI will pass. `make hooks` installs a pre-commit hook that
rejects staged Go files which are not gofmt-clean.

Note the gofmt gate checks plain `gofmt -l`, not the stricter `gofmt -s`
that `make fmt` applies.

```bash
# Install the pre-commit gofmt hook (once per clone)
make hooks

# Run the full CI pipeline locally
make ci

# Build binaries
make build

# Run all tests
make test

# Run single test
go test -v ./pkg/gateway -run TestGenerateNFSGatewayConfig

# Format code
make fmt

# Run linter
make lint

# Install on the current host (each target runs `make build` first)
make install-controller   # sds-controller + service-ip to /opt/sds/bin, systemd units
make install-cli          # /usr/local/bin/sds
make install-mcp          # /usr/local/bin/sds-mcp

# Regenerate api/proto/v1 Go code after editing sds.proto
make proto

# Run controller locally (reads configs/controller.toml, which is not in the
# repo: copy configs/controller.toml.example first)
make run-controller

# Run CLI
make run-cli ARGS="pool list"
```

## Deployment

### Deploy Controller to Server

```bash
# Cross-compiles (linux/amd64 by default; TARGET_OS/TARGET_ARCH override),
# installs sds-controller to /opt/sds/bin and sds to /usr/local/bin on each
# host, then restarts the controller. With Self-HA (sds-meta exists) only the
# sds-meta Primary is restarted.
./scripts/deploy-all.sh orange1,orange2,orange3
```

`make build` alone builds for the host it runs on; binaries built on a Mac do
not run on the nodes. The unit file (`configs/sds-controller.service`) runs
`/opt/sds/bin/sds-controller`, but a node's installed unit may point elsewhere —
check `systemctl cat sds-controller | grep ExecStart` before copying by hand.
The full procedure is in `.claude/skills/deploy/SKILL.md`.

### Test with grpcurl (local)

The controller does not register gRPC reflection, so pass the proto files:

```bash
P="-import-path api/proto/v1 -import-path third_party -proto sds.proto"

# List services
grpcurl $P -plaintext orange1:3374 list

# Call gRPC methods
grpcurl $P -plaintext orange1:3374 v1.SDSController/ListPools

# Or the REST gateway
curl -s http://orange1:3375/v1/pools
```

### Test with sds (on server)

```bash
# Pool operations
sds pool list
sds pool create --name pool0 --type lvm-thin --nodes orange1,orange2 --devices /dev/vdb

# Resource operations (--port and --size are required; omit --nodes to auto-place)
sds resource create --name data --port 7000 --size 10G --nodes orange1,orange2 --pool pool0

# Gateway operations (one gateway per resource)
sds gateway nfs create --resource data --service-ip 192.168.1.200/24 --export-path /data
sds gateway iscsi create --resource blk --iqn iqn.2024-01.com.example:sds.blk --service-ip 192.168.1.100/24
sds gateway nvme create --resource nvm --nqn nqn.2024-01.com.example:sds.nvm --service-ip 192.168.1.150/24
```

## Configuration

Controller config: `/etc/sds/controller.toml` (without `--config` the
controller looks for `controller.toml` in `/etc/sds/`, `./configs/`, `.`).
Every key, with its default, is in `configs/controller.toml.example`; the
structs are in `pkg/config/config.go`. Excerpt:

```toml
[server]
listen_address = "0.0.0.0"
port = 3374
rest_port = 3375

[database]
path = "/var/lib/sds/sds.db"

[dispatch]
config_path = "/root/.dispatch/config.toml"   # empty = dispatch default (~/.dispatch/config.toml)
parallel = 10                                 # 0 = deployment client default

[log]
level = "info"
format = "json"

[storage]
default_pool_type = "thin_pool"   # what an omitted --type becomes, for every client
default_snapshot_suffix = "_snap"
verify_schedule = "0 3 1 * *"     # DRBD online verify of every resource; "" disables

[metrics]
enabled = true
port = 9433

[ui]
port = 3376
```

Other sections: `[wan]`, `[auth]`, `[tls]`, `[audit]`, `[rbac]`,
`[gateway]`, `[resource]`, `[schedule]`, `[self_ha]`, `[alert]`, `[inspect]`.

**Default ports**: gRPC 3374, REST 3375 (`[server] rest_port`), web UI 3376, Prometheus 9433.

## Package Structure

| Package          | Purpose                                                                                |
| ---------------- | -------------------------------------------------------------------------------------- |
| `pkg/controller` | Main controller: gRPC handlers (`server*.go`), managers for storage, resources, snapshots, nodes, gateways, HA, Self-HA, backups, WAN, REST gateway and UI server |
| `pkg/gateway`    | Gateway managers (NFS, iSCSI, NVMe-oF) - generates drbd-reactor configs                |
| `pkg/deployment` | Wrapper around dispatch for SSH-based operations                                       |
| `pkg/config`     | Configuration loading with viper                                                       |
| `pkg/client`     | gRPC client for sds                                                                |
| `pkg/database`   | BBolt store: resources, gateways, HA configs, schedules, backups, events, audit, notification channels |
| `pkg/alert`      | Health detector: turns cluster state into events (degrade, failover, node loss)        |
| `pkg/event`      | Notification bus, bounded history, and Webhook delivery                                |
| `pkg/backup`     | Backups to S3 / SMB / WebDAV (via rclone), incremental and scheduled                   |
| `pkg/drbdtls`    | CA and certificates for TLS-encrypted DRBD replication (kernel TLS via tlshd)          |
| `pkg/wanproxy`   | The per-resource sds-proxy mTLS pair that carries WAN (DR) replication                 |
| `pkg/rbac`       | Casbin-backed role authorization                                                       |
| `pkg/metrics`    | Prometheus metrics                                                                     |
| `pkg/logbuf`     | In-memory ring of recent controller log lines, served over the API                     |
| `pkg/triage`     | Turns events, audit and logs into a short list of known problems (used by `sds-mcp`)   |
| `pkg/inspect`    | Cluster inspection (`sds inspect`): the per-node probe script and pure pass/warn/fail checks over what the controller gathers (`pkg/controller/inspect*.go`) |
| `pkg/mcpserver`  | MCP tools over the controller API (`sds-mcp`, stdio and HTTP)                          |
| `pkg/mcpauth`    | Tokens and OAuth for the remote MCP server                                             |
| `pkg/k8sapp`     | Databases on Kubernetes backed by SDS volumes (`sds_app_*` MCP tools)                  |
| `pkg/csi`        | CSI driver (controller and node services)                                              |
| `pkg/serviceip`  | Floating IP add/remove and announcement, used by `service-ip`                          |
| `pkg/util`       | Size parsing and formatting                                                            |
| `cmd/*`          | Entry points, see the binaries table above                                             |
| `api/proto/v1`   | gRPC/REST protocol definitions (`sds.proto`) and generated code                        |
| `web-ui`         | React web UI; `make build` copies `web-ui/dist` into `ui/dist`, which `ui/ui.go` embeds |
| `deploy/`        | Kubernetes manifests (`k8s`), Proxmox plugin (`proxmox`), Prometheus/Grafana (`monitoring`) |

## Gateway Implementation Details

**Important**: The gateway package is intentionally isolated to avoid import cycles. It defines its own `ResourceInfo` type and uses interfaces for dependencies.

### Import Cycle Avoidance Pattern

The gateway package uses an adapter pattern to avoid circular dependencies:

1. `gateway/gateway.go` defines interfaces (`ResourceManager`, `DeploymentClient`)
2. `controller/controller_adapters.go` defines the adapters (`GatewayResourceManager`, `GatewayDeploymentClient`)
3. Gateway managers use these interfaces instead of importing controller types directly

```go
// In gateway/gateway.go
type ResourceManager interface {
    GetResource(ctx context.Context, name string) (*ResourceInfo, error)
    SetPrimary(ctx context.Context, resource, node string, force bool) error
    EnsureGatewayVolumes(ctx context.Context, resource string, minVolumes int) error
}

// In controller/controller_adapters.go
type GatewayResourceManager struct {
    rm *ResourceManager
    // ...
}
```

### Gateway Files

The package is split by responsibility rather than by protocol alone, because
the shared concerns are where the invisible failures live — a start/stop path
written without `lifecycle.go`'s invariants looks correct and isn't.

| File | Purpose |
| ---- | ------- |
| `gateway.go` | Common types, interfaces, the manager |
| `lifecycle.go` | Delete/start/stop, reactor config writing and reload. Reactor re-promotes within seconds unless the *config file* is disabled first, and dispatch's `sh -c` quoting empties `$vars` unless the script is base64-wrapped |
| `prereqs.go` | Whether the promoter's start chain will succeed. Skip any of these and the gateway *looks* created |
| `volumes.go` | Device-path resolution and the cluster-private/payload split. Getting either wrong exports the wrong block device, invisibly |
| `identity.go` | UUID/serial/FSID — how a client recognises its storage across a failover |
| `config_helpers.go` | Parsing and building promoter config lines |
| `config_read.go` | Reading promoter configs back from the nodes that hold them — never the controller's own filesystem, which is usually not a gateway node |
| `live_edit.go` + `live_protocols.go` + `reactor_units.go` | Editing a running gateway: applied live on the node serving it (targetcli, nvmet configfs, single-unit start/stop) because a drbd-reactor reload of a changed promoter stops the whole gateway; the edited config waits as `.toml.pending` there |
| `validate.go` | IQN/NQN/transport validation, rejected before any side effect |
| `nfs.go` | NFS gateway — Filesystem, IPaddr2, nfsserver, exportfs OCF agents |
| `iscsi.go` + `iscsi_target.go` + `iscsi_acl.go` | iSCSI gateway — iSCSITarget/iSCSILogicalUnit agents; target and LUNs; initiator allow-list and CHAP |
| `nvmeof.go` + `nvmeof_subsystem.go` + `nvmeof_hosts.go` | NVMe-oF gateway — nvmet-subsystem/nvmet-namespace agents; namespaces, subsystem and port; host allow-list |

**Limitation**: Each file must be under 600 lines — see "File size" below.

### Gateway Configuration

Gateways create TOML config files in `/etc/drbd-reactor.d/` named
`sds-<type>-<resource>.toml`, `<type>` being `nfs`, `iscsi` or `nvmeof`
(`sds ha create` writes `sds-ha-<resource>.toml`). NFS example:

```toml
[[promoter]]
  [promoter.resources.<resource>]
    runner = "systemd"
    start = [
      "ocf:heartbeat:Filesystem fs_cluster_private ...",
      "ocf:heartbeat:Filesystem fs_export ...",
      "ocf:heartbeat:nfsserver ...",
      "ocf:heartbeat:exportfs ...",
      "ocf:heartbeat:IPaddr2 ...",   # last to start, first to stop
    ]
```

iSCSI uses iSCSITarget/iSCSILogicalUnit; NVMe-oF uses
nvmet-subsystem/nvmet-namespace/nvmet-port. A gateway needs two volumes: a
small cluster-private state volume and the data volume. With
`[gateway] auto_state_volume` (default on) the state volume is added
automatically.

## Development Workflow

### Adding New Features

1. **API**: edit `api/proto/v1/sds.proto` (add a `google.api.http` option for REST), run `make proto`, implement the handler in the matching `pkg/controller/server_*.go`
2. **For storage/pool/snapshot operations**: `pkg/controller/storage.go` (pools), `storage_zfs.go`, `storage_lvm_snapshots.go`, `snapshots.go`, `snapshot_restore.go`
3. **For DRBD resource operations**: the `pkg/controller/resource_*.go` file for that concern (`resource_create.go`, `resource_delete.go`, `resource_resize.go`, `resource_volumes.go`, ...); `resources.go` holds the types and entry points
4. **For gateway operations**: Add to `pkg/gateway/*.go` (respective file)
5. **For CLI commands**: Add to `cmd/cli/*.go`
6. **For the web UI**: `web-ui/src`

### Testing Changes

1. `make ci`
2. Deploy: `./scripts/deploy-all.sh orange1,orange2,orange3`
3. Test with CLI, REST or grpcurl

### SSH Access

Test servers (orange1, orange2, etc.) can be accessed via SSH without password:

```bash
ssh orange1  # Works directly from local machine
```

## Common Commands

### DRBD Operations

```bash
# Create resource
drbdadm create-md <resource>
drbdadm up <resource>
drbdadm primary <resource>

# Check status
drbdadm status <resource>
drbdsetup status

# Adjust config
drbdadm adjust <resource>
```

### drbd-reactor Operations

```bash
# Reload configuration
systemctl reload drbd-reactor
systemctl restart drbd-reactor

# Check status
systemctl status drbd-reactor
journalctl -u drbd-reactor -f

# Manage promoters
drbd-reactorctl prom list
drbd-reactorctl prom enable <name>
drbd-reactorctl prom disable <name>
```

### LVM Operations

```bash
# Create VG
vgcreate <vgname> <devices>

# Create LV
lvcreate -L <size> -n <lvname> <vgname>

# Remove LV
lvremove -f <vg>/<lv>

# Display info
vgs
lvs
pvs
```

## Error Handling

- Always use structured logging with `zap.Logger`
- Return errors with context using `fmt.Errorf("operation: %w", err)`
- For multi-node operations, check `result.AllSuccess()` and `result.FailedHosts()`

## File size

**Every hand-written source file in the repo — Go, tests included, TypeScript,
Perl, shell, Python — must stay under 600 lines.** `make ci` enforces it
(`scripts/check-file-size.sh`, which checks git-tracked files); generated code
(protobuf, swagger) and `third_party/`, `ui/`, `website/` are exempt. When a file you are
editing approaches the limit, split it by responsibility before adding to it —
a name like `resource_part2.go` obeys the number and defeats the point.

This used to be a gateway-only rule. Outside that package nothing stopped
growth, and by 2026-10-01 `pkg/controller/resources.go` was 6032 lines and 33
files were over the limit.

## Notes

- **Production quality**: This is a production project. Code should be clean, well-commented, and robust.
- **No TODO comments in production code**: Implement features or omit placeholders.
- **DRBD resource names**: Match gateway names (one gateway per resource)
- **Service IPs**: Use CIDR notation (e.g., `192.168.1.100/24`)
- **Default ports**: NFS 2049, iSCSI 3260, NVMe-oF 4420
