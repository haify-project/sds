#!/bin/bash
# Deploy SDS Controller - first time install or update.
#
# Self-HA aware: when the controller is managed by drbd-reactor (the sds-meta
# resource exists), the binary is pushed to every node but the service is only
# restarted on the active (sds-meta Primary) node -- standby nodes are left for
# reactor to manage and are NOT systemctl-enabled (that would fail on their
# DRBD dependency). On a plain single-controller install it falls back to
# enable+restart per host.

set -e

# Configuration
HOSTS="orange1"
CONTROLLER_PORT=3374
CONTROLLER_BINARY="./bin/sds-controller"
CLI_BINARY="./bin/sds"
SERVICE_FILE="./configs/sds-controller.service"
CONFIG_FILE="./configs/controller.toml.example"
REMOTE_BASE="/opt/sds"
REMOTE_CONTROLLER="${REMOTE_BASE}/bin/sds-controller"
REMOTE_CLI="/usr/local/bin/sds"
REMOTE_SERVICE="/etc/systemd/system/sds-controller.service"
REMOTE_CONFIG="/etc/sds/controller.toml"

# Build target: the nodes are linux/amd64, the build host often is not
# (e.g. macOS/arm64). Cross-compile by default so the binaries actually run.
TARGET_OS="${TARGET_OS:-linux}"
TARGET_ARCH="${TARGET_ARCH:-amd64}"

# Colors
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
NC='\033[0m'

log_info() { echo -e "${GREEN}[INFO]${NC} $1"; }
log_warn() { echo -e "${YELLOW}[WARN]${NC} $1"; }
log_step() { echo -e "${BLUE}[STEP]${NC} $1"; }

# Parse args
CLI_ONLY=false
while [[ $# -gt 0 ]]; do
    case $1 in
        --hosts) HOSTS="$2"; shift 2 ;;
        --build) BUILD=true; shift ;;
        --cli-only) CLI_ONLY=true; shift ;;
        --target-os) TARGET_OS="$2"; shift 2 ;;
        --target-arch) TARGET_ARCH="$2"; shift 2 ;;
        -h|--help)
            echo "Usage: $0 [--hosts HOST1,HOST2] [--build] [--cli-only] [--target-os OS] [--target-arch ARCH]"
            echo ""
            echo "  --hosts HOSTS       Comma-separated hosts (default: $HOSTS)"
            echo "  --build             Build before deploying (cross-compiles for ${TARGET_OS}/${TARGET_ARCH})"
            echo "  --cli-only          Only deploy the CLI binary"
            echo "  --target-os OS      Build GOOS (default: $TARGET_OS, or \$TARGET_OS)"
            echo "  --target-arch ARCH  Build GOARCH (default: $TARGET_ARCH, or \$TARGET_ARCH)"
            exit 0
            ;;
        *) HOSTS="$1"; shift ;;
    esac
done

# Build (cross-compiled for the target; `make build` force-syncs the web UI
# into ui/dist via ui-sync, so the embedded UI is always fresh).
if [ "$BUILD" = true ]; then
    log_step "Building binaries for ${TARGET_OS}/${TARGET_ARCH}..."
    GOOS="$TARGET_OS" GOARCH="$TARGET_ARCH" CGO_ENABLED=0 make build 2>&1 | tail -3
fi

log_info "=========================================="
log_info "Deploying to: $HOSTS (CLI_ONLY: $CLI_ONLY)"
log_info "=========================================="

