#!/bin/bash
# Install the SDS storage plugin on this Proxmox VE node.
#
# The plugin is a single Perl module with no dependencies beyond what PVE
# already ships, so installing is a copy plus a daemon reload.
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
    echo "Remove any 'sds:' entries from /etc/pve/storage.cfg before reloading."
    reload_pve
    exit 0
fi

if [ ! -f "$SRC_DIR/$PLUGIN_NAME" ]; then
    echo "$PLUGIN_NAME not found next to install.sh" >&2
    exit 1
fi

# Fail before touching the system if the module does not even compile. Without
# this a syntax error takes pvedaemon down with it on restart.
if ! perl -c "$SRC_DIR/$PLUGIN_NAME" >/dev/null 2>&1; then
    echo "Refusing to install: $PLUGIN_NAME does not compile on this node:" >&2
    perl -c "$SRC_DIR/$PLUGIN_NAME" >&2 || true
    exit 1
fi

mkdir -p "$PLUGIN_DIR"
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
