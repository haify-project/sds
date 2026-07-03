# SDS HA: OCF Agent builder + drbd-reactor TOML editor

Date: 2026-07-03
Status: Approved

## Goal

Bring the two most useful HA-config-editing capabilities from the DRBD-HA
project into sds's HA page, wired to sds's own controller (not DRBD-HA's Rust
backend), keeping sds's existing look/components (no new color palette):

1. **OCF Agent builder** — browse the OCF resource agents available on the
   nodes, pick one, get a parameter form auto-generated from its OCF meta-data,
   fill it in, and compose an ordered list of agents into an HA config's
   drbd-reactor promoter `start = [ ... ]`.
2. **drbd-reactor TOML editor** — view and edit the raw promoter TOML of an HA
   config (`/etc/drbd-reactor.d/sds-ha-<res>.toml`), with a live form↔TOML
   preview during creation, and a Sync action that writes the edited TOML to all
   nodes and reloads drbd-reactor.

Out of scope: DRBD-HA's profiles model, SSE event stream, per-node enable/disable,
multi-service wizard chrome. sds keeps its `HaConfig` model.

## Backend API contract (sds controller)

All under the existing REST prefix `/v1`. gRPC + grpc-gateway as usual.

### 1. List OCF resource agents
`GET /v1/ha/resource-agents` →
```json
{ "agents": [ { "provider": "heartbeat", "name": "Filesystem", "shortdesc": "Manages filesystem mounts" }, ... ] }
```
Implementation: on a reachable node, enumerate `/usr/lib/ocf/resource.d/*/*`
(provider = dir name, name = file name, executable). shortdesc best-effort
(may be "" if not cheaply available at list time). Dedup by provider/name.

### 2. Get an OCF agent's metadata (parameter schema)
`GET /v1/ha/resource-agents/{provider}/{name}` →
```json
{
  "provider": "heartbeat", "name": "IPaddr2", "version": "1.0",
  "shortdesc": "...", "longdesc": "...",
  "parameters": [
    { "name": "ip", "required": true, "unique": true, "type": "string", "default": "", "shortdesc": "IPv4/IPv6 address", "longdesc": "..." },
    { "name": "cidr_netmask", "required": false, "unique": false, "type": "string", "default": "", "shortdesc": "...", "longdesc": "..." }
  ]
}
```
Implementation: run `OCF_ROOT=/usr/lib/ocf /usr/lib/ocf/resource.d/<provider>/<name> meta-data`
on a node, parse the OCF meta-data XML (`<resource-agent>`, `<parameters>` →
`<parameter name unique required>` with `<content type default>`, `<shortdesc>`,
`<longdesc>`). Return a clear error if the agent is missing.

### 3. Read an HA config's promoter TOML
`GET /v1/ha/{resource}/toml` →
```json
{ "resource": "pgha", "path": "/etc/drbd-reactor.d/sds-ha-pgha.toml", "content": "[[promoter]]\n..." }
```
Reads the file on the active/first node. 404 if the resource has no HA config.

### 4. Write (sync) an HA config's promoter TOML
`POST /v1/ha/{resource}/toml`  body `{ "content": "..." }` →
```json
{ "success": true, "message": "toml synced to 3 nodes, drbd-reactor reloaded" }
```
Validate the content is non-empty and contains a `[[promoter]]` table (basic
guard), distribute to all resource nodes at the same path, then reload
drbd-reactor. AllSuccess-checked; clear error listing failed nodes.

### 5. MakeHa generalization: ocf_agents
Extend `MakeHaRequest` with an optional ordered list:
```proto
message OcfAgent {
  string provider = 1;   // e.g. heartbeat
  string name = 2;       // e.g. IPaddr2
  string instance = 3;   // OCF instance id, e.g. vip_pgha
  map<string,string> params = 4;
}
repeated OcfAgent ocf_agents = N; // appended to the promoter start[] after the built-in mount/vip
```
When present, each becomes a `start` entry
`ocf:<provider>:<name> <instance> <k>=<v> ...` in order, composed with the
existing mount/vip/services items. When absent, behavior is unchanged
(back-compat). The generated `.res`/promoter otherwise identical to today.

## Frontend (web-ui, HAPage — sds look, existing shadcn components)

- **OCF Agent editor** (in the Create HA flow): a searchable agent picker
  (from `GET /v1/ha/resource-agents`); on select, fetch metadata and render a
  parameter form (inputs typed by `type`, prefilled with `default`, required
  validation). "Add" appends the configured agent to an ordered list shown as
  rows (with remove + reorder). The list is submitted as `ocf_agents` to MakeHa.
- **TOML editor**: each HA config card gets an expandable "DRBD Reactor Promoter
  Config (<res>.toml)" section: a monospace `<textarea>` loaded from
  `GET /v1/ha/{res}/toml`, with a **Sync** button that POSTs the edited content
  and toasts the result. During creation, a live read-only TOML preview panel
  reflects the form (built client-side; port `extractStartArrayItems`-style
  helpers from DRBD-HA `utils/toml.ts` for parsing the `start` array).
- No new color palette; reuse sds Card/Badge/Button/Input/Select/Dialog/Tabs.

## Testing

- Backend Go unit tests: OCF meta-data XML parser (feed a sample IPaddr2
  meta-data XML → assert parsed parameters); resource-agents listing parse;
  toml sync validation (rejects empty / no-`[[promoter]]`); MakeHa composes
  `ocf_agents` into the start list (assert generated promoter content).
- Frontend: build + tsc clean; component renders the agent form from sample
  metadata; TOML editor loads + sync calls the endpoint.
- Live (parent): on the cluster, list agents, open IPaddr2 metadata, view pgha's
  TOML, and create a small HA config with an extra OCF agent; verify via
  Playwright + the reactor config on the node.

## Build order

1. Backend endpoints 1–4 (read-only + toml sync) + tests.
2. Backend MakeHa `ocf_agents` extension + tests.
3. Frontend OCF agent editor + TOML editor, wired to the contract above.
4. Deploy + live/Playwright verification.
