// The sds storage type in the Proxmox VE web interface: Datacenter → Storage →
// Add → SDS, and the same dialog to edit an existing sds storage.
//
// PVE builds its storage dialogs from PVE.Utils.storageSchema, a table its
// own code fills in; a storage plugin has no way to add itself. This file is
// loaded right after pvemanagerlib.js (gui-patch.sh adds the script tag) and
// registers the type the way PVE registers its built-in ones. Without it the
// storage still works, but cannot be added from the interface, and opening an
// existing one fails with "no editor registered for storage type: sds".
//
// The fields mirror the plugin's properties (SDSPlugin.pm); a change there
// belongs here too.

Ext.define('PVE.storage.SDSInputPanel', {
    extend: 'PVE.panel.StorageBase',

    // The PVE dialogs read this to pick an online help section; sds has none
    // in the PVE manual.
    onlineHelp: '',

    onGetValues: function (values) {
        let me = this;

        // Every node reaches the same volumes over DRBD.
        if (me.isCreate) {
            values.shared = 1;
            // An option left empty is one not set, rather than an empty value
            // written into storage.cfg.
            for (const key of ['sdspool', 'sdsnodes', 'resourceprefix', 'controllerca', 'apitoken']) {
                if (values[key] === '') {
                    delete values[key];
                }
            }
        }
        // The token is kept in /etc/pve/priv; an empty field while editing
        // leaves it as it is.
        if (!me.isCreate && !values.apitoken) {
            delete values.apitoken;
        }
        // The pool list is a plain combo box, so clearing it while editing has
        // to be said as a deletion.
        if (!me.isCreate && values.sdspool === '') {
            delete values.sdspool;
            values.delete = [].concat(values.delete || [], 'sdspool');
        }
        // Exact size is the plugin's default, so it is written only when
        // turned off.
        if (String(values.exactsize) === '1') {
            delete values.exactsize;
            if (!me.isCreate) {
                values.delete = [].concat(values.delete || [], 'exactsize');
            }
        }

        return me.callParent([values]);
    },

    initComponent: function () {
        let me = this;

        // The pools SDS made on this node are its sds_* volume groups, which
        // PVE's own LVM scan lists; on a node that holds no replica the list is
        // empty and the name can still be typed.
        me.poolStore = Ext.create('Ext.data.Store', { fields: ['pool', 'text'], data: [] });
        Proxmox.Utils.API2Request({
            url: `/nodes/${Proxmox.NodeName}/scan/lvm`,
            method: 'GET',
            success: (response) => {
                let pools = (response.result.data || [])
                    .filter((vg) => vg.vg.startsWith('sds_'))
                    .map((vg) => ({
                        pool: vg.vg.replace(/^sds_/, ''),
                        text: `${vg.vg.replace(/^sds_/, '')} (${Proxmox.Utils.format_size(vg.size)})`,
                    }));
                me.poolStore.loadData(pools);
            },
        });

        // Bootstrap puts a controller on every cluster node, so a new storage
        // starts with all of them listed.
        if (me.isCreate) {
            Proxmox.Utils.API2Request({
                url: '/cluster/status',
                method: 'GET',
                success: (response) => {
                    let ips = (response.result.data || [])
                        .filter((entry) => entry.type === 'node' && entry.ip)
                        .map((entry) => entry.ip);
                    let field = me.down('field[name=controller]');
                    if (field && !field.getValue() && ips.length) {
                        field.setValue(ips.join(','));
                    }
                },
            });
        }

        me.column1 = [
            {
                xtype: 'pmxDisplayEditField',
                name: 'controller',
                fieldLabel: gettext('Controller'),
                editable: me.isCreate,
                allowBlank: false,
                // Every node that can run the controller: host or host:port,
                // optionally https://. Fixed once the storage exists.
                emptyText: '10.0.0.11,10.0.0.12,10.0.0.13',
            },
            {
                xtype: 'combobox',
                name: 'sdspool',
                fieldLabel: gettext('Pool'),
                emptyText: gettext('Controller default'),
                editable: true,
                queryMode: 'local',
                displayField: 'text',
                valueField: 'pool',
                store: me.poolStore,
            },
            {
                xtype: 'pveContentTypeSelector',
                cts: ['images', 'rootdir'],
                fieldLabel: gettext('Content'),
                name: 'content',
                value: ['images', 'rootdir'],
                multiSelect: true,
                allowBlank: false,
            },
        ];

        me.column2 = [
            {
                xtype: 'proxmoxintegerfield',
                name: 'replicas',
                fieldLabel: gettext('Replicas'),
                minValue: 1,
                maxValue: 16,
                emptyText: '2',
                deleteEmpty: !me.isCreate,
            },
            {
                xtype: 'proxmoxtextfield',
                name: 'sdsnodes',
                fieldLabel: gettext('Replica nodes'),
                emptyText: gettext('Placed by free space'),
                deleteEmpty: !me.isCreate,
            },
        ];

        me.advancedColumn1 = [
            {
                xtype: 'proxmoxKVComboBox',
                name: 'storagetype',
                fieldLabel: gettext('Backing'),
                value: '__default__',
                deleteEmpty: !me.isCreate,
                comboItems: [
                    ['__default__', gettext('Controller default')],
                    ['lvm-thin', 'LVM-Thin'],
                    ['lvm', 'LVM'],
                    ['zfs', 'ZFS'],
                ],
            },
            {
                xtype: 'proxmoxtextfield',
                name: 'resourceprefix',
                fieldLabel: gettext('Resource prefix'),
                emptyText: 'pve',
                deleteEmpty: !me.isCreate,
            },
            {
                xtype: 'proxmoxtextfield',
                inputType: 'password',
                name: 'apitoken',
                fieldLabel: gettext('API token'),
                emptyText: me.isCreate ? gettext('None') : gettext('Unchanged'),
            },
        ];

        me.advancedColumn2 = [
            {
                xtype: 'proxmoxKVComboBox',
                name: 'onnoquorum',
                fieldLabel: gettext('Quorum lost'),
                value: '__default__',
                deleteEmpty: !me.isCreate,
                comboItems: [
                    ['__default__', Proxmox.Utils.defaultText + ' (' + gettext('pause I/O') + ')'],
                    ['suspend-io', gettext('Pause I/O')],
                    ['io-error', gettext('Fail I/O')],
                ],
            },
            {
                xtype: 'proxmoxtextfield',
                name: 'controllerca',
                fieldLabel: gettext('CA file'),
                emptyText: gettext('System trust store'),
                deleteEmpty: !me.isCreate,
            },
            {
                xtype: 'proxmoxcheckbox',
                name: 'exactsize',
                fieldLabel: gettext('Exact disk size'),
                checked: true,
                uncheckedValue: 0,
            },
        ];

        me.callParent();
    },
});

