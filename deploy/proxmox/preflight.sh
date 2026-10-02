#!/bin/bash
# Preflight for the SDS Proxmox VE storage plugin.
#
# Checks the things whose absence would otherwise surface as an inscrutable
# failure on the first VM disk creation. Run on every PVE node that should run
# guests off SDS storage.
#
#   ./preflight.sh <controller-host>[:<rest-port>]
#
# Exits non-zero if anything required is missing.

set -uo pipefail

CONTROLLER="${1:-}"
if [ -z "$CONTROLLER" ]; then
    echo "usage: $0 <controller-host>[:<rest-port>]" >&2
    exit 2
fi

HOST="${CONTROLLER%%:*}"
PORT="${CONTROLLER##*:}"
[ "$PORT" = "$CONTROLLER" ] && PORT=3375

FAIL=0
ok()   { echo "  OK    $*"; }
bad()  { echo "  FAIL  $*"; FAIL=1; }
warn() { echo "  WARN  $*"; }

echo "SDS Proxmox plugin preflight on $(hostname)"
echo

# 1. This must be a Proxmox node at all.
if command -v pveversion >/dev/null 2>&1; then
    ok "Proxmox VE present: $(pveversion | head -1)"
else
    bad "pveversion not found — this does not look like a Proxmox VE node"
fi

# 2. DRBD. The PVE host has to see /dev/drbdN locally to back a VM disk, so it
#    needs the DRBD 9 kernel module and userspace tools even when it stores no
#    replica of its own (it attaches as a diskless client).
if command -v drbdadm >/dev/null 2>&1; then
    ok "drbd-utils present: $(drbdadm --version | grep DRBDADM_VERSION= | cut -d= -f2)"
else
    bad "drbdadm not found — install drbd-utils (Debian 12: LINBIT repo)"
fi

if modprobe drbd 2>/dev/null && [ -e /proc/drbd ] || grep -q '^drbd ' /proc/modules 2>/dev/null; then
    VER=$(cat /sys/module/drbd/version 2>/dev/null || echo unknown)
    case "$VER" in
        9.*) ok "DRBD kernel module loaded: $VER" ;;
        unknown) warn "DRBD module loaded but its version could not be read" ;;
        *) bad "DRBD kernel module is $VER — version 9 is required" ;;
    esac
else
    bad "DRBD kernel module cannot be loaded — install drbd-dkms (LINBIT) for kernel $(uname -r)"
fi

# 3. sudo. The sds-controller reaches this node over SSH and runs every
#    privileged operation (drbdadm, lvcreate, writing /etc/drbd.d) through
#    `sudo`. A minimal Proxmox/Debian install may not ship sudo, in which case
#    those commands fail silently mid-operation (config never lands, DRBD never
#    comes up) with no obvious cause — so check for it explicitly.
if command -v sudo >/dev/null 2>&1; then
    ok "sudo present"
else
    bad "sudo not found — install it (apt-get install sudo); the controller runs node commands via sudo"
fi

# 4. The controller has to be reachable over REST from this node.
if curl -sf -m 10 -o /dev/null "http://${HOST}:${PORT}/v1/resources"; then
    ok "sds-controller REST reachable at ${HOST}:${PORT}"
elif curl -s -m 10 -o /dev/null -w '%{http_code}' "http://${HOST}:${PORT}/v1/resources" | grep -q '^401\|^403'; then
    ok "sds-controller REST reachable at ${HOST}:${PORT} (auth enabled — set 'apitoken' in storage.cfg)"
else
    bad "sds-controller REST not reachable at ${HOST}:${PORT}"
fi

# 5. This node must be registered with SDS under its PVE node name, because the
#    plugin promotes/attaches by node name. A mismatch is the subtlest failure
#    mode here, so it is checked explicitly.
NODENAME=$(hostname)
NODES_JSON=$(curl -sf -m 10 "http://${HOST}:${PORT}/v1/nodes" 2>/dev/null)
if [ -n "$NODES_JSON" ]; then
    if echo "$NODES_JSON" | grep -q "\"name\":\"${NODENAME}\""; then
        ok "node '${NODENAME}' is registered with sds"
    else
        bad "node '${NODENAME}' is NOT registered with sds — run: sds node register --name ${NODENAME} --address <ip>"
    fi
else
    warn "could not list sds nodes (auth?); verify '${NODENAME}' is registered manually"
fi

# 6. Perl dependencies. Both ship with PVE, so this should never fail — but if
#    it does, the plugin would fail to load with a bare "Can't locate".
for mod in HTTP::Tiny JSON::PP; do
    if perl -M"$mod" -e1 2>/dev/null; then
        ok "perl module $mod available"
    else
        bad "perl module $mod missing"
    fi
done

echo
if [ "$FAIL" -eq 0 ]; then
    echo "Preflight passed. Install the plugin with: ./install.sh"
else
    echo "Preflight FAILED — fix the items above before installing the plugin." >&2
fi
exit "$FAIL"
