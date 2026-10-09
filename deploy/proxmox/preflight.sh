#!/bin/bash
# Preflight for the Haify Proxmox VE storage plugin.
#
# Checks the things whose absence would otherwise surface as an inscrutable
# failure on the first VM disk creation. Run on every PVE node that should run
# guests off Haify storage.
#
#   ./preflight.sh <controller>[,<controller>...]
#
# <controller> is what goes in storage.cfg: host, host:port or [v6]:port,
# optionally prefixed with https://. Set SDS_CA to a PEM bundle when the
# controller's certificate is signed by a private CA (storage.cfg's
# controllerca). Exits non-zero if anything required is missing.

set -uo pipefail

CONTROLLER="${1:-}"
if [ -z "$CONTROLLER" ]; then
    echo "usage: $0 <controller>[,<controller>...]   (host, host:port, optionally https://)" >&2
    exit 2
fi

# base_url turns one storage.cfg address into scheme://host:port.
base_url() {
    local entry="$1" scheme=http
    case "$entry" in
        https://*) scheme=https; entry="${entry#https://}" ;;
        http://*) entry="${entry#http://}" ;;
    esac
    entry="${entry%/}"
    case "$entry" in
        \[*\]:*) ;;
        \[*\]) entry="${entry}:3375" ;;
        *:*:*) entry="[${entry}]:3375" ;;
        *:*) ;;
        *) entry="${entry}:3375" ;;
    esac
    echo "${scheme}://${entry}"
}

CURL=(curl -s -m 10)
[ -n "${SDS_CA:-}" ] && CURL+=(--cacert "$SDS_CA")

FAIL=0
ok()   { echo "  OK    $*"; }
bad()  { echo "  FAIL  $*"; FAIL=1; }
warn() { echo "  WARN  $*"; }

echo "Haify Proxmox plugin preflight on $(hostname)"
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

# 4. A controller has to be reachable over REST from this node. With Self-HA
#    only one of the listed addresses answers at a time, so one is enough.
BASE=""
IFS=',' read -ra ENTRIES <<< "$CONTROLLER"
for entry in "${ENTRIES[@]}"; do
    entry="$(echo "$entry" | tr -d '[:space:]')"
    [ -n "$entry" ] || continue
    url="$(base_url "$entry")"
    code=$("${CURL[@]}" -o /dev/null -w '%{http_code}' "${url}/v1/resources")
    case "$code" in
        200) ok "sds-controller REST answers at ${url}"; BASE="${BASE:-$url}" ;;
        401|403) ok "sds-controller REST answers at ${url} (auth enabled — set 'apitoken' in storage.cfg)"; BASE="${BASE:-$url}" ;;
        000) warn "no answer at ${url} (fine for a standby Self-HA node; for https, check the certificate and SDS_CA)" ;;
        *) warn "${url} answered HTTP ${code}" ;;
    esac
done
[ -n "$BASE" ] || bad "no sds-controller REST address in '${CONTROLLER}' answered"

# 5. This node must be registered with Haify under its PVE node name, because the
#    plugin promotes/attaches by node name. A mismatch is the subtlest failure
#    mode here, so it is checked explicitly.
NODENAME=$(hostname)
NODES_JSON=""
[ -n "$BASE" ] && NODES_JSON=$("${CURL[@]}" -f "${BASE}/v1/nodes" 2>/dev/null)
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

# 7. LVM must not scan the DRBD devices that carry guest disks, or a guest's
#    own volume group gets activated on this host and pins the device open
#    (lvm-filter.sh). install.sh adds the filter, so its absence is a warning.
if LVM_OUT=$("$(dirname "${BASH_SOURCE[0]}")/lvm-filter.sh" --check 2>&1); then
    ok "$LVM_OUT"
else
    while IFS= read -r line; do warn "$line"; done <<< "$LVM_OUT"
fi

echo
if [ "$FAIL" -eq 0 ]; then
    echo "Preflight passed. Install the plugin with: ./install.sh"
else
    echo "Preflight FAILED — fix the items above before installing the plugin." >&2
fi
exit "$FAIL"
