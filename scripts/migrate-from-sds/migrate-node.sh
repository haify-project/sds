#!/bin/bash
# Move one node from the SDS names to the Haify ones. Run as root, one phase at
# a time, in the order the cluster runbook gives:
#
#   prep       install binaries, config, units under the new names (no effect
#              on anything running); back up everything it will change
#   storage    rename the volume group, thin pool and the control-plane LVs,
#              rewrite and rename the DRBD .res files (sds-meta must be down)
#   meta-up    bring haify-meta up (on the nodes that carry it)
#   reactor    swap the promoter configs to their new names and reload
#   boot       swap the boot unit (and node-c's pool loop unit)
#   cleanup    stop and remove the old units and files
#
# New binaries are expected in /root/haify-new/.
set -euo pipefail

NEW=/root/haify-new
STAMP=$(date +%Y%m%d)
BACKUP=/root/sds-pre-haify-$STAMP.tgz

# tx rewrites the names inside a text file onto the new ones. Host names such
# as sds-b or lima-sds-a are machine identities and are left alone.
tx() {
	sed -e 's/sds\\x2dmeta/haify\\x2dmeta/g' \
		-e 's/sds-meta/haify-meta/g' \
		-e 's#/dev/sds_#/dev/haify_#g' \
		-e 's/sdsthin/haifythin/g' \
		-e 's#/var/lib/sds#/var/lib/haify#g' \
		-e 's/var-lib-sds/var-lib-haify/g' \
		-e 's#/etc/sds/#/etc/haify/#g' \
		-e 's#/opt/sds/#/opt/haify/#g' \
		-e 's#/opt/sds$#/opt/haify#g' \
		-e 's/sds-controller/haify-controller/g' \
		-e 's/sds-ai/haify-ai/g' \
		-e 's/sds-mcp/haify-mcp/g' \
		-e 's/sds-kb/haify-kb/g' \
		-e 's/sds-drbd-up/haify-drbd-up/g' \
		-e 's/sds-pool-loop/haify-pool-loop/g' \
		-e 's/sds-ha-/haify-ha-/g' \
		-e 's/sds\.db/haify.db/g' \
		-e 's/SDS_/HAIFY_/g' \
		-e 's/\bSDS\b/Haify/g' \
		"$@"
}

vg_old() { vgs --noheadings -o vg_name 2>/dev/null | awk '{print $1}' | grep '^sds_' || true; }

