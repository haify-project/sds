# shellcheck shell=bash
# Step 1 (cluster membership and SSH trust) and step 9 (quorum checks).

# read_members prints "local\t<this node>", then "<name>\t<ip>\t<online>" per
# member, from /etc/pve/.members: pmxcfs keeps it current and it carries name,
# address and online state together, which `pvecm nodes` does not.
read_members() {
	local members="$PVE_DIR/.members"
	[ -r "$members" ] || return 1
	# shellcheck disable=SC2016 # Perl, not shell
	json_query '
		exit 1 unless ref $j->{nodelist} eq "HASH" && %{ $j->{nodelist} };
		print "local\t", ($j->{nodename} // ""), "\n";
		for my $n (sort keys %{ $j->{nodelist} }) {
			my $e = $j->{nodelist}{$n};
			printf "%s\t%s\t%d\n", $n, ($e->{ip} // ""), ($e->{online} ? 1 : 0);
		}
	' < "$members"
}

# read_members_fallback does the same from corosync.conf (every member and its
# ring0 address) and `pvecm nodes` (the members currently in the quorum), for
# a node whose .members cannot be read.
read_members_fallback() {
	local conf="$PVE_DIR/corosync.conf" online name addr
	[ -r "$conf" ] || return 1
	online=$(pvecm nodes 2>/dev/null | awk '$1 ~ /^[0-9]+$/ && $2 ~ /^[0-9]+$/ { print $3 }') || return 1
	printf 'local\t%s\n' "$(hostname)"
	awk '
		/^nodelist[[:space:]]*\{/ { inlist = 1; next }
		inlist && /^}/ { inlist = 0 }
		inlist && $1 == "name:" { name = $2 }
		inlist && $1 == "ring0_addr:" { addr = $2 }
		inlist && /}/ && name != "" { print name "\t" addr; name = ""; addr = "" }
	' "$conf" | while IFS=$'\t' read -r name addr; do
		# ring0_addr may be a hostname; the controller and SSH need an address.
		case "$addr" in
			*[!0-9.:a-fA-F]*) addr=$(getent ahosts "$addr" | awk 'NR == 1 { print $1 }') ;;
		esac
		if printf '%s\n' "$online" | grep -qxF "$name"; then
			printf '%s\t%s\t1\n' "$name" "$addr"
		else
			printf '%s\t%s\t0\n' "$name" "$addr"
		fi
	done
}

discover_cluster() {
	local out name ip online offline=()
	if ! out=$(read_members); then
		log "$PVE_DIR/.members unreadable or without a node list; falling back to pvecm nodes + corosync.conf"
		out=$(read_members_fallback) ||
			die "this node is not in a PVE cluster. Create one first (pvecm create <name>; a one-node cluster is fine), then rerun."
	fi
	while IFS=$'\t' read -r name ip online; do
		if [ "$name" = local ]; then
			LOCAL_NODE="$ip"
			continue
		fi
		[ -n "$ip" ] || die "PVE reports no address for member '$name'"
		NODES+=("$name")
		NODE_IP["$name"]="$ip"
		[ "$online" = 1 ] || offline+=("$name")
	done <<< "$out"
	[ "${#NODES[@]}" -gt 0 ] || die "no cluster members found"
	if [ "${#offline[@]}" -gt 0 ]; then
		die "member(s) offline: ${offline[*]}. Every PVE node must be up: packages, registration and the plugin go on all of them."
	fi
	if [ -z "$LOCAL_NODE" ] || ! in_list "$LOCAL_NODE" "${NODES[@]}"; then
		die "this node ('$LOCAL_NODE') is not in the member list; run bootstrap.sh on a PVE cluster member"
	fi
}

