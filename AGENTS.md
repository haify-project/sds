# Repository Guidelines

SDS is a lightweight Software Defined Storage controller written in Go, built on DRBD and LVM/ZFS. It consists of a gRPC controller (`sds-controller`), a CLI (`sds-cli`), a Kubernetes CSI driver, and an MCP server for AI assistant integration.

## Project Structure

```
sds/
├── cmd/              # Binary entry points (cli, controller, mcp, csi-controller, csi-node)
├── pkg/              # Core packages
│   ├── controller/   # Business logic: resources, gateways, HA, RBAC, scheduling
│   ├── deployment/   # SSH execution engine (wraps dispatch)
│   ├── gateway/      # iSCSI / NFS / NVMe-oF config generators
│   ├── mcpserver/    # MCP tool definitions
│   ├── csi/          # Kubernetes CSI driver
│   ├── database/     # BoltDB persistence layer
│   ├── config/       # TOML config parsing
│   └── util/         # Shared utilities
├── api/proto/v1/     # gRPC protobuf definitions
├── web-ui/           # React + TypeScript dashboard (Rsbuild, shadcn/ui, Tailwind)
├── ui/               # Go embed wrapper for web-ui/dist
├── configs/          # Example configs and systemd unit files
├── deploy/k8s/       # Kubernetes manifests for CSI deployment
├── scripts/          # Build and deployment helpers
└── docs/             # Architecture docs and design specs
```

Tests live alongside source files as `*_test.go` within each package.

## Build, Test, and Development Commands

```bash
make build          # Compile all binaries to bin/ (also builds web-ui)
make test           # Run full Go test suite (go test -v ./...)
make fmt            # Format Go source (gofmt -s)
make lint           # Run golangci-lint
make proto          # Regenerate gRPC code from api/proto/v1/
make clean          # Remove bin/ and ui/dist

# Run locally
make run-controller # go run ./cmd/controller --config configs/controller.toml

# Web UI
cd web-ui && npm run dev    # Dev server (hot reload)
cd web-ui && npm run build  # Production build → web-ui/dist
make ui-sync                # Copy web-ui/dist into ui/dist for go:embed
```

> `make test` calls `make ui-ensure` first, which creates a placeholder `ui/dist` if the web UI has not been built — Go tests run correctly without Node.js.

## Coding Style & Naming Conventions

- **Go**: standard `gofmt`/`goimports` formatting; run `make fmt` before committing.
- **Linting**: `golangci-lint`; run `make lint` and fix all reported issues.
- **Packages**: lower-case, single-word names matching the directory (e.g., `package controller`).
- **Exported symbols**: follow Go convention — `PascalCase` for types/functions, `camelCase` for unexported.
- **Proto files**: snake_case field names; service method names are `PascalCase` verb+noun (e.g., `CreateResource`).
- **Web UI**: TypeScript strict mode; components in `web-ui/src/components/`, pages in `web-ui/src/pages/`, API calls in `web-ui/src/services/`.

## Testing Guidelines

- Framework: standard `testing` package with `testify/assert` and `testify/require`.
- Test files sit next to the file under test: `gateway.go` → `gateway_test.go`.
- Test function names follow `TestFunctionName_Scenario` (e.g., `TestPlacement_RackAwareSpread`).
- Run a single package: `go test -v ./pkg/controller/`.
- CSI conformance tests: `scripts/csi-e2e.sh` (requires a live cluster).
- No minimum coverage threshold is enforced, but new business logic in `pkg/controller/` and `pkg/gateway/` is expected to have unit tests.

## Commit & Pull Request Guidelines

Commit messages follow **Conventional Commits**:

```
feat(gateway): add NVMe-oF namespace resize support
fix(csi): correct topology key for single-node clusters
docs(wan): update Phase 1 implementation status
```

- Use `feat`, `fix`, `docs`, `refactor`, `test`, or `chore` as the type.
- Scope matches the affected package or subsystem (e.g., `controller`, `csi`, `ha`, `wan`).
- Keep the subject line under 72 characters; add a body for non-obvious changes.
- PRs should reference a related issue or design doc when one exists.
- Run `make fmt`, `make lint`, and `make test` locally before opening a PR.

## Configuration & Secrets

- The controller config lives at `/etc/sds/controller.toml`; use `configs/controller.toml.example` as the starting point.
- API tokens are resolved in order: `--token` flag → `SDS_TOKEN` env → `~/.sds/token` → `/etc/sds/token`.
- Never commit real tokens, SSH keys, or host addresses; use the example configs in `configs/`.
