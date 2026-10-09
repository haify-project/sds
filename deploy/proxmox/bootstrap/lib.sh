# shellcheck shell=bash
# Shared helpers for deploy/proxmox/bootstrap.sh: logging, the single place
# commands reach a node (run_on / check_on / copy_to), and small parsers.
#
# Every command that runs on a node goes through run_on (changes) or check_on
# (read-only probes). That split is what makes --dry-run trustworthy: a dry run
# executes the probes, so the plan reflects the cluster as it is, and prints
# every run_on/copy_to instead of executing it.

# Node tables, filled by discover_cluster (cluster.sh) and the plan in
# bootstrap.sh, and read by every step.
# shellcheck disable=SC2034 # used by the files sourced next to this one
{
	declare -gA NODE_IP=()
	declare -ga NODES=() STORAGE_NODES=() CONTROLLER_NODES=()
	LOCAL_NODE=""
	CONTROLLER_NODE=""
}

# Options shared by every step; bootstrap.sh sets them from the command line
# only, so a stray variable in the caller's environment cannot flip them.
DRY_RUN=0
VERBOSE=0

# Where PVE's cluster filesystem is mounted. Only the tests point it elsewhere.
PVE_DIR=${HAIFY_PVE_DIR:-/etc/pve}

log()  { printf '[%s] %s\n' "$(date +%H:%M:%S)" "$*"; }
step() { printf '\n=== Step %s: %s ===\n' "$1" "$2"; }
note() { printf '      %s\n' "$*"; }
warn() { printf 'WARNING: %s\n' "$*" >&2; }
die()  { printf 'ERROR: %s\n' "$*" >&2; exit 1; }

# q quotes one value for a remote shell. Remote commands are strings run by
# the node's shell, so every operand that came from input or discovery is
# passed through q, never pasted in raw. Plain words stay as they are and
# anything else is single-quoted, which keeps the commands a dry run prints
# readable and copy-pasteable (printf %q would backslash every comma).
q() {
	case "$1" in
		"" | *[!A-Za-z0-9_./:=,@+%-]*) printf "'%s'" "${1//\'/\'\\\'\'}" ;;
		*) printf '%s' "$1" ;;
	esac
}

# ssh_opts_for fills SSH_OPTS for <node> the way PVE itself reaches its peers
# (PVE::SSHInfo): batch mode, the host key checked under the node's *name*,
# and, where PVE keeps one, the node's own known_hosts file in /etc/pve. The
# trust PVE established when the node joined is therefore verified rather
# than bypassed: no StrictHostKeyChecking=no, no accept-new.
ssh_opts_for() {
	local node="$1" kh="$PVE_DIR/nodes/$1/ssh_known_hosts"
	SSH_OPTS=(-o BatchMode=yes -o ConnectTimeout=10 -o "HostKeyAlias=$node")
	if [ -f "$kh" ]; then
		SSH_OPTS+=(-o "UserKnownHostsFile=$kh" -o GlobalKnownHostsFile=none)
	fi
}

node_ip() {
	local ip="${NODE_IP[$1]:-}"
	[ -n "$ip" ] || die "internal: no address known for node '$1'"
	printf '%s' "$ip"
}

# scp_target prints root@<ip>:<path>, bracketing an IPv6 address.
scp_target() {
	local ip
	ip=$(node_ip "$1")
	case "$ip" in
		*:*) printf 'root@[%s]:%s' "$ip" "$2" ;;
		*) printf 'root@%s:%s' "$ip" "$2" ;;
	esac
}

# run_on <node> <command>: run a command that changes something. In a dry run
# it is printed, tagged with the node, and not executed.
run_on() {
	local node="$1" cmd="$2"
	if [ "$DRY_RUN" = 1 ]; then
		printf '[dry-run] %-12s %s\n' "$node" "$cmd"
		return 0
	fi
	log "[$node] $cmd"
	ssh_opts_for "$node"
	# shellcheck disable=SC2029 # the command is built to run on the node
	ssh "${SSH_OPTS[@]}" "root@$(node_ip "$node")" "$cmd"
}

# check_on <node> <command>: run a read-only probe, also in a dry run, and
# pass its stdout and exit status through. ssh's own failure (255) is never
# mistaken for "the probe said no": it stops the bootstrap.
check_on() {
	local node="$1" cmd="$2" rc=0
	[ "$VERBOSE" = 1 ] && printf '[check]   %-12s %s\n' "$node" "$cmd" >&2
	ssh_opts_for "$node"
	# shellcheck disable=SC2029 # the command is built to run on the node
	ssh "${SSH_OPTS[@]}" "root@$(node_ip "$node")" "$cmd" || rc=$?
	if [ "$rc" -eq 255 ]; then
		die "cannot reach $node over SSH (probe: $cmd)"
	fi
	return "$rc"
}

