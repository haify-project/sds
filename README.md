# SDS - Software Defined Storage

Software Defined Storage solution built on DRBD, written in Go.

## Architecture

```
┌─────────────────────────────────────────────────────┐
│                    sds-cli                          │
│              (Command Line Interface)               │
└──────────────────┬──────────────────────────────────┘
                   │ gRPC/REST
                   ▼
┌─────────────────────────────────────────────────────┐
│              sds-controller                          │
│           (systemd service)                          │
│  ┌───────────────────────────────────────────────┐  │
│  │         Core Business Logic                    │  │
│  │  - Pool Management                             │  │
│  │  - Volume Management                           │  │
│  │  - Snapshot Management                         │  │
│  │  - Gateway Management                          │  │
│  └───────────────────────────────────────────────┘  │
└──────────────────┬──────────────────────────────────┘
                   │ gRPC
                   ▼
┌─────────────────────────────────────────────────────┐
│              drbd-agent (Go)                         │
│     (Running on each storage node)                  │
│  ┌───────────────────────────────────────────────┐  │
│  │   DRBD | LVM | systemd | Block Device         │  │
│  └───────────────────────────────────────────────┘  │
└─────────────────────────────────────────────────────┘
```

## Features

- **Storage Pool Management**: Create and manage LVM volume groups
- **Volume Management**: Create, resize, delete logical volumes
- **DRBD Resources**: Automated DRBD resource configuration and management
- **Snapshots**: LVM snapshot support for data protection
- **Gateways**: NFS, iSCSI, NVMe-oF gateway management
- **High Availability**: Integration with drbd-reactor for automatic failover

## Project Structure

```
sds/
├── cmd/
│   ├── cli/              # Command line interface
│   └── controller/       # Controller service
├── pkg/
│   ├── client/           # drbd-agent gRPC client
│   ├── controller/       # Controller implementation
│   ├── core/             # Core business logic
│   ├── config/           # Configuration management
│   └── api/              # API definitions
├── internal/
│   ├── storage/          # Storage operations
│   ├── volume/           # Volume operations
│   ├── pool/             # Pool operations
│   ├── snapshot/         # Snapshot operations
│   └── gateway/          # Gateway operations
├── proto/api/v1/         # gRPC proto definitions
├── configs/              # Configuration files
├── scripts/              # Deployment and utility scripts
└── go.mod
```

## Getting Started

### Prerequisites

- Go 1.21+
- Running drbd-agent instances
- LVM configured on storage nodes

### Installation

```bash
# Build
make build

# Install controller
sudo make install-controller

# Install CLI
sudo make install-cli

# Start controller
sudo systemctl start sds-controller
sudo systemctl enable sds-controller
```

### Usage

```bash
# Show controller status
sds-cli status

# List pools
sds-cli pool list

# Create a volume
sds-cli volume create --pool vg0 --name data --size 100G

# Create a snapshot
sds-cli snapshot create --volume vg0/data --name snap1

# List DRBD resources
sds-cli drbd list
```

## Configuration

Controller configuration: `/etc/sds/controller.toml`

```toml
[server]
listen_address = "0.0.0.0:3374"

[drbd_agent]
# drbd-agent endpoints for each node
endpoints = [
    "node1.example.com:50051",
    "node2.example.com:50051",
]

[tls]
enabled = true
ca_cert = "/etc/sds/certs/ca.crt"
client_cert = "/etc/sds/certs/client.crt"
client_key = "/etc/sds/certs/client.key"

[storage]
default_pool_type = "vg"
default_snapshot_suffix = "_snap"
```

## Development

```bash
# Run tests
make test

# Run controller locally
make run-controller

# Run CLI
make run-cli -- pool list
```

## License

Apache License 2.0
