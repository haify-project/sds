#!/bin/sh
# Loads sds-storage.js into the Proxmox VE web interface, or with --remove
# stops loading it.
#
# PVE gives storage plugins no way into its interface, so this adds one script
# tag to pve-manager's page template, right after pvemanagerlib.js. A
# pve-manager upgrade replaces the template; the apt hook installed alongside
# runs this again afterwards. Running it twice changes nothing.
set -e

TPL=${SDS_PVE_INDEX_TPL:-/usr/share/pve-manager/index.html.tpl}
MARK='<!-- sds-storage -->'
TAG='<script type="text/javascript" src="/pve2/js/sds-storage.js?ver=[% version %]"></script>'

[ -f "$TPL" ] || exit 0

if [ "${1:-}" = "--remove" ]; then
    if grep -qF "$MARK" "$TPL"; then
        sed -i "\|$MARK|d" "$TPL"
    fi
    exit 0
fi

grep -qF "$MARK" "$TPL" && exit 0
grep -q 'pvemanagerlib.js' "$TPL" || { echo "gui-patch.sh: no pvemanagerlib.js in $TPL; SDS is not added to the web interface" >&2; exit 0; }
# The marker sits on the same line, so --remove takes exactly what this added.
sed -i "\|pvemanagerlib.js|a\\    $TAG $MARK" "$TPL"
