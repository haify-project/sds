#!/bin/bash
# Deploy SDS Controller - first time install or update

set -e

# Configuration
HOSTS="orange1"
CONTROLLER_PORT=3374
CONTROLLER_BINARY="./bin/sds-controller"
CLI_BINARY="./bin/sds-cli"
SERVICE_FILE="./configs/sds-controller.service"
CONFIG_FILE="./configs/controller.toml.example"
REMOTE_BASE="/opt/sds"
REMOTE_CONTROLLER="${REMOTE_BASE}/bin/sds-controller"
REMOTE_CLI="/usr/local/bin/sds-cli"
REMOTE_SERVICE="/etc/systemd/system/sds-controller.service"
REMOTE_CONFIG="/etc/sds/controller.toml"

# Colors
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
NC='\033[0m'

log_info() { echo -e "${GREEN}[INFO]${NC} $1"; }
log_warn() { echo -e "${YELLOW}[WARN]${NC} $1"; }
log_step() { echo -e "${BLUE}[STEP]${NC} $1"; }

# Parse args
while [[ $# -gt 0 ]]; do
    case $1 in
        --hosts) HOSTS="$2"; shift 2 ;;
        --build) BUILD=true; shift ;;
        -h|--help)
            echo "Usage: $0 [--hosts HOST1,HOST2] [--build]"
            echo ""
            echo "  --hosts HOSTS    Comma-separated hosts (default: $HOSTS)"
            echo "  --build          Build before deploying"
            exit 0
            ;;
        *) HOSTS="$1"; shift ;;
    esac
done

# Build
log_step "Building binaries..."
make build 2>&1 | tail -3

log_info "=========================================="
log_info "Deploying to: $HOSTS"
log_info "=========================================="

for host in ${HOSTS//,/ }; do
    log_step "Setting up $host..."

    # Create directories
    ssh "$host" "sudo mkdir -p /etc/sds /opt/sds/bin /var/log/sds /var/lib/sds" 2>/dev/null

    # Copy binaries
    scp "$CONTROLLER_BINARY" "$host:/tmp/sds-controller" 2>/dev/null
    scp "$CLI_BINARY" "$host:/tmp/sds-cli" 2>/dev/null
    ssh "$host" "sudo mv /tmp/sds-controller $REMOTE_CONTROLLER && sudo chmod +x $REMOTE_CONTROLLER && sudo mv /tmp/sds-cli $REMOTE_CLI && sudo chmod +x $REMOTE_CLI" 2>/dev/null

    # Copy service file (always update to include DB path changes)
    if [ -f "$SERVICE_FILE" ]; then
        scp "$SERVICE_FILE" "$host:/tmp/sds-controller.service" 2>/dev/null
        ssh "$host" "sudo mv /tmp/sds-controller.service $REMOTE_SERVICE && sudo systemctl daemon-reload" 2>/dev/null
    fi

    # Copy config if not exists
    if [ -f "$CONFIG_FILE" ]; then
        if ! ssh "$host" "test -f $REMOTE_CONFIG" 2>/dev/null; then
            scp "$CONFIG_FILE" "$host:/tmp/controller.toml" 2>/dev/null
            ssh "$host" "sudo mv /tmp/controller.toml $REMOTE_CONFIG" 2>/dev/null
        fi
    fi

    # Enable and restart service
    ssh "$host" "sudo systemctl enable sds-controller.service && sudo systemctl restart sds-controller.service" 2>/dev/null
done

sleep 2

# Show status
log_info "=========================================="
log_info "Service Status:"
log_info "=========================================="
for host in ${HOSTS//,/ }; do
    echo "[$host]"
    ssh "$host" "sudo systemctl status sds-controller.service --no-pager" 2>/dev/null | head -n 10
    echo ""
done

log_info "✓ Deployment completed!"
log_info ""
log_info "Database: /var/lib/sds/sds.db (BoltDB)"
log_info "Logs: journalctl -u sds-controller.service -f"
log_info ""
log_info "Test: sds-cli -c <HOST>:$CONTROLLER_PORT pool list"
