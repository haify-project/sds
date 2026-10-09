# Haify HA: OCF Agent builder + drbd-reactor TOML editor

Date: 2026-07-03
Status: Implemented

## Goal

Two HA-config editing capabilities, taken from the DRBD-HA project and wired to
the Haify controller:

1. **OCF agent builder** — browse the OCF resource agents installed on the
   nodes, pick one, fill in a parameter form generated from its OCF meta-data,
   and place it in an HA config's drbd-reactor promoter `start = [ ... ]` list.
2. **Promoter TOML editor** — view and edit the raw promoter TOML of an HA
   config (`/etc/drbd-reactor.d/sds-ha-<res>.toml`) and sync it to the
   resource's nodes.

DRBD-HA's profiles model, SSE event stream and per-node enable/disable were not
taken over. Haify keeps its own `HaConfig` model.

## Backend API

gRPC with grpc-gateway REST mappings under `/v1`. Implementation in
`pkg/controller/ha_ocf.go` and `pkg/controller/server_ha.go`.

### List OCF resource agents

`ListResourceAgents` — `GET /v1/ha/resource-agents` →
`{ "agents": [ { "provider", "name", "shortdesc" } ] }`.

Enumerates the executable files under `/usr/lib/ocf/resource.d/*/*` on the
first reachable node (provider = directory, name = file), deduplicated.

### Get an agent's parameter schema

`GetResourceAgentMetadata` — `GET /v1/ha/resource-agents/{provider}/{name}` →
`provider`, `name`, `version`, `shortdesc`, `longdesc`, and `parameters[]`
(`name`, `required`, `unique`, `type`, `default`, `shortdesc`, `longdesc`).

Runs `OCF_ROOT=/usr/lib/ocf /usr/lib/ocf/resource.d/<provider>/<name> meta-data`
on a node and parses the OCF meta-data XML. Provider and name are validated
before anything runs.

### Read an HA config's promoter TOML

`GetHaToml` — `GET /v1/ha/{resource}/toml` →
`{ "resource", "path", "content" }`. Tries the resource's nodes in order and
returns the first copy found; NotFound if no node has the file.

### Write (sync) an HA config's promoter TOML

`SyncHaToml` — `POST /v1/ha/{resource}/toml` with `{ "content": "..." }`.
Rejects empty content and content without a `[[promoter]]` table, distributes
the file to the resource's nodes at the same path, then reloads drbd-reactor.
A reload failure lists the failed nodes. Response message:
`toml synced to N nodes, drbd-reactor reloaded`.

### MakeHa: OCF agents and ordered start items

`MakeHaRequest` fields:

```proto
repeated string services = 2;
string mount_point = 3;
string fstype = 4;
string vip = 5;
repeated OcfAgent ocf_agents = 6;     // provider, name, instance, params
repeated HaStartItem start_items = 7; // oneof { string systemd_unit; OcfAgent ocf; }
```

An OCF agent renders as `ocf:<provider>:<name> <instance> <k>=<v> ...` with
parameters in sorted key order.

- **`start_items` set:** the promoter `start` list is exactly these items in
  this order, systemd/mount units and OCF agents interleaved. This is what
  allows stacks such as portblock → Filesystem → IPaddr2 → nfsserver →
  exportfs → portunblock. `mount_point`/`fstype`/`vip` are then used only for
  provisioning side effects (mount unit generation, the VIP's `service-ip`
  unit), not for ordering.
- **`start_items` empty:** legacy order — mount unit, then
  `service-ip@<ip>-<mask>.service` for the VIP, then `services`, then
  `ocf_agents`.

The generated promoter uses `runner = "systemd"` and
`on-drbd-demote-failure = "reboot"`.

## Web UI

- `web-ui/src/pages/CreateHAPage.tsx` — the create flow. The operator builds an
  ordered list of items (service, mount, VIP, OCF agent); OCF agents come from
  `components/OcfAgentBuilder.tsx` (searchable agent list, parameter form typed
  and prefilled from the metadata, required-field validation). A read-only
  "DRBD Reactor Promoter Config (preview)" panel renders the TOML client-side
  (`lib/toml.ts`). Submission sends `start_items` (plus the first mount and VIP
  for side effects).
- `web-ui/src/pages/ha/TomlEditorSection.tsx`, inside each HA config card
  (`PromoterCard.tsx`) — loads the TOML via `GET /v1/ha/{res}/toml` into a
  monospace editor; **Sync** posts the edited content and shows the result.
