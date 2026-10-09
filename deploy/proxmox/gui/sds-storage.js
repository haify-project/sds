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

        return me.callParent([values]);
    },

    initComponent: function () {
        let me = this;

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
                xtype: 'proxmoxtextfield',
                name: 'sdspool',
                fieldLabel: gettext('Pool'),
                emptyText: gettext('Controller default'),
                deleteEmpty: !me.isCreate,
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
