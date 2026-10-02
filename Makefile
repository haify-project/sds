.PHONY: kb build test clean install-controller install-cli install-mcp run-controller run-cli proto web-ui web-ui-dev web-ui-build ui-sync ui-ensure hooks ci

# Sync the freshly built web UI into ui/dist for go:embed. The directory is
# gitignored and intentionally kept around after builds so plain `go build`
# and `go test ./...` keep working without re-running npm.
#
# .gitkeep is restored after the copy: it is the one tracked file in ui/dist,
# and it is what lets go:embed resolve on a fresh clone. Dropping it would both
# break that and show up as a spurious deletion in `git status` after a build.
ui-sync: web-ui-build
	@echo "Preparing UI for embedding..."
	@rm -rf ui/dist
	@cp -r web-ui/dist ui/
	@touch ui/dist/.gitkeep

# Ensure ui/dist exists so go:embed (ui/ui.go) compiles. Prefers the real
# web-ui build output; falls back to a clearly marked placeholder so Go
# tests can run on machines without Node.js.
ui-ensure:
	@if [ ! -f ui/dist/index.html ]; then \
		if [ -d web-ui/dist ]; then \
			echo "Syncing ui/dist from existing web-ui/dist..."; \
			rm -rf ui/dist && cp -r web-ui/dist ui/; \
		else \
			echo "web-ui/dist not found; writing placeholder ui/dist (run 'make build' for the real UI)"; \
			mkdir -p ui/dist; \
			echo '<!DOCTYPE html><html><body>SDS UI placeholder - run make build to embed the real UI</body></html>' > ui/dist/index.html; \
		fi \
	fi
	@touch ui/dist/.gitkeep

# Build binaries
build: ui-sync
	@echo "Building sds-controller..."
	go build -o bin/sds-controller ./cmd/controller
	@echo "Building sds..."
	go build -o bin/sds ./cmd/cli
	@echo "Building sds-mcp..."
	go build -o bin/sds-mcp ./cmd/mcp
	GOOS=linux go build -o bin/service-ip ./cmd/service-ip
	go build -o bin/csi-controller ./cmd/csi-controller
	go build -o bin/csi-node ./cmd/csi-node

# Run tests
test: ui-ensure
	go test -v ./...

# Clean build artifacts
clean:
	rm -rf bin/ ui/dist

# Install controller systemd service
install-controller: build
	@echo "Installing sds-controller..."
	sudo mkdir -p /opt/sds/bin /etc/sds
	sudo cp bin/sds-controller bin/service-ip /opt/sds/bin/
	sudo install -m 755 bin/service-ip /usr/local/bin/service-ip
	sudo cp configs/sds-controller.service configs/service-ip@.service /etc/systemd/system/
	sudo cp configs/controller.toml.example /etc/sds/controller.toml.example
	sudo systemctl daemon-reload
	@echo "Controller installed. Copy /etc/sds/controller.toml.example to"
	@echo "/etc/sds/controller.toml, edit it, then run:"
	@echo "  sudo systemctl enable --now sds-controller"

# Install CLI
install-cli: build
	@echo "Installing sds..."
	sudo install -m 755 bin/sds /usr/local/bin/sds
	sudo ln -sf sds /usr/local/bin/sds-cli
	@echo "CLI installed to /usr/local/bin/sds (sds-cli links to it)"

# Install MCP server
install-mcp: build
	@echo "Installing sds-mcp..."
	sudo cp bin/sds-mcp /usr/local/bin/
	@echo "MCP server installed to /usr/local/bin/sds-mcp"

# Run controller locally
# configs/controller.toml is local and untracked: copy it from the example.
run-controller:
	@test -f configs/controller.toml || { echo "configs/controller.toml not found: cp configs/controller.toml.example configs/controller.toml and edit it" >&2; exit 1; }
	go run ./cmd/controller --config configs/controller.toml

# Run CLI
run-cli:
	go run ./cmd/cli $(ARGS)

# Generate proto files
proto:
	@echo "Generating proto files..."
	./scripts/generate-proto.sh

# Format code
fmt:
	go fmt ./...
	gofmt -s -w .

# Lint
# The --max flags turn off golangci-lint's output truncation (50 per linter,
# 3 per message by default), which otherwise hides most of what it found and
# picks the survivors non-deterministically. Same flags as CI, so `make lint`
# actually predicts the gate.
lint:
	golangci-lint run ./... --max-issues-per-linter=0 --max-same-issues=0

# Point git at the versioned hooks in .githooks (run once per clone). The
# pre-commit hook rejects staged Go files that are not gofmt-clean, which is
# CI's first gate and the one `go test` cannot catch.
hooks:
	@git config core.hooksPath .githooks
	@echo "git hooks installed (core.hooksPath=.githooks); bypass with --no-verify"

# Run the CI pipeline exactly as .github/workflows/ci.yml does, so a red build
# is found here rather than after a push. Note CI checks plain `gofmt -l`, NOT
# the stricter `gofmt -s` that `make fmt` applies.
ci: ui-ensure
	@echo "==> file size"
	@./scripts/check-file-size.sh
	@echo "==> gofmt"
	@unformatted=$$(gofmt -l . | grep -vE '^(vendor|\.claude)/' || true); \
		if [ -n "$$unformatted" ]; then \
			echo "These files are not gofmt-clean:"; echo "$$unformatted"; exit 1; \
		fi
	@echo "==> go vet"
	@go vet ./...
	@echo "==> golangci-lint"
	@golangci-lint run ./... --max-issues-per-linter=0 --max-same-issues=0
	@echo "==> go vet + golangci-lint + go build as linux"
	@# CI runs on Linux. Code built only there (cmd/service-ip, pkg/serviceip)
	@# is invisible to vet and lint on a Mac, so without this pass the local run
	@# is green while CI fails on it.
	@GOOS=linux go vet ./...
	@GOOS=linux golangci-lint run ./... --max-issues-per-linter=0 --max-same-issues=0
	@GOOS=linux go build ./...
	@echo "==> go build"
	@go build ./...
	@echo "==> go test (race, uncached)"
	@go test -race -count=1 ./...
	@echo "==> govulncheck"
	@go run golang.org/x/vuln/cmd/govulncheck@v1.7.0 ./...
	@echo "==> web-ui build"
	@npm --prefix web-ui run build --silent >/dev/null
	@echo "CI pipeline passed"

# Dependencies
deps:
	go mod download
	go mod tidy

# Web UI
web-ui-dev:
	cd web-ui && npm run dev

web-ui-build:
	cd web-ui && npm run build

web-ui-install: web-ui-build
	@echo "Installing web-ui..."
	sudo mkdir -p /opt/sds/www
	sudo cp -r web-ui/dist/* /opt/sds/www/
	@echo "Web UI installed to /opt/sds/www/"

# The shared knowledge base sds-ai attaches read-only on every cluster:
# docs, code graph, CLI reference and DRBD manuals. See ai/kb/build.sh for
# the environment it needs.
kb:
	ai/kb/build.sh dist/kb