PVE.Utils.storageSchema.sds = {
    name: 'SDS',
    ipanel: 'SDSInputPanel',
    faIcon: 'database',
    backups: false,
};

// An sds storage holds no backups, so its dialog has no Backup Retention tab
// to show; PVE adds one to every storage. The panel stays (the dialog's load
// fills it), only the tab bar goes.
Ext.define('PVE.sds.BaseEditOverride', {
    override: 'PVE.storage.BaseEdit',
    initComponent: function () {
        let me = this;
        me.callParent(arguments);
        if (me.type === 'sds') {
            me.down('tabpanel').getTabBar().hide();
        }
    },
});

// --------------------------------------------------------------------------
// Where each disk's replicas are. The plugin adds sds-* fields to the volumes
// PVE's storage content API lists (SDS/Inventory.pm), read from the DRBD view
// of the node the API call runs on.
// --------------------------------------------------------------------------

PVE.sds = Ext.apply(PVE.sds || {}, {
    // stateHtml renders a disk's sds-state as an icon and a few words.
    stateHtml: function (state, detail) {
        let map = {
            ok: ['fa-check-circle good', gettext('In step')],
            syncing: ['fa-refresh fa-spin warning', detail || gettext('Syncing')],
            degraded: ['fa-exclamation-triangle critical', detail || gettext('Degraded')],
        };
        let [cls, text] = map[state] || ['fa-question-circle faded', gettext('Not up on this node')];
        return `<i class="fa fa-fw ${cls}"></i> ${Ext.htmlEncode(text)}`;
    },

    // replicasHtml renders one chip per node holding a replica, coloured by its
    // disk state, with the node running the guest marked.
    replicasHtml: function (rec) {
        let disks = {};
        (rec['sds-disks'] || '').split(',').filter(Boolean).forEach((pair) => {
            let [node, state] = pair.split(':');
            disks[node] = state;
        });
        let primary = rec['sds-primary'];
        let chips = (rec['sds-replicas'] || '').split(',').filter(Boolean).map((node) => {
            let state = disks[node] || 'unknown';
            let cls = state === 'UpToDate' ? 'good' : state === 'unknown' ? 'faded' : 'critical';
            let running = node === primary ? ` <i class="fa fa-play" title="${gettext('Running here')}"></i>` : '';
            return `<span style="white-space:nowrap;margin-right:10px" title="${Ext.htmlEncode(state)}">` +
                `<i class="fa fa-circle ${cls}"></i> ${Ext.htmlEncode(node)}${running}</span>`;
        });
        let diskless = rec['sds-diskless']
            ? `<span class="faded" title="${gettext('Reach the disk over the network')}">` +
              `${gettext('diskless')}: ${Ext.htmlEncode(rec['sds-diskless'].replace(/,/g, ', '))}</span>`
            : '';
        return chips.join('') + diskless;
    },

    // loadDisks lists every disk on every sds storage, as seen from this node.
    loadDisks: function (callback) {
        Proxmox.Utils.API2Request({
            url: '/storage',
            method: 'GET',
            params: { type: 'sds' },
            success: (response) => {
                let ids = (response.result.data || []).map((s) => s.storage);
                let all = [];
                let pending = ids.length;
                if (!pending) {
                    callback(all);
                    return;
                }
                // API2Request runs its own `callback` option before `success`,
                // so the count is kept in success and failure instead.
                let finish = () => { if (--pending === 0) { callback(all); } };
                ids.forEach((id) => Proxmox.Utils.API2Request({
                    url: `/nodes/${Proxmox.NodeName}/storage/${id}/content`,
                    method: 'GET',
                    params: { content: 'images' },
                    success: (r) => { all = all.concat(r.result.data || []); finish(); },
                    failure: finish,
                }));
            },
        });
    },
});

