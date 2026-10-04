#!/bin/bash
# Keep this host's LVM off the devices that carry guest disks.
#
# A guest that uses LVM inside its disk writes a PV header at the start of it,
# and on this host that disk is /dev/drbdN — or, for an encrypted resource,
# also the dm-crypt container DRBD sits on (/dev/mapper/sds_*). The host's LVM
# scans both, finds the guest's volume group, and may activate its LVs: then
# the guest's LVs are open on the host, the DRBD device cannot be demoted, and
# a live migration or failover of that VM fails. Two guests with the same VG
# name also make the host's own LVM commands report duplicates.
#
# The backing LVs themselves are safe: LVM does not scan LVs for PVs
# (devices/scan_lvs = 0, the default since lvm2 2.03), which --check confirms.
#
#   ./lvm-filter.sh          add the filter to /etc/lvm/lvm.conf if it is missing
#   ./lvm-filter.sh --check  exit 1 if it is missing (run by preflight.sh)
#
# The entries are prepended to an existing global_filter, so PVE's own (which
# keeps LVM off ZFS zvols and Ceph RBDs) is kept; a backup of lvm.conf is left
# next to it. A global_filter written across several lines is not edited: the
# script says what to add and exits 1.

set -euo pipefail

CONF="${LVM_SYSTEM_DIR:-/etc/lvm}/lvm.conf"
WANT=('r|^/dev/drbd|' 'r|^/dev/mapper/sds_|')

# has_filter reports whether LVM's effective global_filter rejects both.
has_filter() {
    local current
    current=$(lvmconfig devices/global_filter 2>/dev/null) || return 1
    for entry in "${WANT[@]}"; do
        case "$current" in
            *"\"$entry\""*) ;;
            *) return 1 ;;
        esac
    done
}

if ! command -v lvmconfig >/dev/null 2>&1; then
    echo "lvmconfig not found: is lvm2 installed?" >&2
    exit 1
fi

if [ "${1:-}" = "--check" ]; then
    rc=0
    if has_filter; then
        echo "LVM global_filter keeps LVM off DRBD devices"
    else
        echo "LVM global_filter does not reject ${WANT[*]} — run lvm-filter.sh (install.sh does)" >&2
        rc=1
    fi
    scan=$(lvmconfig --typeconfig full devices/scan_lvs 2>/dev/null || echo unknown)
    scan="${scan#*=}"
    if [ "$scan" != "0" ]; then
        echo "LVM devices/scan_lvs is ${scan}: LVM scans backing LVs too, and finds guest volume groups in them; set it to 0" >&2
        rc=1
    fi
    exit "$rc"
fi

if has_filter; then
    echo "LVM global_filter already keeps LVM off DRBD devices"
    exit 0
fi

if [ "$(id -u)" -ne 0 ]; then
    echo "lvm-filter.sh must run as root to edit $CONF" >&2
    exit 1
fi

cp -p "$CONF" "$CONF.sds-bak"

# The edit is done in Perl, which every PVE node has: prepend to a one-line
# global_filter, or add one at the top of the devices section (or a new one).
if ! WANT_LIST="$(printf '"%s"\n' "${WANT[@]}")" perl -0777 -pi -e '
    my $list = join(", ", split /\n/, $ENV{WANT_LIST});
    if (/^[ \t]*global_filter[ \t]*=/m) {
        s{^([ \t]*global_filter[ \t]*=[ \t]*\[)[ \t]*([^\n]*\])}{
            my ($head, $rest) = ($1, $2);
            $head . $list . ($rest =~ /^\]/ ? " " : ", ") . $rest
        }me or exit 3;
    } elsif (!s/^([ \t]*devices[ \t]*\{[^\n]*\n)/$1\tglobal_filter = [ $list ]\n/m) {
        $_ .= "\ndevices {\n\tglobal_filter = [ $list ]\n}\n";
    }
' "$CONF"; then
    cp -p "$CONF.sds-bak" "$CONF"
    echo "$CONF has a global_filter spanning several lines; add these entries at the front of it by hand:" >&2
    printf '  "%s",\n' "${WANT[@]}" >&2
    exit 1
fi

if ! lvmconfig --validate >/dev/null 2>&1 || ! has_filter; then
    cp -p "$CONF.sds-bak" "$CONF"
    echo "the edited $CONF did not validate; it was restored from $CONF.sds-bak" >&2
    exit 1
fi
echo "Added ${WANT[*]} to the LVM global_filter in $CONF (backup: $CONF.sds-bak)"
