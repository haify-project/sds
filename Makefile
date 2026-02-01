.PHONY: build test clean install-controller install-cli run-controller run-cli proto web-ui web-ui-dev web-ui-build

# Build binaries
build: web-ui-build
	@echo "Preparing UI for embedding..."
	@rm -rf ui/dist
	@cp -r web-ui/dist ui/
	@echo "Building sds-controller..."
	go build -o bin/sds-controller ./cmd/controller
	@echo "Building sds-cli..."
	go build -o bin/sds-cli ./cmd/cli
	@rm -rf ui/dist

# Run tests
test:
	go test -v ./...

# Clean build artifacts
clean:
	rm -rf bin/

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
