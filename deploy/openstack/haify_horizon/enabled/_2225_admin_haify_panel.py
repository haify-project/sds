# Copy into openstack_dashboard/local/enabled/ (see deploy/openstack/README.md).
PANEL = "haify"
PANEL_DASHBOARD = "admin"
PANEL_GROUP = "volume"
ADD_PANEL = "haify_horizon.panel.Haify"
ADD_INSTALLED_APPS = ["haify_horizon"]
# A Replication tab on the admin volume details of a Haify volume.
EXTRA_TABS = {
    "openstack_dashboard.dashboards.admin.volumes.tabs.VolumeDetailTabs": (
        (10, "haify_horizon.tabs.ReplicationTab"),
    ),
}
