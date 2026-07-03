# SDS AI Copilot Panel — Design

Date: 2026-07-03
Status: Approved (pending spec review)

## Overview

Add an **AI Copilot** to the existing SDS web-ui: a chat sidebar backed by an
agent that is aware of the live SDS cluster (via the `sds-mcp` tools) and grounded
in DRBD / drbd-reactor domain knowledge (the `drbd-reactor.db` cortexdb store).
The agent answers/diagnoses read-only, and for changes it **proposes** guarded
actions that the operator approves with one click; approved actions execute
through the sds controller REST the web-ui already uses.

The agent runtime is **agent-go** (ReAct core). **oss-agent** is consumed as a
Go library (root facade `github.com/liliang-cn/oss-agent`, `package ossagent`,
already importable as of v0.3.0) which wraps agent-go with knowledge_search,
red-line safety, domain config, and log triage. This design adds MCP-tool support
to that library (v0.4.0) and builds the SDS-side consumer.

### Goals

- Operator can ask natural-language questions about the SDS cluster and DRBD/
  drbd-reactor behavior and get answers grounded in **real cluster state** +
  **domain knowledge**, with cited sources and visible tool use.
- Operator gets **guarded, human-approved** remediation: the agent proposes
  concrete actions (with reasons); nothing changes the cluster without an
  explicit click, and every proposed change passes the red-line wall first.
- Reuse: agent-go as core; oss-agent as the library; the existing sds-mcp;
  the existing sds web-ui controller REST for execution.

### Non-goals

- No fully autonomous remediation (agent never executes writes on its own).
- No new operations backend: approved actions reuse existing controller REST.
- No re-implementation of the agent loop, knowledge store, or safety wall —
  those come from oss-agent/agent-go.

## Architecture

```
 sds web-ui (existing React) ── new "AI Copilot" sidebar ──────────┐
   │  POST /ai/chat/stream  (NDJSON, tool-use + suggestions streamed)│
   ▼                                                                 │
 cmd/sds-ai  (NEW small binary in the sds repo that IMPORTS the      │
   ossagent library — NOT oss-agent's stock `serve`)                 │
   agent-go ReAct core (via ossagent.New)                            │
     ├─ knowledge_search  → cortexdb: drbd-reactor.db (1024 chunks)  │
     ├─ sds-mcp READ-ONLY tools (list/status/health) → situational   │
     │        awareness                                              │
     └─ emits structured "suggested actions" (never executes writes) │
   red-line wall validates every suggested action                    │
        │                                                            │
   operator clicks "Approve" on a suggestion card ──────────────────┘
        ▼
 sds web-ui calls the EXISTING controller REST (createHA / evict /
 snapshot / …) to execute — same guarded path the UI already uses.
```

### Component boundaries

| Unit | Owner | Purpose | Depends on |
|---|---|---|---|
| oss-agent lib v0.4.0 (MCP support) | **user** (oss-agent repo) | Mount MCP-server tools into the ReAct agent; expose read-only filtering + structured suggestions | agent-go `pkg/mcp`, `pkg/agent` |
| SDS `domain.toml` | me (sds repo) | SDS/DRBD persona, red_lines, knowledge pointer | oss-agent domain schema |
| `cmd/sds-ai` binary (sds repo) | me | Imports the ossagent library; wires sds-mcp + drbd-reactor.db + ai/domain.toml; serves `/ai/chat/stream` (NDJSON). Kept as its own binary (ideally a nested go module) so the AI/vector-DB dep tree never bloats the lean controller. The browser can't import a Go lib, so a host process is always required — this is that host. | oss-agent lib v0.4.0 |
| sds web-ui AI Copilot sidebar | me (sds repo) | Chat (SSE) + suggested-action approval cards | oss-agent serve API; existing controller REST |

### Dependency direction — no import cycles

The two systems are decoupled through **runtime boundaries (MCP wire protocol +
HTTP)**, never Go-module imports. This is a hard architectural constraint.

```
 Compile-time imports (Go module level):
   oss-agent  ──imports──▶ agent-go, cortexdb     (never imports sds)
   sds        ──imports──▶ own pkg + MCP SDK       (never imports oss-agent)
      sds-mcp = cmd/mcp inside the sds module; depends one-way on sds internals.

 Runtime edges (protocol, NOT imports):
   sds web-ui ──HTTP/SSE──▶ oss-agent serve       (separate process)
   oss-agent  ──MCP───────▶ sds-mcp               (separate/subprocess)
   sds web-ui ──HTTP──────▶ sds controller REST   (existing)
```

