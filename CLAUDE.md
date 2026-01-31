# CLAUDE.md - SDS Controller Development Guide

This file provides guidance to Claude Code when working on the SDS (Software Defined Storage) controller project.

## Project Overview

SDS is a DRBD-based storage management system built in Go. It provides centralized management for storage pools, DRBD resources, snapshots, and storage gateways (NFS, iSCSI, NVMe-oF).

**Architecture:**

```
sds-cli (client) --> sds-controller (gRPC) --> deployment --> dispatch --> SSH --> storage nodes
                                                        |
                                                        v
                                                  drbd-reactor (HA)
```

**Key Design:**

- **No drbd-agent dependency**: Uses `dispatch` library for SSH-based operations instead of a separate agent service
- **drbd-reactor integration**: Creates promoter configs for automatic failover
- **Gateway pattern**: Gateways are DRBD resources + drbd-reactor configs that export storage via NFS/iSCSI/NVMe-oF

## Build and Test

```bash
# Build binaries
make build

# Run tests
make test

# Format code
make fmt

# Run linter
make lint

# Install controller (on server)
sudo make install-controller

# Install CLI
sudo make install-cli

# Run controller locally
make run-controller

# Run CLI
make run-cli ARGS="pool list"
```

## Deployment

### Deploy Controller to Server

```bash
# Build and copy to server
make build
scp bin/sds-controller orange1:/tmp/

# Install on server
ssh orange1 "sudo systemctl stop sds-controller && \
  sudo cp /tmp/sds-controller /usr/local/bin/sds-controller && \
  sudo systemctl start sds-controller"
```

### Test with grpcurl (local)

```bash
# List services
grpcurl -plaintext orange1:50051 list

# Call gRPC methods
grpcurl -plaintext orange1:50051 v1.SDSController/ListPools
```

### Test with sds-cli (on server)

```bash
# Pool operations
sds-cli pool list
sds-cli pool create --name vg0 --node orange1 --disks /dev/vdb

# Resource operations
sds-cli resource create --name data --port 7000 --nodes orange1,orange2

# Gateway operations
sds-cli gateway nfs create --resource data --service-ip 192.168.1.200/24 --export-path /data
sds-cli gateway iscsi create --resource data --iqn iqn.2024-01.com.example:sds.data --service-ip 192.168.1.100/24
sds-cli gateway nvme create --resource data --nqn nqn.2024-01.com.example:sds.data --service-ip 192.168.1.150/24
```

## Configuration

Controller config: `/etc/sds/controller.toml`

```toml
[server]
listen_address = "0.0.0.0"
port = 3374

[dispatch]
config_path = "/root/.dispatch/config.toml"
parallel = 10
hosts = ["orange1", "orange2"]

[log]
level = "info"
format = "json"

[storage]
default_pool_type = "vg"
default_snapshot_suffix = "_snap"
```

**Default port: 3374** (changed from 3374)

## Package Structure

| Package          | Purpose                                                                                |
| ---------------- | -------------------------------------------------------------------------------------- |
| `pkg/controller` | Main controller, gRPC server, managers (storage, resources, snapshots, nodes, gateway) |
| `pkg/gateway`    | Gateway managers (NFS, iSCSI, NVMe-oF) - generates drbd-reactor configs                |
| `pkg/deployment` | Wrapper around dispatch for SSH-based operations                                       |
| `pkg/config`     | Configuration loading with viper                                                       |
| `pkg/client`     | gRPC client for sds-cli                                                                |
| `cmd/controller` | Controller main entry point                                                            |
| `cmd/cli`        | CLI tool commands                                                                      |
| `api/proto/v1`   | gRPC protocol definitions                                                              |

## Gateway Implementation Details

**Important**: The gateway package is intentionally isolated to avoid import cycles. It defines its own `ResourceInfo` type and uses interfaces for dependencies.

### Import Cycle Avoidance Pattern

The gateway package uses an adapter pattern to avoid circular dependencies:

1. `gateway/gateway.go` defines interfaces (`ResourceManager`, `DeploymentClient`)
2. `controller/controller.go` creates adapters (`GatewayResourceManager`, `GatewayDeploymentClient`)
3. Gateway managers use these interfaces instead of importing controller types directly

```go
// In gateway/gateway.go
type ResourceManager interface {
    GetResource(ctx context.Context, name string) (*ResourceInfo, error)
    SetPrimary(ctx context.Context, resource, node string, force bool) error
}

// In controller/controller.go
type GatewayResourceManager struct {
    rm *ResourceManager
}
```

### Gateway Files

| File                     | Lines | Purpose                                                                                        |
| ------------------------ | ----- | ---------------------------------------------------------------------------------------------- |
| `pkg/gateway/gateway.go` | ~380  | Common types, interfaces, shared operations                                                    |
| `pkg/gateway/nfs.go`     | ~310  | NFS gateway - creates promoter config with Filesystem, IPaddr2, nfsserver, exportfs OCF agents |
| `pkg/gateway/iscsi.go`   | ~410  | iSCSI gateway - creates promoter config with iSCSITarget, iSCSILogicalUnit OCF agents          |
| `pkg/gateway/nvmeof.go`  | ~410  | NVMe-oF gateway - creates promoter config with nvmet-subsystem, nvmet-namespace OCF agents     |

**Limitation**: Each file must be under 600 lines of code.

### Gateway Configuration

Gateways create TOML config files in `/etc/drbd-reactor.d/` with format `sds-<type>-<resource>.toml`:

```toml
[[promoter]]
  [promoter.resources.<resource>]
    runner = "systemd"
    start = [
      "ocf:heartbeat:Filesystem ...",
      "ocf:heartbeat:IPaddr2 ...",
      "ocf:heartbeat:nfsserver ...",  # or iSCSITarget, nvmet-subsystem
    ]
```

## Development Workflow

### Adding New Features

1. **For storage/pool/snapshot operations**: Add to `pkg/controller/storage.go`, `pkg/controller/pool.go`, `pkg/controller/snapshots.go`
2. **For DRBD resource operations**: Add to `pkg/controller/resources.go`
3. **For gateway operations**: Add to `pkg/gateway/*.go` (respective file)
4. **For CLI commands**: Add to `cmd/cli/*.go`

### Testing Changes

1. Build locally: `make build`
2. Deploy to test server: `scp bin/sds-controller orange1:/tmp/`
3. Restart service on server: `ssh orange1 "sudo systemctl restart sds-controller"`
4. Test with CLI or grpcurl

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

## Notes

- **Production quality**: This is a production project. Code should be clean, well-commented, and robust.
- **No TODO comments in production code**: Implement features or omit placeholders.
- **DRBD resource names**: Match gateway names (one gateway per resource)
- **Service IPs**: Use CIDR notation (e.g., `192.168.1.100/24`)
- **Default ports**: NFS 2049, iSCSI 3260, NVMe-oF 4420
