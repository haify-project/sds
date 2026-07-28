.PHONY: build test clean install-controller install-cli install-mcp run-controller run-cli proto web-ui web-ui-dev web-ui-build ui-sync ui-ensure

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
	@echo "Building sds-cli..."
	go build -o bin/sds-cli ./cmd/cli
	@echo "Building sds-mcp..."
	go build -o bin/sds-mcp ./cmd/mcp
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
	sudo mkdir -p /opt/sds/bin
	sudo cp bin/sds-controller /opt/sds/bin/
	sudo cp configs/sds-controller.service /etc/systemd/system/
	sudo cp configs/controller.toml.example /etc/sds/controller.toml.example
	sudo systemctl daemon-reload
	@echo "Controller installed. Edit /etc/sds/controller.toml then run:"
	@echo "  sudo systemctl start sds-controller"
	@echo "  sudo systemctl enable sds-controller"

# Install CLI
install-cli: build
	@echo "Installing sds-cli..."
	sudo cp bin/sds-cli /usr/local/bin/
	@echo "CLI installed to /usr/local/bin/sds-cli"

# Install MCP server
install-mcp: build
	@echo "Installing sds-mcp..."
	sudo cp bin/sds-mcp /usr/local/bin/
	@echo "MCP server installed to /usr/local/bin/sds-mcp"

# Run controller locally
run-controller:
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
lint:
	golangci-lint run

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
