#!/bin/bash
# Install the SDS storage plugin on this Proxmox VE node.
#
# The plugin is a Perl module plus its helpers under SDS/, with no dependencies
# beyond what PVE already ships, so installing is a copy plus a daemon reload.
#
#   ./install.sh            install/upgrade, then restart pvedaemon + pveproxy
#   ./install.sh --uninstall remove the plugin
#
# Run it on every PVE node in the cluster: pvedaemon loads storage plugins
# in-process, so a node without the module cannot use the storage.

set -euo pipefail

PLUGIN_DIR=/usr/share/perl5/PVE/Storage/Custom
PLUGIN_NAME=SDSPlugin.pm
SRC_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

# Helper modules live in a subdirectory: PVE loads every *.pm directly under
# Custom/ as a storage plugin, so they must not sit next to SDSPlugin.pm.
HELPER_DIR="$PLUGIN_DIR/SDS"
HELPER_SRC_DIR="$SRC_DIR/PVE/Storage/Custom/SDS"
HELPER_NAMES=(Client.pm Naming.pm)

if [ "$(id -u)" -ne 0 ]; then
    echo "install.sh must run as root" >&2
    exit 1
fi

reload_pve() {
    # Storage plugins are loaded in-process, so the daemons must be restarted
    # for a new or changed module to take effect. Restarting these does not
    # disturb running guests.
    systemctl restart pvedaemon pveproxy
}

if [ "${1:-}" = "--uninstall" ]; then
    rm -f "$PLUGIN_DIR/$PLUGIN_NAME"
    echo "Removed $PLUGIN_DIR/$PLUGIN_NAME"
    for name in "${HELPER_NAMES[@]}"; do
        rm -f "$HELPER_DIR/$name"
        echo "Removed $HELPER_DIR/$name"
    done
    rmdir "$HELPER_DIR" 2>/dev/null || true
    echo "Remove any 'sds:' entries from /etc/pve/storage.cfg before reloading."
    reload_pve
    exit 0
fi

if [ ! -f "$SRC_DIR/$PLUGIN_NAME" ]; then
    echo "$PLUGIN_NAME not found next to install.sh" >&2
    exit 1
fi
for name in "${HELPER_NAMES[@]}"; do
    if [ ! -f "$HELPER_SRC_DIR/$name" ]; then
        echo "$HELPER_SRC_DIR/$name not found" >&2
        exit 1
    fi
done

# Fail before touching the system if the module does not even compile. Without
# this a syntax error takes pvedaemon down with it on restart. -I makes the
# check use the helpers being installed, not ones already on the node.
if ! perl -I "$SRC_DIR" -c "$SRC_DIR/$PLUGIN_NAME" >/dev/null 2>&1; then
    echo "Refusing to install: $PLUGIN_NAME does not compile on this node:" >&2
    perl -I "$SRC_DIR" -c "$SRC_DIR/$PLUGIN_NAME" >&2 || true
    exit 1
fi

mkdir -p "$PLUGIN_DIR" "$HELPER_DIR"
for name in "${HELPER_NAMES[@]}"; do
    install -m 0644 "$HELPER_SRC_DIR/$name" "$HELPER_DIR/$name"
    echo "Installed $HELPER_DIR/$name"
done
install -m 0644 "$SRC_DIR/$PLUGIN_NAME" "$PLUGIN_DIR/$PLUGIN_NAME"
echo "Installed $PLUGIN_DIR/$PLUGIN_NAME"

reload_pve
echo "Restarted pvedaemon and pveproxy"

cat <<'EOF'

Next: add a storage entry to /etc/pve/storage.cfg (cluster-wide, edit once), e.g.

  sds: sds0
        controller 192.168.1.10
        sdspool vg0
        replicas 2
        content images,rootdir
        shared 1

See storage.cfg.example for every option.
EOF
