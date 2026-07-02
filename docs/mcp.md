# SDS MCP server (for AI agents)

`sds-mcp` (in `cmd/mcp`) exposes the SDS controller as a
[Model Context Protocol](https://modelcontextprotocol.io) server over stdio, so
an AI agent (Claude, etc.) can drive storage operations as tools: pools,
resources, volumes, gateways (NFS / iSCSI / NVMe-oF), HA, self-HA, snapshots,
nodes, and health.

## Install

```bash
make install-mcp        # builds + installs to /usr/local/bin/sds-mcp
# or:
go build -o ~/.local/bin/sds-mcp ./cmd/mcp
```

## Register with a client

The project ships a `.mcp.json` that Claude Code auto-loads:

```json
{
  "mcpServers": {
    "sds": {
      "type": "stdio",
      "command": "sds-mcp",
      "args": ["--controller", "${SDS_CONTROLLER_ADDR:-127.0.0.1:3374}"]
    }
  }
}
```

The controller address is **not hard-coded** — it comes from the
`SDS_CONTROLLER_ADDR` environment variable, defaulting to `127.0.0.1:3374`.

## Choosing the controller address

`sds-mcp` runs locally (stdio) and connects to the SDS controller's gRPC port
(default `3374`) over the network. Pick the address by how the cluster is set
up — this is exactly the "resolve it dynamically" rule:

- **Self-HA enabled → use the floating VIP (the service IP).** The controller
  moves between nodes on failover, but the VIP always points at the active one,
  so the VIP never goes stale. Find it with:
  ```bash
  sds-cli ha self status        # -> VIP, e.g. 192.168.123.250/24
  export SDS_CONTROLLER_ADDR=192.168.123.250:3374
  ```
- **No Self-HA → use the controller node's own IP**, e.g.
  `export SDS_CONTROLLER_ADDR=10.0.0.5:3374`.
- **Running `sds-mcp` on a controller node itself → nothing to set**; the
  `127.0.0.1:3374` default connects to the local controller.

After changing `SDS_CONTROLLER_ADDR` (or `.mcp.json`), reload the MCP client
(restart Claude Code) so the server reconnects with the new address — the config
is read once at startup, not hot-reloaded.

## Flags

- `--controller, -c` — controller `host:port` (default `127.0.0.1:3374`).
- `--token` — API token when `[auth]`/`[rbac]` is enabled (also `SDS_TOKEN` env,
  `~/.sds/token`, `/etc/sds/token`).
- `--read-only` — register only read-only tools (list / status / health); use
  this when you want an agent that can inspect but not mutate the cluster.
- `--debug` — debug logging on stderr.