# reachable <node>: root SSH to the node works in batch mode.
reachable() {
	ssh_opts_for "$1"
	ssh "${SSH_OPTS[@]}" "root@$(node_ip "$1")" true >/dev/null 2>&1
}

# copy_to <node> <local file> <remote path> [mode]: install a local file on a
# node. The file lands in a temporary name first and is moved into place with
# install(1), so a dropped connection never leaves a half-written file at the
# real path.
copy_to() {
	local node="$1" src="$2" dest="$3" mode="${4:-0644}" tmp
	tmp="/tmp/.haify-bootstrap.$$.$(basename "$dest")"
	if [ "$DRY_RUN" = 1 ]; then
		printf '[dry-run] %-12s copy %s -> %s (mode %s)\n' "$node" "$src" "$dest" "$mode"
		return 0
	fi
	log "[$node] copy $src -> $dest"
	ssh_opts_for "$node"
	scp -q "${SSH_OPTS[@]}" "$src" "$(scp_target "$node" "$tmp")"
	run_on "$node" "install -D -m $(q "$mode") $(q "$tmp") $(q "$dest") && rm -f $(q "$tmp")"
}

# copy_dir_to <node> <local dir> <remote dir>: replace a remote directory
# with a copy of a local one (used for the plugin sources).
copy_dir_to() {
	local node="$1" src="$2" dest="$3"
	if [ "$DRY_RUN" = 1 ]; then
		printf '[dry-run] %-12s copy %s/ -> %s/\n' "$node" "$src" "$dest"
		return 0
	fi
	run_on "$node" "rm -rf $(q "$dest") && mkdir -p $(q "$(dirname "$dest")")"
	log "[$node] copy $src/ -> $dest/"
	ssh_opts_for "$node"
	# -p keeps the execute bits: the scripts in it are run, and call each other.
	scp -q -r -p "${SSH_OPTS[@]}" "$src" "$(scp_target "$node" "$dest")"
}

# remote_sha256 <node> <path>: the file's SHA-256 on the node, empty when it
# does not exist. Used to skip copies that would change nothing.
remote_sha256() {
	check_on "$1" "sha256sum $(q "$2") 2>/dev/null | cut -d' ' -f1" || true
}

local_sha256() { sha256sum "$1" | cut -d' ' -f1; }

# deb_field <file.deb> <field>: a control field of a local package.
deb_field() { dpkg-deb -f "$1" "$2"; }

# deb_is_installed <node> <file.deb>: true when the node already has this
# exact package version installed.
deb_is_installed() {
	local node="$1" deb="$2" pkg ver have
	pkg=$(deb_field "$deb" Package)
	ver=$(deb_field "$deb" Version)
	have=$(check_on "$node" "dpkg-query -W -f='\${Status} \${Version}' $(q "$pkg") 2>/dev/null" || true)
	[ "$have" = "install ok installed $ver" ]
}

# install_deb <node> <file.deb>: copy a local package to a node and install
# it with apt, so its dependencies are resolved from the node's repositories.
install_deb() {
	local node="$1" deb="$2" remote
	remote="/tmp/$(basename "$deb")"
	copy_to "$node" "$deb" "$remote" 0644
	run_on "$node" "DEBIAN_FRONTEND=noninteractive apt-get install -y $(q "$remote") && rm -f $(q "$remote")"
}

# in_list <word> <list...>
in_list() {
	local w="$1"
	shift
	local x
	for x in "$@"; do [ "$x" = "$w" ] && return 0; done
	return 1
}

# join_by <sep> <items...>
join_by() {
	local sep="$1" out=""
	shift
	local x
	for x in "$@"; do out="${out:+$out$sep}$x"; done
	printf '%s' "$out"
}

# split_csv <string> <array name>: split on commas, trimming blanks.
split_csv() {
	local -n _out="$2"
	local _parts _p
	_out=()
	IFS=',' read -ra _parts <<< "$1"
	for _p in "${_parts[@]}"; do
		_p="${_p//[[:space:]]/}"
		[ -n "$_p" ] && _out+=("$_p")
	done
	return 0
}

# rest_addr <ip>: an address as the plugin and preflight.sh accept it, with
# the IPv6 form bracketed so the port is unambiguous.
rest_addr() {
	case "$1" in
		*:*) printf '[%s]' "$1" ;;
		*) printf '%s' "$1" ;;
	esac
}

# json_query <perl expression over $j>: run on the local node (always a PVE
# node, so JSON::PP is there) against JSON read from stdin. The expression
# prints whatever the caller needs, one record per line.
json_query() {
	# shellcheck disable=SC2016 # Perl, not shell
	perl -MJSON::PP -0777 -e '
		my $in = <STDIN>;
		exit 1 unless defined $in && length $in;
		our $j = eval { JSON::PP->new->decode($in) };
		exit 1 unless $j;
		eval $ARGV[0];
		die $@ if $@;
	' "$1"
}
