"""Cinder volume driver for Haify.

Volumes are Haify resources: DRBD replicated across the storage nodes, and
used by a compute host as the block device /dev/drbd/by-res/<resource>/0, from
its own replica or, without one, as a DRBD diskless client over the network.
No iSCSI target sits in the data path.

cinder.conf:

    [DEFAULT]
    enabled_backends = haify

    [haify]
    volume_driver = haify_cinder.driver.HaifyDriver
    volume_backend_name = haify
    haify_controller = 192.168.1.10,192.168.1.11

The host running cinder-volume must have DRBD 9 and drbd-utils installed and
be reachable by the controller like a storage node: it attaches a volume to
copy an image into it or out of it. Every compute host needs the same, and
should run Haify's libvirt hook (deploy/libvirt), which makes a guest's disks
Primary again when Nova starts it after the host rebooted.
"""

import functools

from oslo_config import cfg
from oslo_log import log as logging

from cinder import exception
from cinder import interface
from cinder.volume import configuration
from cinder.volume import driver

from haify_cinder import backend as haify_backend
from haify_cinder import client as haify_client

LOG = logging.getLogger(__name__)

haify_opts = [
    cfg.ListOpt("haify_controller", default=[],
                help="haify-controller REST addresses, host or host:port (port 3375), or http(s):// URLs. "
                     "List every node that can run the controller under Self-HA."),
    cfg.StrOpt("haify_token_file", default="",
               help="File holding a bearer token, when the controller has [auth] or [rbac]."),
    cfg.StrOpt("haify_pool", default="",
               help="Haify storage pool for new volumes; empty lets the controller choose."),
    cfg.StrOpt("haify_storage_type", default="", choices=["", "lvm", "lvm-thin", "zfs"],
               help="Backing type of new volumes; empty is the controller's default_pool_type."),
    cfg.IntOpt("haify_replicas", default=2, min=1,
               help="Replicas of each volume, placed by the controller. Ignored with haify_nodes."),
    cfg.ListOpt("haify_nodes", default=[],
                help="Put every volume on exactly these Haify nodes instead of letting the controller place it."),
    cfg.StrOpt("haify_resource_prefix", default="cinder-",
               help="Prefix of the Haify resource name of a volume, followed by the volume's ID."),
    cfg.DictOpt("haify_node_map", default={},
                help="Host name (as Nova and Cinder name it) to Haify node name, where the two differ: "
                     "compute1:node-a,compute2:node-b. Unlisted hosts are their short host name."),
    cfg.StrOpt("haify_on_no_quorum", default="suspend-io", choices=["suspend-io", "io-error"],
               help="What a guest's I/O does while its host has lost quorum: wait for it (suspend-io), or "
                    "fail (io-error, after which a guest usually needs a reboot)."),
    cfg.IntOpt("haify_copy_timeout", default=3600, min=60,
               help="Seconds to wait for calls that copy or resync data: create from a snapshot, clone, "
                    "extend, revert, delete."),
]

CONF = cfg.CONF
CONF.register_opts(haify_opts, group=configuration.SHARED_CONF_GROUP)


def translated(fn):
    """Reports Haify's refusals as the backend error Cinder shows."""

    @functools.wraps(fn)
    def wrapper(*args, **kwargs):
        try:
            return fn(*args, **kwargs)
        except haify_client.HaifyError as e:
            LOG.error("haify: %s failed: %s", fn.__name__, e)
            raise exception.VolumeBackendAPIException(data=str(e))

    return wrapper


def attachment_hosts(volume, skip_id=None):
    """The hosts a volume's attachments are on, without the one skip_id names."""
    hosts = []
    for a in getattr(volume, "volume_attachment", None) or []:
        if skip_id and a.id == skip_id:
            continue
        host = a.attached_host or (a.connector or {}).get("host")
        if host:
            hosts.append(host)
    return hosts


