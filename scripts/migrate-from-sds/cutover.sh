#!/bin/bash
# Move a cluster's control plane from sds-meta to haify-meta.
#
#   cutover.sh <ssh-wrapper> <node>...
#
# Run from the repository root. The ssh wrapper is a command that takes
# "<host> <command>"; list every node of the cluster, DR nodes included. Each
# node needs /root/migrate-node.sh and the new binaries in /root/haify-new/,
# and `migrate-node.sh prep` must already have run there. Set ACTIVE=<node> to
# resume after the old control plane is already stopped. Stops at the first
# error; every step prints what it found, so the output is the record of the
# cutover. Afterwards run `migrate-node.sh boot` and `cleanup` on every node.
set -euo pipefail

SSH=$1
shift
NODES=("$@")
HERE=$(cd "$(dirname "$0")" && pwd)
WORK=$(mktemp -d)
go build -o "$WORK/dbmigrate" "$HERE/dbmigrate"

step() { printf '\n== %s\n' "$*"; }
on() { "$SSH" "$@"; }

step "state before"
for h in "${NODES[@]}"; do
	on "$h" 'printf "%s: " "$(hostname)"; drbdsetup status 2>/dev/null | grep -E "^[a-z]" | tr "\n" " "; echo'
done

ACTIVE="${ACTIVE:-}"
if [ -z "$ACTIVE" ]; then
	for h in "${NODES[@]}"; do
		if on "$h" 'mountpoint -q /var/lib/sds'; then ACTIVE=$h; fi
	done
fi
[ -n "$ACTIVE" ] || { echo "no node has /var/lib/sds mounted" >&2; exit 1; }
echo "control plane active on $ACTIVE"

step "stop the old control plane (standbys first, $ACTIVE last)"
for h in "${NODES[@]}"; do
	[ "$h" = "$ACTIVE" ] && continue
	on "$h" 'f=/etc/drbd-reactor.d/sds-ha-sds-meta.toml; if [ -f "$f" ]; then drbd-reactorctl disable --now "$f" >/dev/null; echo "$(hostname): promoter disabled"; else echo "$(hostname): no enabled sds-meta promoter"; fi'
done
on "$ACTIVE" 'drbd-reactorctl disable --now /etc/drbd-reactor.d/sds-ha-sds-meta.toml >/dev/null; echo "$(hostname): promoter disabled"'
sleep 5
on "$ACTIVE" 'if mountpoint -q /var/lib/sds; then umount /var/lib/sds; fi; drbdadm status sds-meta | head -1'

step "migrate the database and the files on the control-plane volume ($ACTIVE)"
on "$ACTIVE" 'drbdadm primary sds-meta && mkdir -p /mnt/haify-migrate && mount /dev/drbd/by-res/sds-meta/0 /mnt/haify-migrate && ls /mnt/haify-migrate'
on "$ACTIVE" 'cat /mnt/haify-migrate/sds.db' >"$WORK/sds.db"
"$WORK/dbmigrate" "$WORK/sds.db" "$WORK/haify.db" | grep -v '^\[backups\]\|^    …lume' || true
on "$ACTIVE" 'cat >/mnt/haify-migrate/haify.db && chmod 600 /mnt/haify-migrate/haify.db' <"$WORK/haify.db"
on "$ACTIVE" 'set -e; cd /mnt/haify-migrate
	mv sds.db sds.db.pre-haify
	# The WAN certificates name the proxy; the new controller issues new ones.
	[ -d wanproxy-pki ] && mv wanproxy-pki wanproxy-pki.pre-haify
	if [ -f ai/sds-ai.env ]; then
		bash /root/migrate-node.sh tx ai/sds-ai.env >ai/haify-ai.env && chmod 600 ai/haify-ai.env && mv ai/sds-ai.env ai/sds-ai.env.pre-haify
	fi
	if [ -f ai/domain.toml ]; then
		cp -a ai/domain.toml ai/domain.toml.pre-haify
		bash /root/migrate-node.sh tx ai/domain.toml.pre-haify | sed -e "s/sds_/haify_/g" -e "s/\bsds\b/haify/g" >ai/domain.toml
	fi
	if [ -f mcp/sds-mcp.env ]; then
		bash /root/migrate-node.sh tx mcp/sds-mcp.env >mcp/haify-mcp.env && chmod 600 mcp/haify-mcp.env && mv mcp/sds-mcp.env mcp/sds-mcp.env.pre-haify
	fi
	ls -la . ai mcp 2>/dev/null | grep -v "^total" | grep -i "haify\|pre-haify\|^\." || true
	cd / && umount /mnt/haify-migrate && drbdadm secondary sds-meta && echo "volume migrated, back to Secondary"'

step "take sds-meta down everywhere"
for h in "${NODES[@]}"; do
	on "$h" 'if drbdsetup status sds-meta >/dev/null 2>&1; then drbdadm down sds-meta; echo "$(hostname): down"; else echo "$(hostname): not up"; fi'
done

step "rename storage and DRBD configs"
for h in "${NODES[@]}"; do
	echo "-- $h"
	on "$h" 'bash /root/migrate-node.sh storage'
done

step "bring haify-meta up"
for h in "${NODES[@]}"; do on "$h" 'bash /root/migrate-node.sh meta-up'; done
sleep 3
on "$ACTIVE" 'drbdadm status haify-meta'

step "promoter configs"
for h in "${NODES[@]}"; do on "$h" 'bash /root/migrate-node.sh reactor'; done
# The control-plane promoter was disabled to stop the old control plane, so
# its renamed config is disabled too; every node that had it gets it back.
for h in "${NODES[@]}"; do
	on "$h" 'f=/etc/drbd-reactor.d/haify-ha-haify-meta.toml.disabled; if [ -f "$f" ]; then drbd-reactorctl enable "$f" >/dev/null && echo "$(hostname): haify-meta promoter enabled"; fi'
done

step "reattach backing disks under their new paths (Secondaries, then Primaries)"
for h in "${NODES[@]}"; do on "$h" 'bash /root/migrate-node.sh reattach secondary'; done
for h in "${NODES[@]}"; do on "$h" 'bash /root/migrate-node.sh reattach primary'; done

step "wait for the new control plane"
for i in $(seq 1 30); do
	for h in "${NODES[@]}"; do
		if on "$h" 'systemctl is-active -q haify-controller && mountpoint -q /var/lib/haify'; then
			echo "haify-controller active on $h"
			sleep 5
			on "$h" 'haify node list; haify resource list | head -20; haify pool list'
			step "WAN legs: stop the old proxies, provision new ones under the new certificates"
			for n in "${NODES[@]}"; do
				on "$n" 'for u in $(systemctl list-units --no-legend "sds-proxy@*" | awk "{print \$1}"); do systemctl disable --now "$u"; done'
			done
			for r in $(on "$h" "haify resource list | awk '{print \$1}'"); do
				if on "$h" "haify resource status $r 2>/dev/null | grep -q 'WAN replication'"; then
					on "$h" "haify wan repair $r"
				fi
			done
			rm -rf "$WORK"
			exit 0
		fi
	done
	sleep 4
done
echo "haify-controller did not come up within two minutes" >&2
for h in "${NODES[@]}"; do on "$h" 'printf "%s: " "$(hostname)"; drbd-reactorctl status 2>&1 | head -3; journalctl -u haify-controller -n 5 --no-pager 2>&1 | tail -5'; done
exit 1
