#!/bin/bash
# Bootstrap Haify on an existing Proxmox VE cluster, from any one of its nodes.
#
# Run as root on a PVE node. It reuses PVE's own root SSH trust between the
# members and, step by step, on every node:
#
#   1  reads the members (/etc/pve/.members) and checks root SSH to each
#   2  adds the LINBIT repository; installs kernel headers, drbd-dkms,
#      drbd-utils, drbd-reactor, sudo; loads DRBD 9; starts drbd-reactor
#   3  writes the controller's dispatch SSH config
#   4  installs sds-controller (package or binaries) and controller.toml
#   5  starts the controller, registers every node under its PVE name, and
#      creates the pool on the given devices
#   6  enables controller Self-HA (two or more storage nodes)
#   7  runs preflight.sh and installs the storage plugin
#   8  adds the storage to /etc/pve/storage.cfg
#   9  reports what quorum still lacks (QDevice, DRBD tiebreaker)
#
# Each step checks before it changes anything, so a rerun on a bootstrapped
# cluster changes nothing. See README.md, "Bootstrap a cluster".

set -euo pipefail
shopt -s inherit_errexit

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/../.." && pwd)"

# shellcheck source=bootstrap/lib.sh
. "$SCRIPT_DIR/bootstrap/lib.sh"
# shellcheck source=bootstrap/cluster.sh
. "$SCRIPT_DIR/bootstrap/cluster.sh"
# shellcheck source=bootstrap/drbd.sh
. "$SCRIPT_DIR/bootstrap/drbd.sh"
# shellcheck source=bootstrap/controller.sh
. "$SCRIPT_DIR/bootstrap/controller.sh"
# shellcheck source=bootstrap/storage.sh
. "$SCRIPT_DIR/bootstrap/storage.sh"
# shellcheck source=bootstrap/plugin.sh
. "$SCRIPT_DIR/bootstrap/plugin.sh"

usage() {
	cat <<EOF
Usage: $0 --devices DEV[,DEV...] [options]

Sets up Haify on the PVE cluster this node belongs to. Run as root.

Storage:
  --devices DEVS          Blank disk(s) for the pool on every storage node, comma-separated
                          (required; bootstrap never guesses). Prefer /dev/disk/by-id/ paths.
  --node-devices N=DEVS   Different disk(s) on node N (repeatable), e.g. pve3=/dev/nvme1n1
  --storage-nodes N,N     Nodes that get the pool (default: every member). The others run
                          guests as diskless DRBD clients and can be quorum tiebreakers.
  --pool NAME             sds pool name (default: vg0; the volume group is sds_vg0)
  --pool-type TYPE        lvm-thin (default; snapshots) or lvm
  --force-wipe            Wipe partition tables and signatures from the devices first.
                          A disk that is mounted or in use is refused regardless.

Controller:
  --vip CIDR              Floating address for the controller under Self-HA, e.g.
                          192.168.1.250/24. Required with two or more storage nodes.
  --no-self-ha            Run the controller on one node only (a single point of failure)

PVE storage:
  --storage-id ID         storage.cfg ID (default: sds0)
  --replicas N            Copies of each disk (default: 2, or 1 with one storage node)

Run control:
  --dry-run               Run only read-only checks; print every changing command per node
  --yes                   Do not ask for confirmation
  --from-step N           Resume at step N (1-9) after fixing a failed step
  --verbose               Also print the read-only checks
  -h, --help              This text

Environment:
  SDS_CONTROLLER_DEB      sds-controller_<ver>_<arch>.deb to install instead of binaries
  SDS_BIN_DIR             Directory with linux sds-controller, service-ip, sds
                          (default: $REPO_ROOT/bin)
  SDS_CONFIG_DIR          Directory with sds-controller.service, service-ip@.service,
                          controller.toml.example (default: $REPO_ROOT/configs)
  SDS_PLUGIN_DEB          Storage plugin .deb to install instead of running install.sh
  SDS_LINBIT_REPO         Whole APT source line for DRBD (default: LINBIT public, proxmox-<major>)
  SDS_LINBIT_KEY_URL      Signing key URL (default: $LINBIT_KEY_URL)
  SDS_LINBIT_KEY_FINGERPRINT  Refuse the key unless its fingerprint matches
EOF
}

# ---------------------------------------------------------------------------
# Options
# ---------------------------------------------------------------------------

ORIG_ARGS=("$@")
DEVICES=""
declare -A NODE_DEVICES=()
STORAGE_NODES_ARG=""
POOL=vg0
POOL_TYPE=lvm-thin
FORCE_WIPE=0
VIP=""
NO_SELF_HA=0
STOREID=sds0
REPLICAS=""
ASSUME_YES=0
FROM_STEP=1

