# shellcheck shell=bash
# Step 2: the LINBIT repository, DRBD 9 (DKMS), drbd-utils and drbd-reactor on
# every node.
#
# Every PVE node needs DRBD, not only the ones holding disks: a guest started
# on a node without a replica attaches as a diskless DRBD client, which needs
# the kernel module and drbdadm there too.

LINBIT_KEY_URL=${SDS_LINBIT_KEY_URL:-https://packages.linbit.com/package-signing-pubkey.asc}
LINBIT_KEY_FINGERPRINT=${SDS_LINBIT_KEY_FINGERPRINT:-}
LINBIT_KEYRING=/etc/apt/keyrings/linbit.asc
LINBIT_LIST=/etc/apt/sources.list.d/linbit.list

DRBD_PACKAGES=(drbd-dkms drbd-utils drbd-reactor sudo)

# linbit_repo_line <pve major>: the APT source line. SDS_LINBIT_REPO replaces
# it whole, e.g. for a mirror or LINBIT's customer repository.
linbit_repo_line() {
	if [ -n "${SDS_LINBIT_REPO:-}" ]; then
		printf '%s' "$SDS_LINBIT_REPO"
	else
		printf 'deb [signed-by=%s] https://packages.linbit.com/public proxmox-%s drbd-9' "$LINBIT_KEYRING" "$1"
	fi
}

# pve_major <node>: 8 for pve-manager/8.x, 9 for 9.x.
pve_major() {
	local v
	v=$(check_on "$1" "pveversion" | sed -n 's|^pve-manager/\([0-9][0-9]*\)\..*|\1|p' | head -1)
	[ -n "$v" ] || die "could not read the PVE version on $1 (pveversion)"
	printf '%s' "$v"
}

# fetch_cmd <url> <dest>: download on the node with whichever of curl and
# wget it has (PVE ships both, a trimmed install may not).
fetch_cmd() {
	local url dest
	url=$(q "$1")
	dest=$(q "$2")
	printf 'if command -v curl >/dev/null; then curl -fsSL -o %s %s; else wget -qO %s %s; fi' \
		"$dest" "$url" "$dest" "$url"
}

linbit_repo_present() {
	local node="$1" line
	line=$(linbit_repo_line "$2")
	check_on "$node" "test -s $(q "$LINBIT_KEYRING") && grep -qxF $(q "$line") $(q "$LINBIT_LIST")" >/dev/null 2>&1
}

add_linbit_repo() {
	local node="$1" major="$2" line tmp fpr_check=""
	line=$(linbit_repo_line "$major")
	tmp=/tmp/.sds-bootstrap-linbit.asc
	if [ -n "$LINBIT_KEY_FINGERPRINT" ]; then
		# Pinning the fingerprint is what turns an HTTPS download into a key
		# the operator actually vouched for; gpg is needed only to read it.
		local want="${LINBIT_KEY_FINGERPRINT//[[:space:]]/}"
		fpr_check="command -v gpg >/dev/null || DEBIAN_FRONTEND=noninteractive apt-get install -y gnupg; "
		fpr_check+="got=\$(gpg --show-keys --with-colons $(q "$tmp") | awk -F: '\$1 == \"fpr\" { print \$10 }'); "
		fpr_check+="printf '%s\\n' \"\$got\" | grep -qixF $(q "$want") || "
		fpr_check+="{ echo \"LINBIT key fingerprint mismatch: got \$got, want $want\" >&2; rm -f $(q "$tmp"); exit 1; }; "
	fi
	# set -e: a failed download must stop here, not reach the fingerprint
	# check or install an empty keyring.
	run_on "$node" "set -e; $(fetch_cmd "$LINBIT_KEY_URL" "$tmp"); ${fpr_check}install -D -m 0644 $(q "$tmp") $(q "$LINBIT_KEYRING"); rm -f $(q "$tmp")"
	run_on "$node" "printf '%s\\n' $(q "$line") > $(q "$LINBIT_LIST")"
}

# missing_packages <node> <pkg...>: prints the ones not installed.
missing_packages() {
	local node="$1"
	shift
	local p cmd=""
	for p in "$@"; do
		cmd+="dpkg-query -W -f='\${Status}' $(q "$p") 2>/dev/null | grep -q 'install ok installed' || echo $(q "$p"); "
	done
	check_on "$node" "$cmd"
}

# headers_packages <node>: kernel headers for the running kernel, which DKMS
# builds against, plus the meta-package that keeps headers coming with every
# future PVE kernel. PVE 8+ names them proxmox-headers-*, older releases
# pve-headers-*.
headers_packages() {
	# shellcheck disable=SC2016 # expanded by the node's shell, not here
	check_on "$1" '
k=$(uname -r)
for p in "proxmox-headers-$k" "pve-headers-$k"; do
	if dpkg-query -W -f="\${Status}" "$p" 2>/dev/null | grep -q "install ok installed" ||
	   apt-cache show "$p" >/dev/null 2>&1; then echo "$p"; break; fi
done
for p in proxmox-default-headers pve-headers; do
	if apt-cache show "$p" >/dev/null 2>&1; then echo "$p"; break; fi
done'
}

ensure_drbd_reactor_config() {
	local node="$1"
	# drbd-reactor reads promoter configs (the Self-HA one among them) only
	# from the snippets directory named in its main config; without the file
	# it does not start at all.
	if ! check_on "$node" "test -f /etc/drbd-reactor.toml" >/dev/null; then
		run_on "$node" "mkdir -p /etc/drbd-reactor.d && printf '%s\\n' 'snippets = \"/etc/drbd-reactor.d\"' '' '[[log]]' 'level = \"info\"' > /etc/drbd-reactor.toml"
	elif ! check_on "$node" "grep -Eq '^[[:space:]]*snippets[[:space:]]*=' /etc/drbd-reactor.toml" >/dev/null; then
		die "$node: /etc/drbd-reactor.toml has no 'snippets = \"/etc/drbd-reactor.d\"' line; add it (sds writes its promoters there) and rerun with --from-step 2"
	fi
	if check_on "$node" "systemctl is-enabled --quiet drbd-reactor && systemctl is-active --quiet drbd-reactor" >/dev/null; then
		note "$node: drbd-reactor enabled and running"
	else
		run_on "$node" "systemctl enable --now drbd-reactor"
	fi
}

# ensure_drbd9_loaded: the DKMS module must be the one in the kernel. PVE's
# kernel carries an in-tree DRBD 8.4, which sds cannot use.
ensure_drbd9_loaded() {
	local node="$1" ver
	ver=$(check_on "$node" "cat /sys/module/drbd/version 2>/dev/null" || true)
	case "$ver" in
		9.*)
			note "$node: DRBD $ver loaded"
			return 0
			;;
		"") ;;
		*)
			# Installing the packages loads the in-tree 8.4 before the DKMS
			# build lands (drbd-utils' udev rules on a fresh node), so an
			# 8.4 that nothing uses is swapped for 9 here. One with devices
			# configured or held open is someone's, and is left alone.
			if [ "$(check_on "$node" "echo \$(cat /sys/module/drbd/refcnt) \$(grep -cE '^ *[0-9]+:' /proc/drbd)" || true)" != "0 0" ]; then
				die "$node: DRBD $ver is loaded and in use; sds needs 9. Stop what uses it, 'rmmod drbd', and rerun with --from-step 2"
			fi
			note "$node: DRBD $ver is loaded but unused: replacing it with the DKMS module"
			run_on "$node" "rmmod drbd"
			;;
	esac
	run_on "$node" "modprobe drbd"
	[ "$DRY_RUN" = 1 ] && return 0
	ver=$(check_on "$node" "cat /sys/module/drbd/version 2>/dev/null" || true)
	case "$ver" in
		9.*) note "$node: DRBD $ver loaded" ;;
		*) die "$node: the loaded DRBD module is '${ver:-none}', not 9.x. Check the DKMS build: dkms status; ls /lib/modules/\$(uname -r)/updates/dkms/" ;;
	esac
}