@interface.volumedriver
class HaifyDriver(driver.VolumeDriver):
    """Haify (DRBD) volumes, attached as local block devices."""

    VERSION = "1.0.0"
    CI_WIKI_NAME = None

    def __init__(self, *args, **kwargs):
        super().__init__(*args, **kwargs)
        self.configuration.append_config_values(haify_opts)
        self.backend = None

    @staticmethod
    def get_driver_options():
        return haify_opts

    def _log(self, level, msg):
        getattr(LOG, level)("haify: %s", msg)

    def do_setup(self, context):
        c = self.configuration
        token = None
        if c.haify_token_file:
            with open(c.haify_token_file) as f:
                token = f.read().strip() or None
        settings = haify_backend.Settings(
            controller=c.haify_controller, token=token, pool=c.haify_pool,
            storage_type=c.haify_storage_type, replicas=c.haify_replicas, nodes=c.haify_nodes,
            prefix=c.haify_resource_prefix, node_map=c.haify_node_map,
            on_no_quorum=c.haify_on_no_quorum, copy_timeout=c.haify_copy_timeout)
        self.backend = haify_backend.Backend(haify_client.Client(c.haify_controller, token=token), settings,
                                             log=self._log)

    @translated
    def check_for_setup_error(self):
        if not self.configuration.haify_controller:
            raise exception.InvalidConfigurationValue(option="haify_controller", value="")
        self.backend.client.pools()

    # Volumes

    @translated
    def create_volume(self, volume):
        self.backend.create(volume.id, volume.size)

    @translated
    def delete_volume(self, volume):
        self.backend.delete(volume.id)

    @translated
    def extend_volume(self, volume, new_size):
        self.backend.extend(volume.id, new_size)

    @translated
    def create_volume_from_snapshot(self, volume, snapshot):
        self.backend.create_from_snapshot(volume.id, volume.size, snapshot.volume_id, snapshot.id)

    @translated
    def create_cloned_volume(self, volume, src_vref):
        self.backend.clone(volume.id, volume.size, src_vref.id)

    # Snapshots

    @translated
    def create_snapshot(self, snapshot):
        self.backend.create_snapshot(snapshot.volume_id, snapshot.id)

    @translated
    def delete_snapshot(self, snapshot):
        self.backend.delete_snapshot(snapshot.volume_id, snapshot.id)

    @translated
    def revert_to_snapshot(self, context, volume, snapshot):
        self.backend.revert(volume.id, snapshot.id)

    # Attach and detach

    @translated
    def initialize_connection(self, volume, connector, **kwargs):
        return self.backend.connect(volume.id, connector.get("host"), attachment_hosts(volume))

    @translated
    def terminate_connection(self, volume, connector, **kwargs):
        if connector is None:
            # A force detach of every attachment: the hosts are unknown, and
            # DRBD refuses to demote a device a guest still has open anyway.
            LOG.warning("haify: force detach of %s; its roles are left as they are", volume.id)
            return
        attachment = kwargs.get("attachment")
        others = attachment_hosts(volume, skip_id=attachment.id if attachment else None)
        self.backend.disconnect(volume.id, connector.get("host"), others)

    def create_export(self, context, volume, connector):
        pass

    def ensure_export(self, context, volume):
        pass

    def remove_export(self, context, volume):
        pass

    # Capacity

    def _update_volume_stats(self):
        c = self.configuration
        stats = {
            "volume_backend_name": c.safe_get("volume_backend_name") or "haify",
            "vendor_name": "Haify",
            "driver_version": self.VERSION,
            "storage_protocol": "DRBD",
            "total_capacity_gb": "unknown",
            "free_capacity_gb": "unknown",
            "reserved_percentage": c.safe_get("reserved_percentage") or 0,
            "max_over_subscription_ratio": c.safe_get("max_over_subscription_ratio") or 20.0,
            "thick_provisioning_support": True,
            "thin_provisioning_support": False,
            "QoS_support": False,
            "multiattach": False,
            "online_extend_support": False,
            "backend_state": "up",
        }
        try:
            cap = self.backend.stats()
            stats["total_capacity_gb"] = cap["total_capacity_gb"]
            stats["free_capacity_gb"] = cap["free_capacity_gb"]
            stats["thin_provisioning_support"] = cap["thin"]
            stats["thick_provisioning_support"] = not cap["thin"]
        except haify_client.HaifyError as e:
            LOG.warning("haify: cannot read pool capacity: %s", e)
            stats["backend_state"] = "down"
        self._stats = stats
