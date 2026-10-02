# SDS MCP server (for AI agents)

`sds-mcp` (in `cmd/mcp`) exposes the SDS controller as a
[Model Context Protocol](https://modelcontextprotocol.io) server, so an AI
agent can inspect and operate the cluster through tools. It talks to the
controller's gRPC API, the same one `sds` uses. It runs in three ways:

| Command | Transport | Who may call what |
| ------- | --------- | ----------------- |
| `sds-mcp` | stdio, for a client on the same machine | every tool, or only reads with `--read-only` |
| `sds-mcp serve` | HTTP at `/mcp`, for remote clients | decided by the role of each client's token |
| `sds-mcp k8s` | stdio | the Kubernetes tools (`sds_k8s_*`), against a Kubernetes API server instead of the controller |

## Install

```bash
make install-mcp        # runs `make build` (needs Node.js for the web UI), copies bin/sds-mcp to /usr/local/bin
# or just the binary:
go build -o ~/.local/bin/sds-mcp ./cmd/mcp
```

## Local server (stdio)

```bash
claude mcp add sds -- sds-mcp --controller 192.168.123.250:3374
```

The repository's `.mcp.json`, which Claude Code loads in this directory, does
the same for one fixed address:

```json
{
  "mcpServers": {
    "sds": {
      "type": "stdio",
      "command": "sds-mcp",
      "args": ["--controller", "192.168.123.250:3374"],
      "env": {}
    }
  }
}
```

Change the address to your cluster's. Claude Code expands environment
variables in `.mcp.json`, so `"${SDS_CONTROLLER_ADDR:-127.0.0.1:3374}"` also
works there. The client reads the file once at startup: restart it after a
change.

Which address to use:

- **Self-HA enabled:** the VIP. The controller moves between nodes on
  failover; the VIP follows it. `sds ha self status` prints it.
- **No Self-HA:** the controller node's own address.
- **On the controller node itself:** nothing; the default `127.0.0.1:3374`
  reaches it.

Flags:

- `--controller, -c` — controller `host:port` (default `127.0.0.1:3374`).
- `--token` — API token when the controller has `[auth]` enabled. Without the
  flag: `SDS_TOKEN`, then `~/.sds/token`, then `/etc/sds/token`.
- `--read-only` — register only the read-only tools.
- `--allow NAME[,NAME]` — register these mutating tools as well, e.g.
  `--allow sds_ha_evict`. Implies `--read-only`. The server refuses to start
  on a name that is not a tool.
- `--debug` — debug logging on stderr. stdout carries the protocol.

`sds-mcp` connects to the controller without TLS. It has no `--tls-*` options
and does not read `SDS_TLS*`, so it cannot reach a controller whose gRPC port
requires TLS.

### Kubernetes tools

`sds-mcp k8s` serves `sds_k8s_app_list` and `sds_k8s_app_create` (a database
on Kubernetes whose data lives on an SDS volume). It uses `--kubeconfig`
(or `SDS_KUBECONFIG`), or the in-cluster config inside a pod, and takes
`--read-only`, `--allow` and `--debug` like the main server.

## Remote server (`sds-mcp serve`)

`sds-mcp serve` serves the same tools over streamable HTTP at `/mcp`. Every
request needs a token.

```bash
sds-mcp token create --name laptop --role read --url https://mcp.example.com/mcp
sds-mcp serve --listen 0.0.0.0:43871 --public-url https://mcp.example.com
```

`token create` prints the secret once, with the `claude mcp add` command for
it. `--expires 720h` limits its life; the default `0` never expires.

### Roles

A token has a role, and the role decides which tools exist on that
connection. A tool that is not registered cannot be called by name.

| Role | Tools |
| ---- | ----- |
| `read` | the read-only tools: lists, status, health, diagnose, events, logs, audit, runbooks |
| `operate` | plus every mutating tool not marked destructive (the middle column below) |
| `admin` | plus the destructive ones: delete, remove, restore, stop, evict, drain, renumber, Self-HA enable/disable, DR failback, and the gateway export/LUN/ACL tools |

`operate` is not a no-interruption role: it includes `sds_resource_set_role`,
`sds_resource_unmount`, `sds_resource_set_options` and `sds_resource_tls`,
which demote, unmount or reconnect.

`--max-role read|operate|admin` caps every token on the main listener, whatever
the token says. `--admin-listen ADDR` opens a second listener that
`--max-role` does not cap, for operators on the local network: an admin token
there gets every tool while the listener a reverse proxy points at stays
capped. It takes bearer tokens only, no OAuth, and must never be proxied. Bind
it to a port the proxy does not forward, e.g.
`--listen 0.0.0.0:43871 --max-role operate --admin-listen 0.0.0.0:43872`.

### Tokens

Tokens are stored as SHA-256 hashes in `/var/lib/sds/mcp/tokens.json`
(`--tokens` or `SDS_MCP_TOKENS` to change it). `/var/lib/sds` is the Self-HA
mount, so the store moves with the controller; run `sds-mcp token` commands on
the node where sds-meta is mounted. `sds-mcp token list` and
`sds-mcp token revoke <name|id>` work while the server runs: the server
re-reads the file when it changes, and a revoked token is refused on its next
request, including inside an open session. Revoking a token also revokes every
OAuth token issued under it. The server starts with an empty store, logs a
warning, and refuses every request until a token exists.

### Clients

**Claude Code** sends the token as a header:

```bash
claude mcp add --transport http sds https://mcp.example.com/mcp \
    --header "Authorization: Bearer sdsmcp_..."
```

**ChatGPT and claude.ai** add a server by URL and run OAuth, which the server
offers only with `--public-url`. The client discovers the endpoints and
registers itself; on the authorization page you paste a token made with
`token create`. The client gets an access token valid for one hour and a
refresh token valid for 30 days, with the pasted token's role, capped by
`--max-role`, or lower if it asked for a smaller scope. Only S256 PKCE is
accepted, and redirect URIs must be `https`, or `http` to a loopback address.

### Deployment

Serve it over HTTPS: either `--tls-cert`/`--tls-key`, or terminate TLS in a
reverse proxy, bind to a private address and pass `--trust-proxy` so the
failed-login limit (an address is locked out after 10 failed attempts within
5 minutes) sees the real client address from `X-Forwarded-For`. `GET /healthz` answers `ok` without a
token.

`serve` reaches the controller with `--controller` (default `127.0.0.1:3374`)
and `--controller-token` (same fallbacks as `--token` above).

Every tool call is logged with the token's name and role; arguments are not,
since some carry secrets:

```bash
journalctl -u sds-mcp-http | grep 'tool call'
```

To make the server follow the controller across failover, install
`configs/sds-mcp-http.service` on every node, left disabled. It runs
`/opt/sds/bin/sds-mcp serve --listen 0.0.0.0:43871` with the token store on the
Self-HA mount, and reads site flags from `SDS_MCP_ARGS` in
`/var/lib/sds/mcp/sds-mcp.env`. Then put `sds-mcp-http.service` in the sds-meta
promoter's start list: before `ha self enable`, through
`[self_ha] extra_services` in `controller.toml`; on a running cluster, in
`/etc/drbd-reactor.d/sds-ha-sds-meta.toml` on every node.

## Runbooks

The tools say what can be done; runbooks say in what order, and what fails
halfway if the order is wrong. They are embedded from
`pkg/mcpserver/runbooks/*.md` and served two ways:

- as MCP prompts, which Claude Code offers as slash commands, with an optional
  `target` argument (the resource or node);
- through the read-only tool `sds_runbook`: no name lists them, a name returns
  one. ChatGPT, claude.ai and the AI Copilot reach them this way.

| Runbook | For | Needs |
| ------- | --- | ----- |
| `add-replica` | another full copy of a resource on another node | operate |
| `dr-failback` | a WAN resource back to its primary site after a DR failover | admin |
| `grow-volume` | a larger volume, and the filesystem on it | operate |
| `planned-switchover` | sds-meta or a gateway moved to another node on purpose | admin |
| `reboot-node` | a node taken out of service and brought back | admin |
| `renumber-nodes` | nodes whose IP addresses changed | admin |
| `verify-and-repair` | replicas compared block by block, and differences repaired | operate |

## Tools

| Area | read | operate adds | admin adds |
| ---- | ---- | ------------ | ---------- |
| Cluster, nodes | `sds_node_list`, `sds_node_health_check`, `sds_diagnose`, `sds_event_list`, `sds_log_list`, `sds_audit_list`, `sds_ocf_agent_list`, `sds_ocf_agent_metadata`, `sds_notify_channel_list`, `sds_replication_tls_status`, `sds_runbook` | `sds_node_register`, `sds_node_set_labels`, `sds_node_undrain`, `sds_notify_channel_test` | `sds_node_drain`, `sds_node_unregister`, `sds_node_set_address` |
| LVM pools | `sds_pool_list` | `sds_pool_create`, `sds_pool_add_disk`, `sds_pool_add_cache` | `sds_pool_delete`, `sds_pool_remove_cache`, `sds_pool_convert_thin` |
| ZFS | `sds_zfs_pool_list` | `sds_zfs_volume_create`, `sds_zfs_volume_resize`, `sds_zfs_dataset_create`, `sds_zfs_snapshot_clone` | `sds_zfs_pool_delete`, `sds_zfs_dataset_delete` |
| Resources, volumes | `sds_resource_list`, `sds_resource_status` | `sds_resource_create`, `sds_resource_adopt`, `sds_resource_add_volume`, `sds_resource_resize_volume`, `sds_resource_set_options`, `sds_resource_set_role`, `sds_resource_mount`, `sds_resource_unmount`, `sds_resource_dual_primary`, `sds_resource_repair`, `sds_resource_verify`, `sds_resource_tls` | `sds_resource_delete`, `sds_resource_remove_volume`, `sds_resource_create_filesystem` |
| Replicas, WAN | | `sds_resource_add_replica`, `sds_resource_attach_diskless`, `sds_resource_detach_diskless`, `sds_resource_set_tiebreaker`, `sds_resource_add_dr`, `sds_wan_repair`, `sds_wan_set_endpoint` | `sds_resource_remove_replica`, `sds_resource_dr_failback` |
| Profiles | `sds_resource_profile_list`, `sds_resource_profile_get`, `sds_resource_profile_max_size` | `sds_resource_profile_create`, `sds_resource_profile_set_options`, `sds_resource_profile_adjust`, `sds_resource_set_profile` | `sds_resource_profile_delete` |
| Snapshots | `sds_snapshot_list`, `sds_snapshot_schedule_list` | `sds_snapshot_create`, `sds_snapshot_schedule_create` | `sds_snapshot_delete`, `sds_snapshot_restore`, `sds_snapshot_schedule_delete` |
| Backups | `sds_backup_list`, `sds_backup_target_list`, `sds_backup_schedule_list` | `sds_backup_create`, `sds_backup_import`, `sds_backup_schedule_create`, `sds_backup_schedule_delete` | `sds_backup_delete`, `sds_backup_restore`, `sds_backup_target_delete` |
| Gateways | `sds_gateway_list`, `sds_gateway_get` | `sds_gateway_create_nfs`, `sds_gateway_create_iscsi`, `sds_gateway_create_nvme`, `sds_gateway_start`, `sds_iscsi_chap` | `sds_gateway_stop`, `sds_gateway_delete`, `sds_nfs_exports`, `sds_iscsi_luns`, `sds_iscsi_initiators`, `sds_nvme_namespaces`, `sds_nvme_hosts` |
| HA, Self-HA | `sds_ha_list`, `sds_ha_status`, `sds_ha_promoter_status`, `sds_ha_get_toml`, `sds_self_ha_status` | `sds_ha_create` | `sds_ha_evict`, `sds_ha_delete`, `sds_ha_sync_toml`, `sds_self_ha_enable`, `sds_self_ha_disable` |

`sds_nfs_exports`, `sds_iscsi_luns`, `sds_iscsi_initiators`,
`sds_nvme_namespaces` and `sds_nvme_hosts` each take an action (list, add,
remove) and are marked destructive as a whole, so listing exports, LUNs or
ACLs needs `admin`. `sds_iscsi_chap` (get, set) needs `operate` for either.

### What has no tool

- Commands that take a secret, since a tool argument is recorded by whatever
  called it: `backup target add` (storage credentials), `channel add` (webhook
  URLs and signing keys), `rbac`. The exception is `sds_iscsi_chap`, which sets
  a CHAP password; the server does not log arguments and never returns the
  password.
- `resource dr-failover`: it force-promotes the DR node and loses the writes
  still in the WAN buffer, a decision to take at the CLI with `--yes`.
- `replication-tls setup`: it installs a CA into each node's system trust
  store.
- `backup schedule run`, `channel delete`, `event watch` (a stream;
  `sds_event_list` reads the history), `gateway nfs mount` (mounts on the
  machine running the CLI).