Neither module imports the other, so no cycle is possible. The two cycle-forming
shapes are **explicitly forbidden**:

1. oss-agent MUST NOT `import github.com/liliang-cn/sds` — it reaches the cluster
   only through the sds-mcp wire protocol (tool name/schema, no Go coupling).
2. sds MUST NOT `import github.com/liliang-cn/oss-agent` — the AI backend runs as
   a separate `oss-agent serve` process reached over HTTP.

If a future iteration embeds the agent in-process (agent-go `pkg/mcp/inprocess`),
the same rule holds: dependency stays one-way `sds → oss-agent`, with sds passing
its own in-process MCP server to oss-agent via an **interface** — oss-agent still
never imports sds. The `sds → sds-mcp` edge always stays on the wire.

## oss-agent library v0.4.0 — API contract (for the user to implement)

This is what the SDS side needs from the library. Names are proposals; the user
may adjust as long as the capabilities exist.

### 1. MCP servers in `Config`

```go
type MCPServerSpec struct {
    Name      string            // logical name, e.g. "sds"
    Transport string            // "stdio" | "http" | "sse"
    Command   string            // stdio: executable, e.g. "sds-mcp"
    Args      []string          // stdio: e.g. ["--controller","192.168.123.250:3374"]
    URL       string            // http/sse: base URL
    Headers   map[string]string // http/sse: optional
    // ReadOnly restricts which of this server's tools are mounted.
    // When true, only tools whose name matches ReadOnlyToolPrefixes (or a
    // library default read-only allowlist) are exposed to the agent.
    ReadOnly            bool
    ReadOnlyToolAllow   []string // explicit allowlist (optional; overrides heuristic)
}

type Config struct {
    // ... existing fields ...
    MCPServers []MCPServerSpec
}
```

On `New(cfg)`: for each `MCPServerSpec`, connect an agent-go `pkg/mcp.Client`,
enumerate `GetTools()`, filter to the read-only set, and register each with
`agent.AddTool(name, description, params, handler)` where the handler calls
`client.CallTool`. Tool call/return already surface through the existing `Stream`
`Event` (`EventToolCall` / `EventToolResult`), so the sidebar can render them.

**Read-only enforcement (belt and suspenders):** the SDS side will pass only
read-only tools via allowlist, AND the sds-mcp write tools stay unmounted. The
library must not mount a write tool when `ReadOnly` is set.

### 2. Structured suggested actions

The agent must be able to surface a machine-readable action proposal (so the
sidebar can render an approve button rather than parse prose). Two acceptable
shapes — user picks whichever fits oss-agent best:

- **(preferred) A `suggest_action` tool** the agent can call, whose args are the
  structured proposal; the library forwards it as a new `Event` kind
  `EventSuggestion` (with `Tool="suggest_action"`, `Args=<proposal>`), and does
  **not** execute anything. OR