# Phase 1: push binaries / service / config to every host (no service start).
for host in ${HOSTS//,/ }; do
    log_step "Copying to $host..."

    if [ "$CLI_ONLY" = false ]; then
        ssh "$host" "sudo mkdir -p /etc/sds /opt/sds/bin /var/log/sds /var/lib/sds"
        scp -q "$CONTROLLER_BINARY" "$host:/tmp/sds-controller"
        ssh "$host" "sudo install -m755 /tmp/sds-controller $REMOTE_CONTROLLER && rm -f /tmp/sds-controller"
    fi

    scp -q "$CLI_BINARY" "$host:/tmp/sds"
    ssh "$host" "sudo install -m755 /tmp/sds $REMOTE_CLI && sudo ln -sf sds /usr/local/bin/sds-cli && rm -f /tmp/sds"

    if [ "$CLI_ONLY" = false ]; then
        if [ -f "$SERVICE_FILE" ]; then
            scp -q "$SERVICE_FILE" "$host:/tmp/sds-controller.service"
            ssh "$host" "sudo mv /tmp/sds-controller.service $REMOTE_SERVICE && sudo systemctl daemon-reload"
        fi
        # Copy config only if absent (never clobber an existing config).
        if [ -f "$CONFIG_FILE" ] && ! ssh "$host" "test -f $REMOTE_CONFIG"; then
            scp -q "$CONFIG_FILE" "$host:/tmp/controller.toml"
            ssh "$host" "sudo mv /tmp/controller.toml $REMOTE_CONFIG"
        fi
        # drbd-reactor auto-reload (harmless per host).
        if ssh "$host" "command -v drbd-reactor &>/dev/null"; then
            if ssh "$host" "test -f /usr/share/doc/drbd-reactor/examples/drbd-reactor-reload.path"; then
                ssh "$host" "sudo cp /usr/share/doc/drbd-reactor/examples/drbd-reactor-reload.{path,service} /etc/systemd/system/ 2>/dev/null; sudo systemctl daemon-reload; sudo systemctl enable --now drbd-reactor-reload.path" 2>/dev/null || true
            fi
        fi
    fi
done

if [ "$CLI_ONLY" = true ]; then
    log_info "✓ CLI deployed to: $HOSTS"
    exit 0
fi

# Phase 2: detect self-HA and restart appropriately.
# If sds-meta exists, the controller is reactor-managed -> restart only the
# active (Primary) node; standby nodes are driven by reactor on failover.
ACTIVE_NODE=""
SELF_HA=false
for host in ${HOSTS//,/ }; do
    role=$(ssh "$host" "drbdadm role sds-meta 2>/dev/null" || true)
    if [ -n "$role" ]; then
        SELF_HA=true
        [ "$role" = "Primary" ] && ACTIVE_NODE="$host"
    fi
done

if [ "$SELF_HA" = true ]; then
    if [ -z "$ACTIVE_NODE" ]; then
        log_warn "Self-HA detected but no sds-meta Primary among the given hosts; not restarting."
        log_warn "Restart the active controller node manually: ssh <active> sudo systemctl restart sds-controller"
    else
        log_step "Self-HA cluster: restarting controller only on active node $ACTIVE_NODE..."
        ssh "$ACTIVE_NODE" "sudo systemctl restart sds-controller.service"
        log_info "Standby nodes updated; reactor will run the new binary there on failover."
    fi
else
    for host in ${HOSTS//,/ }; do
        log_step "Enabling + restarting controller on $host..."
        ssh "$host" "sudo systemctl enable sds-controller.service && sudo systemctl restart sds-controller.service"
    done
fi

sleep 2

log_info "=========================================="
log_info "Service Status:"
log_info "=========================================="
if [ "$SELF_HA" = true ] && [ -n "$ACTIVE_NODE" ]; then
    echo "[$ACTIVE_NODE (active)]"
    ssh "$ACTIVE_NODE" "sudo systemctl status sds-controller.service --no-pager" 2>/dev/null | head -n 8
else
    for host in ${HOSTS//,/ }; do
        echo "[$host]"
        ssh "$host" "sudo systemctl status sds-controller.service --no-pager" 2>/dev/null | head -n 8
        echo ""
    done
fi

log_info "✓ Deployment completed!"
log_info "Database: /var/lib/sds/sds.db (BoltDB)"
log_info "Logs: journalctl -u sds-controller.service -f"
log_info "Test: sds -c <HOST>:$CONTROLLER_PORT pool list"