need_arg() { [ $# -ge 2 ] && [ -n "$2" ] || die "$1 needs a value (see --help)"; }

while [ $# -gt 0 ]; do
	case "$1" in
		--devices) need_arg "$@"; DEVICES="$2"; shift 2 ;;
		--node-devices)
			need_arg "$@"
			case "$2" in
				?*=?*) NODE_DEVICES["${2%%=*}"]="${2#*=}" ;;
				*) die "--node-devices takes NODE=DEV[,DEV...], got '$2'" ;;
			esac
			shift 2
			;;
		--storage-nodes) need_arg "$@"; STORAGE_NODES_ARG="$2"; shift 2 ;;
		--pool) need_arg "$@"; POOL="$2"; shift 2 ;;
		--pool-type) need_arg "$@"; POOL_TYPE="$2"; shift 2 ;;
		--force-wipe) FORCE_WIPE=1; shift ;;
		--vip) need_arg "$@"; VIP="$2"; shift 2 ;;
		--no-self-ha) NO_SELF_HA=1; shift ;;
		--storage-id) need_arg "$@"; STOREID="$2"; shift 2 ;;
		--replicas) need_arg "$@"; REPLICAS="$2"; shift 2 ;;
		--dry-run) DRY_RUN=1; shift ;;
		--yes | -y) ASSUME_YES=1; shift ;;
		--from-step) need_arg "$@"; FROM_STEP="$2"; shift 2 ;;
		--verbose | -v) VERBOSE=1; shift ;;
		-h | --help) usage; exit 0 ;;
		*) die "unknown argument '$1' (see --help)" ;;
	esac
done

CONTROLLER_DEB=${SDS_CONTROLLER_DEB:-}
PLUGIN_DEB=${SDS_PLUGIN_DEB:-}
BIN_DIR=${SDS_BIN_DIR:-$REPO_ROOT/bin}
CONFIG_DIR=${SDS_CONFIG_DIR:-$REPO_ROOT/configs}

case "$FROM_STEP" in [1-9]) ;; *) die "--from-step takes 1-9" ;; esac
case "$POOL_TYPE" in
	lvm-thin | lvm) ;;
	*) die "--pool-type must be lvm-thin or lvm (Self-HA's metadata volume is an LVM volume; create ZFS pools by hand)" ;;
esac
[[ "$POOL" =~ ^[A-Za-z0-9][A-Za-z0-9_.-]*$ ]] || die "--pool '$POOL': use letters, digits, '_', '.', '-'"
[[ "$STOREID" =~ ^[A-Za-z][A-Za-z0-9_.-]*$ ]] || die "--storage-id '$STOREID' is not a valid PVE storage ID"
[ -n "$PLUGIN_DEB" ] && { [ -r "$PLUGIN_DEB" ] || die "SDS_PLUGIN_DEB=$PLUGIN_DEB is not readable"; }

if [ "$DRY_RUN" = 0 ] && [ "$(id -u)" -ne 0 ]; then
	die "run as root (or try --dry-run)"
fi

WORK_DIR=$(mktemp -d "${TMPDIR:-/tmp}/sds-bootstrap.XXXXXX")
CURRENT_STEP=0

# on_exit removes the scratch files and, after a failure, says where to pick
# up again: every step re-checks what is done, so resuming at the failed step
# with the same arguments is safe.
on_exit() {
	local rc=$?
	rm -rf "$WORK_DIR"
	if [ "$rc" -ne 0 ] && [ "$CURRENT_STEP" -gt 0 ]; then
		printf '\nStep %s failed. Fix the cause above, then resume with:\n  %s' "$CURRENT_STEP" "$0" >&2
		local a skip=0
		for a in "${ORIG_ARGS[@]}"; do
			if [ "$skip" = 1 ]; then skip=0; continue; fi
			case "$a" in
				--from-step) skip=1; continue ;;
				--yes | -y) continue ;;
			esac
			printf ' %q' "$a" >&2
		done
		printf ' --from-step %s\n' "$CURRENT_STEP" >&2
	fi
	exit "$rc"
}
trap on_exit EXIT

# ---------------------------------------------------------------------------
# Plan: which nodes hold storage, which can run the controller
# ---------------------------------------------------------------------------

CURRENT_STEP=1
step_cluster
# What follows checks the arguments against the cluster; a mistake there is
# fixed by changing the command line, not by resuming.
CURRENT_STEP=0

if [ -n "$STORAGE_NODES_ARG" ]; then
	split_csv "$STORAGE_NODES_ARG" STORAGE_NODES
	for n in "${STORAGE_NODES[@]}"; do
		in_list "$n" "${NODES[@]}" || die "--storage-nodes: '$n' is not a member of this PVE cluster (${NODES[*]})"
	done
else
	STORAGE_NODES=("${NODES[@]}")
fi
[ "${#STORAGE_NODES[@]}" -gt 0 ] || die "no storage nodes"
for n in "${!NODE_DEVICES[@]}"; do
	in_list "$n" "${STORAGE_NODES[@]}" || die "--node-devices names '$n', which is not a storage node"
done
if [ -z "$DEVICES" ]; then
	for n in "${STORAGE_NODES[@]}"; do
		[ -n "${NODE_DEVICES[$n]:-}" ] ||
			die "--devices is required (no disk given for $n). Bootstrap never picks a disk itself: name a blank one, e.g. --devices /dev/disk/by-id/..."
	done
fi