Ext.define('PVE.sds.DisksView', {
    extend: 'Ext.grid.Panel',
    alias: 'widget.pveSDSDisksView',

    initComponent: function () {
        let me = this;
        let nodename = me.pveSelNode.data.node;
        let storeid = me.pveSelNode.data.storage;

        me.store = Ext.create('Ext.data.Store', {
            fields: ['volid', 'vmid', 'size'],
            sorters: [{ property: 'volid', direction: 'ASC' }],
            proxy: {
                type: 'proxmox',
                url: `/api2/json/nodes/${nodename}/storage/${storeid}/content`,
                extraParams: { content: 'images' },
            },
        });
        let reload = () => me.store.load();

        Ext.apply(me, {
            emptyText: gettext('No disks'),
            tbar: [
                { text: gettext('Reload'), iconCls: 'fa fa-refresh', handler: reload },
                '->',
                {
                    xtype: 'displayfield',
                    value: Ext.String.format(gettext("Live state as node '{0}' sees it"), nodename),
                },
            ],
            columns: [
                {
                    header: gettext('Disk'),
                    dataIndex: 'volid',
                    flex: 1.2,
                    renderer: (v) => Ext.htmlEncode(v.replace(/^[^:]+:/, '')),
                },
                { header: 'VM', dataIndex: 'vmid', width: 70 },
                { header: gettext('Size'), dataIndex: 'size', width: 90, renderer: Proxmox.Utils.format_size },
                {
                    header: gettext('Replicas'),
                    flex: 2,
                    renderer: (v, md, rec) => PVE.sds.replicasHtml(rec.data),
                },
                {
                    header: gettext('Runs on'),
                    width: 100,
                    renderer: (v, md, rec) => Ext.htmlEncode(rec.data['sds-primary'] || '-'),
                },
                {
                    header: gettext('Replication'),
                    flex: 1.2,
                    renderer: (v, md, rec) => PVE.sds.stateHtml(rec.data['sds-state'], rec.data['sds-detail']),
                },
            ],
            listeners: {
                activate: () => {
                    reload();
                    me.timer = setInterval(reload, 10000);
                },
                deactivate: () => clearInterval(me.timer),
                destroy: () => clearInterval(me.timer),
            },
        });
        me.callParent();
    },
});

// The SDS tab on an sds storage's page (Datacenter tree → node → storage).
// PVE.storage.Browser builds its tab list and hands it to PVE.panel.Config,
// so the tab is added there, for sds storages only.
Ext.define('PVE.sds.StorageBrowserOverride', {
    override: 'PVE.panel.Config',
    initComponent: function () {
        let me = this;
        if (me instanceof PVE.storage.Browser) {
            let data = me.pveSelNode.data;
            let rec = PVE.data.ResourceStore.findRecord('id', `storage/${data.node}/${data.storage}`, 0, false, true, true);
            if (rec && rec.data.plugintype === 'sds') {
                me.items.push({
                    xtype: 'pveSDSDisksView',
                    title: 'SDS',
                    iconCls: 'fa fa-database',
                    itemId: 'sdsReplicas',
                    pveSelNode: me.pveSelNode,
                });
            }
        }
        me.callParent(arguments);
    },
});

// --------------------------------------------------------------------------
// HA: which nodes hold a replica of each guest's disks, and node affinity rules
// that send the guest there first.
// --------------------------------------------------------------------------