# step_cluster always runs, also when resuming with --from-step: every later
# step needs the member list, and it changes nothing.
step_cluster() {
	step 1 "cluster membership and root SSH between nodes"
	discover_cluster
	local n
	for n in "${NODES[@]}"; do
		note "$n  ${NODE_IP[$n]}$([ "$n" = "$LOCAL_NODE" ] && echo '  (this node)')"
	done
	# PVE sets up root SSH between members when a node joins (keys and known
	# hosts kept in /etc/pve). Bootstrap uses that trust and adds none of its
	# own, so a node it does not reach is a PVE problem to fix first.
	for n in "${NODES[@]}"; do
		if ! reachable "$n"; then
			die "root SSH from $LOCAL_NODE to $n (${NODE_IP[$n]}) failed in batch mode.
PVE configures this when a node joins; repair it with 'pvecm updatecerts' on each node, then check:
  ssh -o BatchMode=yes -o HostKeyAlias=$n root@${NODE_IP[$n]} true"
		fi
	done
	log "all ${#NODES[@]} member(s) online and reachable as root over SSH"
}

# ---------------------------------------------------------------------------
# Step 9: report what quorum still lacks. Nothing here is configured
# automatically: a QDevice needs a host outside the cluster that only the
# operator can choose.
# ---------------------------------------------------------------------------

step_quorum_checks() {
	step 9 "quorum: corosync QDevice and DRBD tiebreaker"
	local missing=0 status expected flags
	status=$(check_on "$LOCAL_NODE" "pvecm status" 2>/dev/null) || status=""
	expected=$(printf '%s\n' "$status" | awk -F: '/^Expected votes/ { gsub(/[[:space:]]/, "", $2); print $2 }')
	flags=$(printf '%s\n' "$status" | awk -F: '/^Flags/ { print $2 }')
	if [ -z "$expected" ]; then
		warn "could not read 'pvecm status'; check corosync quorum by hand"
		missing=1
	elif printf '%s' "$flags" | grep -qw Qdevice; then
		log "OK: corosync has a QDevice (expected votes: $expected)"
	elif [ $((expected % 2)) -eq 0 ]; then
		warn "MISSING: corosync has $expected votes and no QDevice. Losing half the nodes loses PVE quorum,"
		warn "  and PVE HA fences the rest. Add one on a host outside the cluster:"
		warn "  apt install corosync-qdevice (every node), corosync-qnetd (that host), then"
		warn "  pvecm qdevice setup <host-ip>   (https://pve.proxmox.com/wiki/Cluster_Manager#_corosync_external_vote_support)"
		missing=1
	else
		log "OK: corosync has an odd number of votes ($expected); no QDevice needed"
	fi

	# DRBD keeps its own quorum per resource. A disk with two diskful replicas
	# needs a third, diskless voter, which sds adds automatically ([resource]
	# auto_tiebreaker) only when a third registered node exists.
	if [ "$REPLICAS" -eq 2 ]; then
		if [ "${#NODES[@]}" -lt 3 ]; then
			warn "MISSING: replicas=2 and only ${#NODES[@]} sds node(s): no node is left to be a DRBD tiebreaker,"
			warn "  so losing either node suspends I/O on the other. Add a third machine with drbd-dkms and"
			warn "  drbd-utils (it needs no disk; the QDevice host can serve), then on the controller:"
			warn "  sds node register --name <name> --address <ip>"
			missing=1
		elif check_on "$CONTROLLER_NODE" "grep -Eq '^[[:space:]]*auto_tiebreaker[[:space:]]*=[[:space:]]*false' /etc/sds/controller.toml" 2>/dev/null; then
			warn "MISSING: [resource] auto_tiebreaker = false in /etc/sds/controller.toml: two-replica disks get"
			warn "  no tiebreaker. Turn it back on, or give each disk one: sds ha set-tiebreaker <resource> --node <node>"
			missing=1
		else
			log "OK: ${#NODES[@]} sds nodes; a two-replica disk gets a diskless tiebreaker on a third node"
		fi
	elif [ "$REPLICAS" -eq 1 ]; then
		warn "replicas=1: disks have a single copy and survive no node loss"
	else
		log "OK: replicas=$REPLICAS keeps DRBD quorum through one node loss without a tiebreaker"
	fi

	if [ "$missing" = 1 ]; then
		warn "the storage works, but the items marked MISSING above leave a node loss able to stop it"
	fi
	return 0
}
