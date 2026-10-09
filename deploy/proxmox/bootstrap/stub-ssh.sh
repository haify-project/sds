#!/bin/bash
# Test double for ssh, used by test.sh (installed on PATH as `ssh`).
#
# It answers bootstrap.sh's read-only probes the way a node would in one of
# two states, STUB_MODE=fresh (nothing installed) or STUB_MODE=done (fully
# bootstrapped), and logs every call. A command it does not recognise is
# recorded in $STUB_DIR/unhandled so the test fails on probes nobody
# modelled, instead of passing by accident.

set -uo pipefail

node=""
args=()
while [ $# -gt 0 ]; do
	case "$1" in
		-o)
			case "$2" in HostKeyAlias=*) node="${2#HostKeyAlias=}" ;; esac
			shift 2
			;;
		-*) shift ;;
		*) args+=("$1"); shift ;;
	esac
done
cmd="${args[*]:1}"
printf '%s\t%s\n' "$node" "${cmd//$'\n'/ }" >> "$STUB_DIR/ssh.log"

if [ -n "${STUB_UNREACHABLE:-}" ] && [ "$node" = "$STUB_UNREACHABLE" ]; then
	echo "ssh: connect to host: Connection refused" >&2
	exit 255
fi

fresh() { [ "$STUB_MODE" = fresh ]; }
answer() { if fresh; then exit "$1"; else exit "$2"; fi; }

# done_sha <remote path>: the SHA-256 a bootstrapped node has for a file,
# i.e. that of the local file bootstrap would install there.
done_sha() {
	local src=""
	case "$1" in
		/opt/haify/bin/haify-controller) src="$HAIFY_BIN_DIR/haify-controller" ;;
		*/service-ip) src="$HAIFY_BIN_DIR/service-ip" ;;
		/usr/local/bin/haify) src="$HAIFY_BIN_DIR/haify" ;;
		/etc/systemd/system/*) src="$HAIFY_CONFIG_DIR/${1##*/}" ;;
		/root/.dispatch/config.toml)
			# The file bootstrap generated in its scratch directory.
			for src in "$TMPDIR"/haify-bootstrap.*/dispatch-config.toml; do break; done
			;;
		*/Custom/HaifyPlugin.pm) src="$STUB_PLUGIN_SRC/HaifyPlugin.pm" ;;
		*/Custom/Haify/*.pm) src="$STUB_PLUGIN_SRC/PVE/Storage/Custom/Haify/${1##*/}" ;;
		*/pve-manager/js/haify-storage.js) src="$STUB_PLUGIN_SRC/gui/haify-storage.js" ;;
	esac
	[ -n "$src" ] && [ -r "$src" ] && sha256sum "$src" | cut -d' ' -f1
}

# shellcheck disable=SC2016 # patterns match the probes' text literally
case "$cmd" in
	true) exit 0 ;;
	pveversion) echo "pve-manager/${STUB_PVE_MAJOR:-8}.2.4/faa83925c9641325 (running kernel: 6.8.12-1-pve)" ;;
	"uname -r") echo 6.8.12-1-pve ;;
	*linbit.asc*grep\ -qxF*) answer 1 0 ;;
	*proxmox-headers-*apt-cache*) printf '%b\n' "${STUB_HEADERS:-proxmox-headers-6.8.12-1-pve\nproxmox-default-headers}" ;;
	*'${Status} ${Version}'*)
		# deb_is_installed: report the version of the package under test.
		fresh && exit 1
		printf 'install ok installed %s' "${STUB_DEB_VERSION:-}"
		;;
	*"dpkg-query -W"*"|| echo"*)
		# missing_packages: fresh nodes miss every package asked about.
		if fresh; then printf '%s\n' "$cmd" | grep -o 'echo [a-z0-9.+-]*' | cut -d' ' -f2; fi
		;;
	*/sys/module/drbd/refcnt*) echo "${STUB_DRBD_USE:-0 0}" ;;
	*/sys/module/drbd/version*)
		# STUB_DRBD_VERSION: a module already loaded on a fresh node (the
		# kernel's in-tree 8.4, pulled in while the packages installed).
		if [ -n "${STUB_DRBD_VERSION:-}" ]; then echo "$STUB_DRBD_VERSION"; exit 0; fi
		fresh && exit 1
		echo 9.2.12
		;;
	"test -f /etc/drbd-reactor.toml") answer 1 0 ;;
	*"snippets"*"/etc/drbd-reactor.toml"*) answer 1 0 ;;
	*"systemctl is-enabled --quiet drbd-reactor"*) answer 1 0 ;;
	"test -r /root/.ssh/id_rsa") exit 0 ;;
	"test -r "*) exit 1 ;;
	sha256sum\ *)
		fresh && exit 0
		f="${cmd#sha256sum }"
		f="${f%% 2>*}"
		done_sha "$f"
		;;
	"test -d /etc/haify"*) answer 1 0 ;;
	"test -L /usr/local/bin/haify-cli") answer 1 0 ;;
	"test -f /etc/haify/controller.toml") answer 1 0 ;;
	"systemctl is-active --quiet haify-controller")
		fresh && exit 3
		[ "$node" = "${STUB_ACTIVE_CONTROLLER:-pve1}" ] && exit 0
		exit 3
		;;
	"drbdadm role haify-meta") fresh && exit 10; echo Secondary ;;
	*"/v1/nodes")
		fresh && exit 22
		printf '{"success":true,"nodes":[%s]}' "$STUB_NODES_JSON"
		;;
	*"/v1/pools")
		fresh && exit 22
		printf '{"success":true,"pools":[%s]}' "$STUB_POOLS_JSON"
		;;
	"haify ha self status")
		fresh && exit 1
		printf 'Controller self-HA: enabled\n  Active node: pve1\n'
		;;
	"vgs haify_vg0") answer 5 0 ;;
	d=*readlink*)
		# The device probe.
		echo "${STUB_DEVICE_VERDICT:-clean}"
		;;
	*/etc/pve/storage.cfg*)
		echo "dir local"
		echo "lvmthin local-lvm"
		fresh || echo "haify haify0"
		[ -n "${STUB_STORAGE_EXTRA:-}" ] && echo "$STUB_STORAGE_EXTRA"
		;;
	"pvecm status")
		printf 'Votequorum information\n----------------------\n'
		printf 'Expected votes:   %s\nTotal votes:      %s\nFlags:            Quorate%s\n' \
			"$STUB_VOTES" "$STUB_VOTES" "${STUB_QDEVICE:+ Qdevice}"
		;;
	*auto_tiebreaker*) exit 1 ;;
	*)
		printf '%s\t%s\n' "$node" "$cmd" >> "$STUB_DIR/unhandled"
		exit 1
		;;
esac
exit 0
