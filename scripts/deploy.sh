#!/bin/bash
# Deploy sds-controller to orange1, orange2, orange3

set -e

# Configuration
NODES=("orange1" "orange2" "orange3")
CONTROLLER_BINARY="./bin/sds-controller"
CLI_BINARY="./bin/sds-cli"
SERVICE_FILE="./configs/sds-controller.service"
CONFIG_EXAMPLE="./configs/controller.toml.example"
REMOTE_CONTROLLER="/opt/sds/bin/sds-controller"
REMOTE_CLI="/usr/local/bin/sds-cli"
REMOTE_SERVICE_DIR="/etc/systemd/system"
REMOTE_CONFIG_DIR="/etc/sds"
REMOTE_LOG_DIR="/var/log/sds"

# Colors for output
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
NC='\033[0m' # No Color

log_info() {
    echo -e "${GREEN}[INFO]${NC} $1"
}

log_warn() {
    echo -e "${YELLOW}[WARN]${NC} $1"
}

log_error() {
    echo -e "${RED}[ERROR]${NC} $1"
}

# Check if binaries exist
if [ ! -f "$CONTROLLER_BINARY" ]; then
    log_error "Controller binary not found: $CONTROLLER_BINARY"
    log_info "Please run 'make build' first"
    exit 1
fi

if [ ! -f "$CLI_BINARY" ]; then
    log_error "CLI binary not found: $CLI_BINARY"
    log_info "Please run 'make build' first"
    exit 1
fi

# Deploy to each node
for NODE in "${NODES[@]}"; do
    log_info "=========================================="
    log_info "Deploying to $NODE"
    log_info "=========================================="

    # Check SSH connection
    if ! ssh -o ConnectTimeout=5 "$NODE" "echo 'Connected to $NODE'" > /dev/null 2>&1; then
        log_error "Cannot connect to $NODE"
        continue
    fi

    # Create necessary directories
    log_info "Creating directories on $NODE..."
    ssh "$NODE" "sudo mkdir -p $REMOTE_CONFIG_DIR
                  sudo mkdir -p /opt/sds/bin
                  sudo mkdir -p $REMOTE_LOG_DIR
                  sudo mkdir -p /var/lib/sds"

    # Copy binaries
    log_info "Copying binaries to $NODE..."
    scp "$CONTROLLER_BINARY" "$NODE:/tmp/sds-controller"
    ssh "$NODE" "sudo mv /tmp/sds-controller $REMOTE_CONTROLLER
                  sudo chmod +x $REMOTE_CONTROLLER"

    scp "$CLI_BINARY" "$NODE:/tmp/sds-cli"
    ssh "$NODE" "sudo mv /tmp/sds-cli $REMOTE_CLI
                  sudo chmod +x $REMOTE_CLI"

    # Copy systemd service file
    log_info "Copying systemd service file to $NODE..."
    if [ -f "$SERVICE_FILE" ]; then
        scp "$SERVICE_FILE" "$NODE:/tmp/sds-controller.service"
        ssh "$NODE" "sudo mv /tmp/sds-controller.service $REMOTE_SERVICE_DIR/"
    else
        log_warn "Service file not found: $SERVICE_FILE"
    fi

    # Copy example config if it doesn't exist
    if [ -f "$CONFIG_EXAMPLE" ]; then
        log_info "Checking config on $NODE..."
        if ! ssh "$NODE" "test -f $REMOTE_CONFIG_DIR/controller.toml"; then
            log_info "Copying example config to $NODE..."
            scp "$CONFIG_EXAMPLE" "$NODE:/tmp/controller.toml"
            ssh "$NODE" "sudo mv /tmp/controller.toml $REMOTE_CONFIG_DIR/controller.toml"
            log_warn "Please edit $REMOTE_CONFIG_DIR/controller.toml on $NODE"
        else
            log_info "Config already exists on $NODE, skipping"
        fi
    fi

    # Reload systemd
    log_info "Reloading systemd on $NODE..."
    ssh "$NODE" "sudo systemctl daemon-reload"

    # Enable service (but don't start it automatically)
    log_info "Enabling sds-controller on $NODE..."
    ssh "$NODE" "sudo systemctl enable sds-controller.service"

    # Note: Don't restart automatically - let user do it manually after config
    log_info "Checking if service is running on $NODE..."
    if ssh "$NODE" "systemctl is-active --quiet sds-controller.service"; then
        log_info "Service is running, restarting..."
        ssh "$NODE" "sudo systemctl restart sds-controller.service"
        sleep 2
        ssh "$NODE" "sudo systemctl status sds-controller.service --no-pager -l" | head -n 10
    else
        log_warn "Service is not running. Start it manually after editing config:"
        log_warn "  ssh $NODE 'sudo systemctl start sds-controller'"
    fi

    log_info "✓ Deployment to $NODE completed!"
    echo ""
done

log_info "=========================================="
log_info "Deployment completed!"
log_info "=========================================="
log_info "Next steps:"
log_info "1. Edit config on each node (if not already configured):"
for NODE in "${NODES[@]}"; do
    echo "  ssh $NODE 'sudo vi $REMOTE_CONFIG_DIR/controller.toml'"
done
log_info ""
log_info "2. Start service on each node:"
for NODE in "${NODES[@]}"; do
    echo "  ssh $NODE 'sudo systemctl start sds-controller'"
done
log_info ""
log_info "3. Check service status:"
for NODE in "${NODES[@]}"; do
    echo "  ssh $NODE 'sudo systemctl status sds-controller'"
done
log_info ""
log_info "4. View logs:"
for NODE in "${NODES[@]}"; do
    echo "  ssh $NODE 'sudo journalctl -u sds-controller -f'"
done