// replicasByGuest maps a VM id to the nodes holding a replica of its disks: the
// nodes every data disk has a replica on (cloud-init and state volumes do not
// count), or, when they share none, those of its first disk.
PVE.sds.replicasByGuest = function (disks) {
    let byVm = {};
    disks.forEach((d) => {
        if (!d.vmid || !/-disk-\d+$/.test(d.volid)) {
            return;
        }
        let replicas = (d['sds-replicas'] || '').split(',').filter(Boolean);
        let cur = byVm[d.vmid];
        byVm[d.vmid] = cur ? cur.filter((n) => replicas.includes(n)) : replicas;
        if (!byVm[d.vmid].length) {
            byVm[d.vmid] = cur && cur.length ? cur : replicas;
        }
    });
    return byVm;
};

Ext.define('PVE.sds.HAResourcesOverride', {
    override: 'PVE.ha.ResourcesView',
    initComponent: function () {
        let me = this;
        me.callParent(arguments);
        me.sdsReplicas = {};

        // The HA store reloads every few seconds; the disks are read at most
        // every 15.
        let last = 0;
        let refresh = () => {
            if (Date.now() - last < 15000) {
                return;
            }
            last = Date.now();
            PVE.sds.loadDisks((disks) => {
                me.sdsReplicas = PVE.sds.replicasByGuest(disks);
                if (me.getView()) {
                    me.getView().refresh();
                }
            });
        };

        me.headerCt.insert(3, Ext.create('Ext.grid.column.Column', {
            header: gettext('SDS replicas'),
            width: 170,
            sortable: false,
            renderer: (v, md, rec) => {
                let m = (rec.data.sid || '').match(/^vm:(\d+)$/);
                let replicas = m ? me.sdsReplicas[m[1]] : undefined;
                if (!replicas) {
                    return '';
                }
                let html = Ext.htmlEncode(replicas.join(', '));
                if (rec.data.node && !replicas.includes(rec.data.node)) {
                    html = `<i class="fa fa-exclamation-triangle warning" title="${gettext('Running on a node without a replica: its disks are read over the network')}"></i> ` + html;
                }
                return html;
            },
        }));

        // Node affinity rules are PVE 9's; PVE 8 has HA groups instead.
        if (PVE.ha.NodeAffinityRulesView) {
            me.down('toolbar').add('-', {
                text: gettext('Prefer nodes with SDS replicas'),
                iconCls: 'fa fa-database',
                handler: () => PVE.sds.preferReplicas(me.sdsReplicas, () => me.rstore.load()),
            });
        }

        refresh();
        me.rstore.on('load', refresh);
    },
});

// preferReplicas writes one non-strict node affinity rule per HA-managed VM with
// disks on sds, named sds-vm-<id>: the nodes holding its replicas come first,
// any other node is still allowed when none of them is up.
PVE.sds.preferReplicas = function (replicasByVm, done) {
    Proxmox.Utils.API2Request({
        url: '/cluster/ha/resources',
        method: 'GET',
        success: (response) => {
            let targets = (response.result.data || [])
                .map((r) => (r.sid || '').match(/^vm:(\d+)$/))
                .filter((m) => m && replicasByVm[m[1]] && replicasByVm[m[1]].length)
                .map((m) => ({ vmid: m[1], nodes: replicasByVm[m[1]] }));
            if (!targets.length) {
                Ext.Msg.alert(gettext('SDS'), gettext('No HA-managed VM has disks on an SDS storage.'));
                return;
            }
            let list = targets.map((t) => `VM ${t.vmid}: ${t.nodes.join(', ')}`).join('<br>');
            Ext.Msg.confirm(
                gettext('Prefer nodes with SDS replicas'),
                gettext('HA keeps each VM on a node that holds a replica of its disks: a VM running on another node is migrated now, and after a failure it starts on one of them, or on any node when none is up.') +
                    `<br><br>${list}`,
                (btn) => {
                    if (btn !== 'yes') {
                        return;
                    }
                    let pending = targets.length;
                    targets.forEach((t) => {
                        let params = {
                            type: 'node-affinity',
                            resources: `vm:${t.vmid}`,
                            nodes: t.nodes.join(','),
                            strict: 0,
                            comment: 'Nodes holding an SDS replica of the disks',
                        };
                        let finish = () => { if (--pending === 0) { done(); } };
                        Proxmox.Utils.API2Request({
                            url: '/cluster/ha/rules',
                            method: 'POST',
                            params: Ext.apply({ rule: `sds-vm-${t.vmid}` }, params),
                            success: finish,
                            failure: () => Proxmox.Utils.API2Request({
                                // It exists already: bring it up to date.
                                url: `/cluster/ha/rules/sds-vm-${t.vmid}`,
                                method: 'PUT',
                                params: params,
                                success: finish,
                                failure: (r) => { Ext.Msg.alert(gettext('Error'), r.htmlStatus); finish(); },
                            }),
                        });
                    });
                },
            );
        },
    });
};
