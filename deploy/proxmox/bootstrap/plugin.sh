# shellcheck shell=bash
# Step 7 (the storage plugin on every node) and step 8 (the storage.cfg entry).

PLUGIN_DIR=/usr/share/perl5/PVE/Storage/Custom
PLUGIN_STAGING=/root/.sds-bootstrap-proxmox

# controller_list: what goes into storage.cfg's `controller`. Every node that
# can run the controller is listed: under Self-HA the plugin tries them in
# turn and skips the ones that refuse the connection, so whichever node is
# active is found without depending on the VIP.
controller_list() {
	local n out=()
	for n in "${CONTROLLER_NODES[@]}"; do
		out+=("$(rest_addr "${NODE_IP[$n]}")")
	done
	join_by , "${out[@]}"
}

# plugin_up_to_date <node>: the node already runs exactly these plugin files
# (or this plugin package version).
plugin_up_to_date() {
	local node="$1" f rel
	if [ -n "$PLUGIN_DEB" ]; then
		deb_is_installed "$node" "$PLUGIN_DEB"
		return
	fi
	[ "$(remote_sha256 "$node" "$PLUGIN_DIR/SDSPlugin.pm")" = "$(local_sha256 "$SCRIPT_DIR/SDSPlugin.pm")" ] || return 1
	for f in "$SCRIPT_DIR"/PVE/Storage/Custom/SDS/*.pm; do
		rel="${f#"$SCRIPT_DIR"/PVE/Storage/Custom/}"
		[ "$(remote_sha256 "$node" "$PLUGIN_DIR/$rel")" = "$(local_sha256 "$f")" ] || return 1
	done
	# The web interface's storage dialog ships with the plugin.
	[ "$(remote_sha256 "$node" /usr/share/pve-manager/js/sds-storage.js)" = "$(local_sha256 "$SCRIPT_DIR/gui/sds-storage.js")" ] || return 1
	return 0
}

step_plugin() {
	local controllers n
	controllers=$(controller_list)
	step 7 "preflight and install the SDS storage plugin on every node"
	for n in "${NODES[@]}"; do
		if plugin_up_to_date "$n"; then
			note "$n: plugin already installed and current"
			continue
		fi
		# preflight.sh and lvm-filter.sh come from this directory in both
		# modes: preflight is what proves the node can use the storage
		# (DRBD 9, sudo, controller reachable, node registered by its PVE name).
		copy_dir_to "$n" "$SCRIPT_DIR" "$PLUGIN_STAGING"
		run_on "$n" "cd $(q "$PLUGIN_STAGING") && ./preflight.sh $(q "$controllers")"
		if [ -n "$PLUGIN_DEB" ]; then
			install_deb "$n" "$PLUGIN_DEB"
		else
			# install.sh also adds the LVM filter that keeps the host off guest
			# volume groups and the storage dialog to the web interface, and
			# restarts the PVE daemons (running guests are not affected).
			run_on "$n" "cd $(q "$PLUGIN_STAGING") && ./install.sh"
		fi
		run_on "$n" "rm -rf $(q "$PLUGIN_STAGING")"
	done
}

# storage_type: the plugin's storagetype for the pool bootstrap created, so new
# disks are carved the way the pool was built rather than by the controller's
# default.
storage_type() {
	case "$POOL_TYPE" in
		lvm-thin) printf 'lvm-thin' ;;
		lvm) printf 'lvm' ;;
	esac
}

step_storage_cfg() {
	step 8 "add storage '$STOREID' to /etc/pve/storage.cfg"
	local existing controllers
	# storage.cfg is cluster-wide (pmxcfs), so one node's view is everyone's.
	# A cluster that never had a storage added has no storage.cfg at all.
	existing=$(check_on "$LOCAL_NODE" "test ! -f /etc/pve/storage.cfg || awk '/^[a-z][a-z0-9]*: / { sub(/:\$/, \"\", \$1); print \$1, \$2 }' /etc/pve/storage.cfg" || true)
	case "$(printf '%s\n' "$existing" | awk -v id="$STOREID" '$2 == id { print $1 }')" in
		"") ;;
		sds)
			note "storage '$STOREID' (type sds) already present; left as is"
			note "  (its controller list is fixed: edit /etc/pve/storage.cfg to change it)"
			return 0
			;;
		*) die "a storage named '$STOREID' already exists with another type; choose another with --storage-id" ;;
	esac
	controllers=$(controller_list)
	run_on "$LOCAL_NODE" "pvesm add sds $(q "$STOREID") --controller $(q "$controllers") --sdspool $(q "$POOL") --replicas $(q "$REPLICAS") --storagetype $(q "$(storage_type)") --content images,rootdir --shared 1"
}