step_drbd() {
	step 2 "LINBIT repository and DRBD 9 / drbd-utils / drbd-reactor on every node"
	local node major missing headers hdr kernel
	if [ -z "$LINBIT_KEY_FINGERPRINT" ]; then
		note "SDS_LINBIT_KEY_FINGERPRINT is not set: a node that lacks the LINBIT key trusts the one"
		note "downloaded over HTTPS from $LINBIT_KEY_URL"
	fi
	for node in "${NODES[@]}"; do
		major=$(pve_major "$node")
		case "$major" in
			8 | 9) ;;
			*) die "$node runs PVE $major; the LINBIT public repository has suites for proxmox-8 and proxmox-9 only (set SDS_LINBIT_REPO to override)" ;;
		esac
		log "$node: PVE $major"
		local repo_added=0
		if linbit_repo_present "$node" "$major"; then
			note "$node: LINBIT repository already configured (proxmox-$major)"
		else
			add_linbit_repo "$node" "$major"
			repo_added=1
		fi

		# The repository is new, so the package index does not know it yet;
		# refresh before asking which headers package exists.
		if [ "$repo_added" = 1 ]; then
			run_on "$node" "apt-get update"
		fi
		headers=()
		while IFS= read -r hdr; do
			[ -n "$hdr" ] && headers+=("$hdr")
		done < <(headers_packages "$node" || true)
		kernel=$(check_on "$node" "uname -r")
		# The meta-package alone would build DRBD for the newest kernel, not the
		# one running, and modprobe below would then find nothing to load.
		if ! printf '%s\n' "${headers[@]}" | grep -qxE "(proxmox|pve)-headers-${kernel//./\\.}"; then
			die "$node: no headers package for the running kernel $kernel; DKMS cannot build DRBD for it.
If a newer kernel is installed, reboot into it first; otherwise check the PVE repositories (apt-get update)."
		fi

		missing=$(missing_packages "$node" "${headers[@]}" "${DRBD_PACKAGES[@]}" | tr '\n' ' ')
		missing="${missing% }"
		if [ -z "$missing" ]; then
			note "$node: ${headers[*]} ${DRBD_PACKAGES[*]} already installed"
		else
			if [ "$repo_added" = 0 ]; then
				run_on "$node" "apt-get update"
			fi
			# confold keeps a configuration file the node already has (for
			# instance a hand-written /etc/drbd-reactor.toml) instead of stopping
			# at dpkg's conffile prompt, which over SSH fails and rolls back the
			# whole transaction.
			run_on "$node" "DEBIAN_FRONTEND=noninteractive apt-get install -y -o Dpkg::Options::=--force-confdef -o Dpkg::Options::=--force-confold $missing"
		fi
		ensure_drbd9_loaded "$node"
		ensure_drbd_reactor_config "$node"
	done
	warn "DRBD is a DKMS module: every PVE kernel upgrade rebuilds it, and a build that fails (a kernel"
	warn "  newer than the drbd-dkms release supports) leaves the node without DRBD after the reboot."
	warn "  After each kernel upgrade, before rebooting, check on that node:"
	warn "    dkms status drbd    # must list the new kernel as 'installed'"
}