- **A convention**: the agent emits a fenced ```action json block in its answer;
  the library parses trailing action blocks into a `Suggestions []Suggestion`
  return value / `EventSuggestion` events.

Proposal payload (what the SDS side expects to receive):

```json
{
  "action": "ha.evict",            // maps to an sds controller REST operation
  "params": { "resource": "pgha" },
  "reason": "pgha primary is on an overloaded node; evict to rebalance",
  "severity": "medium"             // informs the confirm UI + red-line gate
}
```

The library does **not** need to know how to execute the action — execution is
the SDS web-ui's job. The library's responsibility ends at: validate the
proposal string form against the red-line wall (`CheckCommand`-style) and emit it.

### 3. Knowledge base = drbd-reactor.db

No new API needed — `Config.KnowledgeDBPath = ".../drbd-reactor.db"`. **Integration
constraint:** `Config.EmbModel` / `Config.EmbDim` MUST match the embedder that
built the index. The index vectors are **1024-dim** (blob stores 1024 + 1 norm
float). `knowledge.db.ollama768.bak` implies a migration off ollama-768; the
current drbd-reactor.db was built with a 1024-dim model (e.g. mxbai-embed-large
/ bge-class). **Open item O1** below — user confirms the exact model+dim.

## Data flow

### Read-only Q&A / diagnose
1. Sidebar `POST /ai/chat/stream` with the question + session id.
2. agent-go ReAct: may call sds-mcp read tools (e.g. `sds_ha_status`,
   `sds_resource_list`) and `knowledge_search` over drbd-reactor.db.
3. Tool calls/results and text stream back as SSE events; sidebar renders the
   chain of tool use + the grounded answer with cited sources.

### Plan-then-Approve (guarded write)
1. During Q&A/diagnose the agent decides a change is warranted and emits a
   **suggested action** (does not execute).
2. Sidebar renders a suggestion card: action + params + reason + severity.
   The card is disabled/blocked if the red-line wall rejects it.
3. Operator clicks **Approve** → sidebar calls the **existing controller REST**
   endpoint the web-ui already uses for that operation (e.g. the same call
   behind the HA "Evict" button).
4. Result (success/failure) is shown; optionally fed back into the chat as
   context for the next turn.

This keeps writes on the existing, tested, guarded path; the agent only ever
observes and proposes.

## Safety / red-line

- sds-mcp write tools are **never mounted** into the agent (read-only allowlist).
- Every suggested action is checked against the domain `red_lines` before the
  approve button is enabled (defense in depth with the human click).
- The SDS `domain.toml` red_lines encode destructive one-liners (e.g. `drbdadm
  down`, `lvremove -f`, `zpool destroy`, `mkfs` on a mounted/primary device,
  `wipefs`) at the appropriate severity.

## Error handling

- MCP connect failure at `serve` startup: log + continue **degraded**
  (knowledge-only); the sidebar shows a "cluster tools unavailable" banner
  rather than failing chat.
- A read tool erroring mid-loop: surfaced as an `EventToolResult` error; the
  agent reasons around it; the answer notes the gap.
- Approve/execute failure: shown on the card with the controller error; no
  silent success.
- Embedding-dim mismatch (O1 wrong): `knowledge_search` returns garbage/empty —
  caught at bring-up by a smoke query, not in production.

## Testing

1. **Lib (user, oss-agent repo):** unit test that a stub MCP server's read tools
   register into the agent and a write tool is refused under `ReadOnly`.
2. **Knowledge smoke:** `oss-agent search "drbd-reactor promoter"` over
   drbd-reactor.db returns relevant chunks (validates O1 embedder match).
3. **Read-only E2E:** ask "why does pgha have no services?" → agent calls
   `sds_ha_status` + knowledge_search → grounded answer (Playwright, screenshots).
4. **Plan-then-Approve E2E:** drive a scenario where the agent proposes
   `ha.evict` → approve in sidebar → existing REST executes → verify failover
   (Playwright, screenshots), matching the visible-process requirement.

## Phasing & division of labor

- **P0 (user, oss-agent repo):** v0.4.0 — `Config.MCPServers` + read-only tool
  mounting + structured suggestions + red-line check on suggestions. Publish tag.
- **P1 (me, sds repo):** SDS `domain.toml` (persona, red_lines, knowledge path);
  bring up `oss-agent serve` (SDS profile) against sds-mcp + drbd-reactor.db;
  knowledge smoke test.
- **P2 (me, sds repo):** sds web-ui AI Copilot sidebar — SSE chat + tool-use
  rendering + suggested-action approval cards wired to existing controller REST.
- **P3 (me):** read-only E2E, then plan-then-approve E2E on the orange cluster.

I hand P0's contract (this section) to the user; P1–P3 wait on the v0.4.0 tag,
but P1's domain.toml + P2's sidebar UI can be drafted in parallel against a stub.

## Open items

- **O1 (user):** confirm the exact embedder **model + dimension** used to build
  `drbd-reactor.db` (index is 1024-dim), so `Config.EmbModel/EmbDim` match.
- **O2:** where does `oss-agent serve` run for SDS — on the controller VIP host,
  or a separate box? (Affects sds-mcp transport: stdio local vs http remote.)
- **O3:** the mapping table from `suggested action` → controller REST endpoint
  (which actions are approvable in v1: likely ha.evict, ha.create, snapshot.create,
  resource ops — start small).
```