if [ -z "$REPLICAS" ]; then
	REPLICAS=2
	[ "${#STORAGE_NODES[@]}" -lt 2 ] && REPLICAS=1
fi
[[ "$REPLICAS" =~ ^[0-9]+$ ]] && [ "$REPLICAS" -ge 1 ] || die "--replicas must be a positive number"
[ "$REPLICAS" -le "${#STORAGE_NODES[@]}" ] ||
	die "--replicas $REPLICAS needs at least $REPLICAS storage nodes; there are ${#STORAGE_NODES[@]}"

# The controller starts on this node when it holds storage: Self-HA hands it
# over from a node with a replica of the metadata volume.
if in_list "$LOCAL_NODE" "${STORAGE_NODES[@]}"; then
	CONTROLLER_NODE="$LOCAL_NODE"
else
	CONTROLLER_NODE="${STORAGE_NODES[0]}"
fi

SELF_HA=0
if [ "${#STORAGE_NODES[@]}" -ge 2 ] && [ "$NO_SELF_HA" = 0 ]; then
	SELF_HA=1
	[ -n "$VIP" ] || die "--vip is required with ${#STORAGE_NODES[@]} storage nodes: Self-HA moves the controller between them behind it.
Pass a free address in the nodes' subnet in CIDR form (e.g. --vip 192.168.1.250/24), or --no-self-ha."
	[[ "$VIP" =~ ^[0-9a-fA-F.:]+/[0-9]+$ ]] || die "--vip must be in CIDR form, e.g. 192.168.1.250/24"
	CONTROLLER_NODES=("${STORAGE_NODES[@]}")
else
	CONTROLLER_NODES=("$CONTROLLER_NODE")
fi

if [ "$FROM_STEP" -le 4 ]; then
	check_artifacts
fi
if [ "$FROM_STEP" -le 5 ]; then
	verify_devices
fi

print_plan() {
	local n
	printf '\nPlan:\n'
	printf '  PVE nodes:         %s\n' "${NODES[*]}"
	printf '  storage nodes:     %s\n' "${STORAGE_NODES[*]}"
	for n in "${STORAGE_NODES[@]}"; do
		printf '    %-15s  %s\n' "$n" "${NODE_DEVICES[$n]:-$DEVICES}"
	done
	printf '  pool:              %s (%s)%s\n' "$POOL" "$POOL_TYPE" "$([ "$FORCE_WIPE" = 1 ] && echo ', devices wiped first if not blank')"
	printf '  controller:        starts on %s; may run on %s\n' "$CONTROLLER_NODE" "${CONTROLLER_NODES[*]}"
	if [ "$SELF_HA" = 1 ]; then
		printf '  Self-HA:           on, VIP %s\n' "$VIP"
	else
		printf '  Self-HA:           off\n'
	fi
	if [ -n "$CONTROLLER_DEB" ]; then
		printf '  controller from:   %s\n' "$CONTROLLER_DEB"
	else
		printf '  controller from:   %s\n' "$BIN_DIR"
	fi
	printf '  plugin from:       %s\n' "${PLUGIN_DEB:-$SCRIPT_DIR (install.sh)}"
	printf '  PVE storage:       %s, replicas %s, controller %s\n' "$STOREID" "$REPLICAS" "$(controller_list)"
	printf '  steps:             %s-9\n' "$FROM_STEP"
}
print_plan

if [ "$DRY_RUN" = 1 ]; then
	printf '\nDry run: the read-only checks run; each change is printed, with its node, instead of made.\n'
elif [ "$ASSUME_YES" = 0 ]; then
	[ -t 0 ] || die "not on a terminal; pass --yes to proceed without confirmation"
	printf '\nThis installs packages and changes storage on the nodes above. Proceed? [y/N] '
	read -r answer
	case "$answer" in
		y | Y | yes | YES) ;;
		*) echo "aborted"; exit 1 ;;
	esac
fi

# ---------------------------------------------------------------------------
# Steps
# ---------------------------------------------------------------------------

run_step() {
	local n="$1"
	shift
	[ "$FROM_STEP" -le "$n" ] || return 0
	CURRENT_STEP="$n"
	"$@"
}

# Resuming past step 5: the controller may meanwhile run elsewhere (Self-HA).
if [ "$FROM_STEP" -gt 5 ] && ! locate_controller && [ "$DRY_RUN" = 0 ]; then
	CURRENT_STEP=5
	die "no sds-controller is running on any node"
fi

run_step 2 step_drbd
run_step 3 step_dispatch
run_step 4 step_controller_install
run_step 5 step_register_and_pool
run_step 6 step_self_ha
run_step 7 step_plugin
run_step 8 step_storage_cfg
run_step 9 step_quorum_checks

CURRENT_STEP=0
printf '\nDone.'
if [ "$DRY_RUN" = 1 ]; then
	printf ' (dry run: nothing was changed)\n'
else
	printf ' Storage %s is available on every node: create a VM disk on it, e.g.\n' "$STOREID"
	printf '  qm set <vmid> --scsi1 %s:10\n' "$STOREID"
fi