phase_prep() {
	# Everything this migration touches, so a node can be put back by hand.
	tar -czf "$BACKUP" --ignore-failed-read \
		/etc/sds /etc/sds-proxy /etc/drbd.d /etc/drbd-reactor.d \
		/etc/systemd/system/sds-* /etc/systemd/system/var-lib-sds.mount \
		/usr/local/sbin/sds-drbd-up.sh 2>/dev/null || true
	echo "backup: $BACKUP"

	install -d /opt/haify/bin /etc/haify
	for b in haify-controller haify haify-mcp haify-ai haify-proxy; do
		[ -f "$NEW/$b" ] || continue
		install -m 0755 "$NEW/$b" "/usr/local/bin/$b"
		install -m 0755 "$NEW/$b" "/opt/haify/bin/$b"
	done
	if [ -d /opt/sds/share ] && [ ! -d /opt/haify/share ]; then
		cp -a /opt/sds/share /opt/haify/share
		[ -f /opt/haify/share/sds-kb.db ] && mv /opt/haify/share/sds-kb.db /opt/haify/share/haify-kb.db
		[ -f /opt/haify/share/sds-kb.json ] && mv /opt/haify/share/sds-kb.json /opt/haify/share/haify-kb.json
	fi
	if [ -f /etc/sds/controller.toml ]; then
		tx /etc/sds/controller.toml >/etc/haify/controller.toml
		chmod --reference=/etc/sds/controller.toml /etc/haify/controller.toml
	fi
	[ -f /etc/sds/token ] && install -m 0600 /etc/sds/token /etc/haify/token

	# Units: the old ones carry local edits (paths, proxy drop-ins), so the
	# new ones are the old ones renamed rather than fresh copies.
	cd /etc/systemd/system
	for u in sds-controller.service sds-ai.service sds-mcp-http.service var-lib-sds.mount \
		sds-drbd-up.service sds-pool-loop.service; do
		[ -f "$u" ] || continue
		n=$(echo "$u" | tx)
		tx "$u" >"$n"
		if [ -d "$u.d" ]; then
			rm -rf "$n.d"
			cp -a "$u.d" "$n.d"
			for f in "$n.d"/*; do tx -i "$f"; done
		fi
	done
	# The control plane's mount unit, written whole: on some clusters the old
	# one is an empty file and the mount only ever came from drbd-reactor.
	if [ -e var-lib-sds.mount ]; then
		cat >var-lib-haify.mount <<'UNIT'
[Unit]
Description=Mount for haify-meta
[Mount]
What=/dev/drbd/by-res/haify-meta/0
Where=/var/lib/haify
Type=ext4
[Install]
WantedBy=multi-user.target
UNIT
	fi
	if [ -f /usr/local/sbin/sds-drbd-up.sh ]; then
		tx /usr/local/sbin/sds-drbd-up.sh >/usr/local/sbin/haify-drbd-up.sh
		chmod 0755 /usr/local/sbin/haify-drbd-up.sh
	fi
	systemctl daemon-reload
	echo "prep done on $(hostname)"
}

phase_storage() {
	if drbdsetup status sds-meta >/dev/null 2>&1; then
		echo "sds-meta is still up here; take it down first" >&2
		exit 1
	fi
	for vg in $(vg_old); do
		new="haify_${vg#sds_}"
		vgrename "$vg" "$new"
		echo "vg $vg -> $new"
	done
	for vg in $(vgs --noheadings -o vg_name | awk '{print $1}' | grep '^haify_' || true); do
		lvs --noheadings -o lv_name "$vg" | awk '{print $1}' | while read -r lv; do
			case "$lv" in
			sdsthin) n=haifythin ;;
			sds-meta_data) n=haify-meta_data ;;
			sds_*) n="haify_${lv#sds_}" ;;
			*) continue ;;
			esac
			lvrename "$vg" "$lv" "$n"
			echo "lv $vg/$lv -> $n"
		done
	done
	cd /etc/drbd.d
	for f in *.res; do
		[ -f "$f" ] || continue
		n=$(echo "$f" | tx)
		tx "$f" >"$n.tmp"
		if [ "$n" != "$f" ]; then
			mv "$n.tmp" "$n"
			mv "$f" "$f.pre-haify"
			echo "res $f -> $n"
		elif ! cmp -s "$f" "$n.tmp"; then
			cp -a "$f" "$f.pre-haify"
			mv "$n.tmp" "$f"
			echo "res $f rewritten"
		else
			rm -f "$n.tmp"
		fi
	done
	# Nothing may be reattached or reconfigured by this: the running devices
	# keep their backing device, only its name changed.
	for r in $(drbdadm sh-resources); do
		[ "$r" = haify-meta ] && continue
		out=$(drbdadm -d adjust "$r" 2>&1 || true)
		if [ -n "$out" ]; then
			echo "adjust $r would run:"
			echo "$out" | sed 's/^/    /'
		else
			echo "adjust $r: no change"
		fi
	done
}

# reattach makes the kernel's backing-disk path match the renamed volume
# group, one resource at a time, so a later `drbdadm adjust` (the controller
# runs one on repair) is a no-op instead of a surprise detach. "secondary"
# does the resources not Primary here, "primary" the rest; a Primary that
# detaches keeps serving I/O through its UpToDate peers.
phase_reattach() {
	want=$1
	for r in $(drbdadm sh-resources); do
		[ -n "$(drbdadm -d adjust "$r" 2>/dev/null)" ] || continue
		role=$(drbdadm role "$r" 2>/dev/null | cut -d/ -f1)
		if [ "$want" = primary ] && [ "$role" != Primary ]; then continue; fi
		if [ "$want" = secondary ] && [ "$role" = Primary ]; then continue; fi
		if drbdadm status "$r" | grep -q "peer-disk:\(Inconsistent\|Outdated\)\|replication:Sync"; then
			echo "$r: a peer is not in step; leaving it" >&2
			continue
		fi
		# The detach releases the device asynchronously; drbdmeta can find it
		# still busy on the first try.
		if ! drbdadm adjust "$r"; then
			sleep 3
			drbdadm adjust "$r" || { echo "$r: adjust failed twice" >&2; continue; }
		fi
		for _ in $(seq 1 150); do
			s=$(drbdadm status "$r")
			if echo "$s" | grep -q "^  disk:UpToDate" && ! echo "$s" | grep -q "replication:Sync\|disk:Inconsistent"; then
				break
			fi
			sleep 2
		done
		echo "$r ($role): $(drbdadm status "$r" | sed -n 2p | xargs)"
	done
}

phase_meta_up() {
	if [ -f /etc/drbd.d/haify-meta.res ]; then
		drbdadm up haify-meta
		echo "haify-meta up on $(hostname)"
	fi
}

phase_reactor() {
	cd /etc/drbd-reactor.d
	for f in sds-ha-*.toml sds-ha-*.toml.disabled; do
		[ -f "$f" ] || continue
		n=$(echo "$f" | tx)
		tx "$f" >"$n"
		mv "$f" "/root/$f.pre-haify"
		echo "reactor $f -> $n"
	done
	systemctl reload drbd-reactor
}

phase_boot() {
	if systemctl is-enabled -q sds-drbd-up.service 2>/dev/null; then
		systemctl disable sds-drbd-up.service
		systemctl enable haify-drbd-up.service
	fi
	if systemctl is-enabled -q sds-pool-loop.service 2>/dev/null; then
		# The loop device keeps its open file across the rename.
		if [ -d /var/lib/sds ] && [ ! -e /var/lib/haify ]; then
			mv /var/lib/sds /var/lib/haify
		fi
		systemctl disable sds-pool-loop.service
		systemctl enable haify-pool-loop.service
	fi
	echo "boot units swapped on $(hostname)"
}

phase_cleanup() {
	for u in sds-controller.service sds-ai.service sds-mcp-http.service var-lib-sds.mount \
		sds-drbd-up.service sds-pool-loop.service; do
		[ -f "/etc/systemd/system/$u" ] || continue
		systemctl disable "$u" 2>/dev/null || true
		rm -rf "/etc/systemd/system/$u" "/etc/systemd/system/$u.d"
	done
	rm -f /usr/local/sbin/sds-drbd-up.sh
	systemctl daemon-reload
	echo "old units removed on $(hostname)"
}

# PVE: the plugin is now HaifyPlugin with storage type "haify". The storage
# IDs stay (every guest config names them); the type and the plugin's own
# property names change, and so does the token file.
phase_pve() {
	cfg=/etc/pve/storage.cfg
	if grep -q '^sds: ' "$cfg"; then
		cp -a "$cfg" "/root/storage.cfg.pre-haify"
		sed -i -e 's/^sds: /haify: /' -e 's/^\(\s\+\)sdspool /\1haifypool /' -e 's/^\(\s\+\)sdsnodes /\1haifynodes /' "$cfg"
		echo "storage.cfg rewritten"
	fi
	for f in /etc/pve/priv/storage/*.sds-token; do
		[ -f "$f" ] || continue
		mv "$f" "${f%.sds-token}.haify-token"
	done
	rm -rf /usr/share/perl5/PVE/Storage/Custom/SDSPlugin.pm /usr/share/perl5/PVE/Storage/Custom/SDS
	rm -f /usr/share/pve-manager/js/sds-storage.js /etc/apt/apt.conf.d/90sds-pve-gui
	sed -i '/<!-- sds-storage -->/d' /usr/share/pve-manager/index.html.tpl
	cd /root/haify-plugin && ./install.sh
}

case "${1:-}" in
prep) phase_prep ;;
storage) phase_storage ;;
reattach) phase_reattach "$2" ;;
meta-up) phase_meta_up ;;
reactor) phase_reactor ;;
boot) phase_boot ;;
cleanup) phase_cleanup ;;
pve) phase_pve ;;
tx) shift; tx "$@" ;;
*) echo "usage: $0 prep|storage|meta-up|reactor|boot|cleanup" >&2; exit 2 ;;
esac
