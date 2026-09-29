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

## What is deliberately not a tool

Anything that takes a secret has no tool, because a tool argument is recorded
by whatever called it: `backup target add`, `channel add` (webhook URLs and
keys), `gateway iscsi chap set`, `rbac`. `resource dr-failover` is not offered
either: it force-promotes the DR node and loses the writes still in the WAN
buffer, which is a decision to take at the CLI, knowingly, with `--yes`.
Everything else the CLI does has a tool.

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

## Remote access (Claude Code, ChatGPT, claude.ai)

`sds-mcp serve` serves the same tools over HTTP at `/mcp`, behind token
authentication, so a client that is not on the cluster can use them.

```bash
sds-mcp token create --name laptop --role read --url https://mcp.example.com/mcp
sds-mcp serve --listen 0.0.0.0:43871 --public-url https://mcp.example.com
```

A token has a **role**, and the role decides which tools exist on that
connection — a tool that is not there cannot be called by name:

| Role | Can |
| ---- | --- |
| `read` | list, status, health, diagnose. Changes nothing |
| `operate` | plus create, grow, snapshot, start, mount. Nothing that deletes data or interrupts service |
| `admin` | everything, including delete, restore, evict, drain |

`--max-role` caps every token on a given server, whatever the token says: a
server reachable from the internet can be held to `operate`, or `read`.

`--admin-listen ADDR` opens a second listener that `--max-role` does not cap,
for operators on the local network: an admin token there gets every tool, while
the main listener (the one a reverse proxy points at) stays capped. It takes
bearer tokens only, with no OAuth, and must never be proxied or published. Bind
it to a port the proxy is not configured for, e.g.
`--listen 0.0.0.0:43871 --max-role operate --admin-listen 0.0.0.0:43872`.

Tokens are shown once and stored only as a hash, in
`/var/lib/sds/mcp/tokens.json` (`--tokens` or `SDS_MCP_TOKENS` to change it),
on the Self-HA mount so they follow the server. `sds-mcp token list` and
`sds-mcp token revoke <name>` work while the server runs; a revoked token is
refused on its next request, including one already inside a session. Give each
client its own token and an `--expires` you can live with.

**Claude Code** takes the token as a header:

```bash
claude mcp add --transport http sds https://mcp.example.com/mcp \
    --header "Authorization: Bearer sdsmcp_..."
```

**ChatGPT and claude.ai** add a server by URL and then run OAuth, which needs
`--public-url`. They discover the endpoints and register themselves; on the
authorization page you paste a token made with `token create`, and the client
is given a short-lived token of that role (or lower, if it asked for less).
Revoking the token you pasted revokes the client. Only S256 PKCE is accepted,
and redirect URIs must be `https` (or `http` to localhost).

Serve it behind HTTPS: either `--tls-cert`/`--tls-key`, or terminate TLS in a
reverse proxy, bind to a private address and pass `--trust-proxy` so the
failed-login limit sees the real client address. Every tool call is logged with
the token's name and role (arguments are not, since some carry secrets):

```bash
journalctl -u sds-mcp-http | grep 'tool call'
```

To make it follow the controller across failover, install
`configs/sds-mcp-http.service` on every node (left disabled, like `sds-ai`) and
add `sds-mcp-http.service` to the sds-meta promoter's start list.
