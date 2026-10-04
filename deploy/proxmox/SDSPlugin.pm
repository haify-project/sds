package PVE::Storage::Custom::SDSPlugin;

# SDS storage plugin for Proxmox VE.
#
# Backs VM/CT disks with DRBD resources managed by sds-controller, so guests get
# synchronous replication, HA restart on a surviving node, and RAM-only live
# migration (the disk is already on every node, so nothing is copied).
#
# The plugin holds NO storage logic of its own: every operation is a REST call
# to sds-controller (SDS/Client.pm), using only Perl modules PVE already ships,
# so installing needs no extra packages and no sds binaries on the PVE nodes.
#
# See docs/design/proxmox-storage-plugin.md.

use strict;
use warnings;

use Cwd ();
use PVE::INotify;
use PVE::Storage::Plugin;
use PVE::Storage::Custom::SDS::Client qw(_uri_escape);
use PVE::Storage::Custom::SDS::Capacity qw(pool_capacity);
use PVE::Storage::Custom::SDS::Naming qw(sds_resource_name volname_from_resource
    parse_vm_volname kib_to_gb bytes_to_gb gb_to_bytes);
use PVE::Storage::Custom::SDS::Activation qw(activate deactivate controller_unreachable local_device_path);

use base qw(PVE::Storage::Plugin);

our $VERSION = '0.1.0';

# The storage API version this plugin was written against.
my $PLUGIN_APIVER = 11;

# How long to wait for /dev/drbdN to appear after a promote, and how often to
# look. A promote returns once DRBD accepted the role change, but udev needs a
# moment to publish the node, and qemu opens the path immediately afterwards.
our $DEVICE_WAIT_SECONDS  = 20;
our $DEVICE_POLL_INTERVAL = 0.2;

# Test seams: unit tests install a factory returning a mocked REST client, and
# replace the block-device probe, so the plugin's logic can be exercised without
# a controller or a real DRBD device.
our $CLIENT_FACTORY;
our $DEVICE_CHECK = sub { return -b $_[0] };

# ---------------------------------------------------------------------------
# Plugin registration
# ---------------------------------------------------------------------------

sub api {
    # Fully qualified with parens: PVE::Storage loads this plugin, so the
    # constants exist at call time without us use'ing it (which would be a
    # circular dependency).
    my $apiver = PVE::Storage::APIVER();
    my $apiage = PVE::Storage::APIAGE();
    my $oldest = $apiver - $apiage;

    # Inside this PVE's compatibility window: declare what we were built for.
    return $PLUGIN_APIVER if $PLUGIN_APIVER >= $oldest && $PLUGIN_APIVER <= $apiver;

    # Outside it: claim the nearest version this PVE still accepts so the plugin
    # loads and any real incompatibility surfaces as a concrete method error
    # rather than the storage silently disappearing from the UI.
    return $PLUGIN_APIVER < $oldest ? $oldest : $apiver;
}

sub type { return 'sds'; }

sub plugindata {
    return {
        # Block devices: VM disks and container rootdirs.
        content => [ { images => 1, rootdir => 1 }, { images => 1 } ],
        # DRBD exports a raw block device. qcow2-on-DRBD is not supported and
        # not needed: snapshots come from sds (LVM/ZFS), not the image format.
        format  => [ { raw => 1 }, 'raw' ],
        'sensitive-properties' => { apitoken => 1 },
    };
}

sub properties {
    return {
        controller => {
            description => "sds-controller REST address(es), comma-separated, each host or host:port (port defaults to 3375), optionally with https://. List every node that can run the controller under Self-HA.",
            type        => 'string',
        },
        controllerca => {
            description => "PEM CA bundle that signs the controller's certificate, for https:// addresses. Unset: the system trust store.",
            type        => 'string',
        },
        sdspool => {
            description => "sds storage pool (volume group) backing new volumes.",
            type        => 'string',
        },
        sdsnodes => {
            description => "Comma-separated sds nodes to place replicas on. When unset, sds auto-places by free space.",
            type        => 'string',
        },
        replicas => {
            description => "Replica count for auto-placement (ignored when sdsnodes is set).",
            type        => 'integer',
            minimum     => 1,
            maximum     => 16,
        },
        storagetype => {
            description => "sds backing store for new volumes: lvm, lvm-thin or zfs.",
            type        => 'string',
            enum        => [ 'lvm', 'lvm-thin', 'zfs' ],
        },
        resourceprefix => {
            description => "Prefix for generated sds resource names. Give each PVE cluster its own prefix when several share one sds cluster.",
            type        => 'string',
        },
        apitoken => {
            description => "Bearer token for sds when [auth]/[rbac] is enabled.",
            type        => 'string',
        },
        onnoquorum => {
            description => "What a new volume does when its node loses quorum or every UpToDate copy: suspend-io (default) freezes the guest's I/O until it is back, io-error fails it.",
            type        => 'string',
            enum        => [ 'suspend-io', 'io-error' ],
        },
    };
}

sub options {
    return {
        controller     => { fixed    => 1 },
        sdspool        => { optional => 1 },
        sdsnodes       => { optional => 1 },
        replicas       => { optional => 1 },
        storagetype    => { optional => 1 },
        resourceprefix => { optional => 1 },
        apitoken       => { optional => 1 },
        controllerca   => { optional => 1 },
        onnoquorum     => { optional => 1 },
        nodes          => { optional => 1 },
        disable        => { optional => 1 },
        content        => { optional => 1 },
        shared         => { optional => 1 },
        bwlimit        => { optional => 1 },
    };
}

# ---------------------------------------------------------------------------
# sds helpers
# ---------------------------------------------------------------------------

sub _client {
    my ($class, $scfg) = @_;
    return $CLIENT_FACTORY->($scfg) if $CLIENT_FACTORY;
    return PVE::Storage::Custom::SDS::Client->new($scfg);
}

sub _nodename { return PVE::INotify::nodename(); }

sub _get_resource {
    my ($class, $scfg, $resname) = @_;
    my $client = $class->_client($scfg);
    my $res = $client->request('GET', "/v1/resources/$resname");
    return $res->{resource};
}

sub _get_status {
    my ($class, $scfg, $resname) = @_;
    my $client = $class->_client($scfg);
    my $res = $client->request('GET', "/v1/resources/$resname/status");
    return $res->{status};
}

# The volume's "<pool>/<lv>" path and the storage node to operate on.
#
# Snapshots are taken on the BACKING logical volume, which only exists on nodes
# holding a replica. The PVE host usually holds none (it attaches diskless), so
# passing our own node name here fails with "failed to create snapshot on
# <hypervisor>" — pick a diskful node instead, preferring the current Primary.
sub _backing_volume_target {
    my ($class, $scfg, $resname) = @_;

    my $info = $class->_get_resource($scfg, $resname);
    my $vol  = ($info->{volumes} && @{ $info->{volumes} }) ? $info->{volumes}[0] : undef;
    die "resource '$resname' has no volumes\n" if !$vol;

    my $pool = $vol->{pool};
    my $lv   = $vol->{backingVolume} // $vol->{backing_volume};
    die "resource '$resname' has no backing volume recorded\n"
        if !defined($pool) || !defined($lv) || !length($pool) || !length($lv);

    my $replicas = $info->{nodes} // [];
    die "resource '$resname' has no diskful nodes to snapshot on\n" if !@$replicas;

    # Prefer the Primary: its data is the copy the guest is actually writing.
    my $node   = $replicas->[0];
    my $status = eval { $class->_get_status($scfg, $resname) };
    if ($status) {
        my $states = $status->{nodeStates} // $status->{node_states} // {};
        for my $peer (@$replicas) {
            if (lc($states->{$peer}{role} // '') eq 'primary') {
                $node = $peer;
                last;
            }
        }
    }

    return ("$pool/$lv", $node);
}

# ---------------------------------------------------------------------------
# Volume naming / paths
# ---------------------------------------------------------------------------

sub parse_volname {
    my ($class, $volname) = @_;

    # Disks, and the other per-VM volumes PVE allocates: cloud-init drives,
    # snapshot RAM state, backup fleecing images (SDS/Naming.pm).
    if (my ($vmid) = parse_vm_volname($volname)) {
        # (vtype, name, vmid, basename, basevmid, isBase, format)
        return ('images', $volname, $vmid, undef, undef, undef, 'raw');
    }

    die "unable to parse sds volume name '$volname'\n";
}

sub filesystem_path {
    my ($class, $scfg, $volname, $snapname) = @_;

    die "sds volumes cannot be addressed by snapshot path\n" if defined $snapname;

    my ($vtype, $name, $vmid) = $class->parse_volname($volname);
    my $resname = sds_resource_name($scfg, $name);
    my $info    = eval { $class->_get_resource($scfg, $resname) };
    if (my $err = $@) {
        # Without the controller, a volume that is up here is still
        # addressable: DRBD publishes it under by-res (SDS/Activation.pm).
        my $local = local_device_path($resname);
        die $err if !controller_unreachable($err) || !$DEVICE_CHECK->($local);
        my $device = Cwd::abs_path($local) // $local;
        return wantarray ? ($device, $vmid, $vtype) : $device;
    }
    my $vol     = ($info->{volumes} && @{ $info->{volumes} }) ? $info->{volumes}[0] : undef;
    my $device  = $vol ? $vol->{device} : undef;
    die "sds resource '$resname' has no device\n" if !defined($device) || !length($device);

    return wantarray ? ($device, $vmid, $vtype) : $device;
}

sub path {
    my ($class, $scfg, $volname, $storeid, $snapname) = @_;
    return $class->filesystem_path($scfg, $volname, $snapname);
}

sub create_base {
    my ($class, $storeid, $scfg, $volname) = @_;
    die "creating base images is not supported by the sds storage plugin\n";
}

sub clone_image {
    my ($class, $scfg, $storeid, $volname, $vmid, $snap) = @_;
    die "cloning images is not supported by the sds storage plugin\n";
}

# ---------------------------------------------------------------------------
# Allocation
# ---------------------------------------------------------------------------

sub alloc_image {
    my ($class, $storeid, $scfg, $vmid, $fmt, $name, $size) = @_;

    die "unsupported format '$fmt' (sds volumes are raw block devices)\n"
        if defined($fmt) && $fmt ne 'raw';

    $name //= $class->find_free_diskname($storeid, $scfg, $vmid, 'raw');

    my ($owner) = parse_vm_volname($name);
    die "illegal name '$name' - should be 'vm-$vmid-disk-<n>' or another 'vm-$vmid-<name>'\n"
        if !defined($owner) || $owner ne $vmid;

    my $resname = sds_resource_name($scfg, $name);
    my $sizegb  = kib_to_gb($size);

    # A guest whose disk errors out on lost quorum remounts its filesystems
    # read-only and needs a reboot; one whose I/O is suspended just waits, and
    # carries on when quorum returns. If another node took over meanwhile, DRBD
    # demotes this one (on-suspended-primary-outdated force-secondary, an sds
    # default) instead of letting it resume stale.
    my $onnoquorum = $scfg->{onnoquorum} // 'suspend-io';
    my $payload = {
        name        => $resname,
        sizeGb      => $sizegb,
        protocol    => 'C',
        drbdOptions => {
            'on-no-quorum'          => $onnoquorum,
            'on-no-data-accessible' => $onnoquorum,
        },
        # PVE decides where a VM disk is Primary; the label is how sds knows
        # to refuse a drbd-reactor promoter (ha create, a gateway) that would
        # fight it for the role, and not to alarm on a stopped VM's disk.
        labels => { 'sds.pve/managed-by' => 'pve' },
    };
    $payload->{pool}        = $scfg->{sdspool}     if $scfg->{sdspool};
    $payload->{storageType} = $scfg->{storagetype} if $scfg->{storagetype};

    if (my $nodes = $scfg->{sdsnodes}) {
        $payload->{nodes} = [ grep { length } split(/\s*,\s*/, $nodes) ];
    } elsif ($scfg->{replicas}) {
        $payload->{replicas} = int($scfg->{replicas});
    }

    my $client = $class->_client($scfg);
    $client->request('POST', '/v1/resources', $payload);

    return $name;
}

sub free_image {
    my ($class, $storeid, $scfg, $volname, $isBase, $format) = @_;

    my ($vtype, $name) = $class->parse_volname($volname);
    my $resname = sds_resource_name($scfg, $name);
    my $client  = $class->_client($scfg);

    # DeleteResource performs the cascade teardown (HA config, gateway, DRBD
    # device, backing LV), so there is nothing to unwind here first.
    $client->request('DELETE', "/v1/resources/$resname");

    return undef;
}

sub list_images {
    my ($class, $storeid, $scfg, $vmid, $vollist, $cache) = @_;

    my $client = $class->_client($scfg);
    my $res    = $client->request('GET', '/v1/resources');

    my $res_list = $res->{resources} // [];
    my $result   = [];

    for my $info (@$res_list) {
        my ($volname, $owner) = volname_from_resource($scfg, $info->{name} // '');
        next if !defined $volname;

        my $volid = "$storeid:$volname";

        if ($vollist) {
            next if !grep { $_ eq $volid } @$vollist;
        } elsif (defined $vmid) {
            next if $owner ne $vmid;
        }

        my $sizegb = 0;
        if ($info->{volumes} && @{ $info->{volumes} }) {
            $sizegb = $info->{volumes}[0]{sizeGb} // $info->{volumes}[0]{size_gb} // 0;
        }

        push @$result, {
            volid  => $volid,
            format => 'raw',
            size   => gb_to_bytes($sizegb),
            vmid   => $owner,
        };
    }

    return $result;
}

sub volume_size_info {
    my ($class, $scfg, $storeid, $volname, $timeout) = @_;

    my ($vtype, $name) = $class->parse_volname($volname);
    my $resname = sds_resource_name($scfg, $name);
    my $info    = $class->_get_resource($scfg, $resname);

    my $sizegb = 0;
    if ($info->{volumes} && @{ $info->{volumes} }) {
        $sizegb = $info->{volumes}[0]{sizeGb} // $info->{volumes}[0]{size_gb} // 0;
    }
    my $size = gb_to_bytes($sizegb);

    return wantarray ? ($size, 'raw', 0, undef) : $size;
}

# ---------------------------------------------------------------------------
# Storage status
# ---------------------------------------------------------------------------

sub status {
    my ($class, $storeid, $scfg, $cache) = @_;

    my $client = $class->_client($scfg);
    my $res    = $client->request('GET', '/v1/pools');

    # Thin pools report the thin pool's own size and usage, and the smallest
    # node's copy bounds the storage: see SDS/Capacity.pm.
    my ($total, $free) = pool_capacity($res->{pools}, $scfg->{sdspool}, $scfg->{storagetype});

    return ($total, $free, $total - $free, 1);
}

sub activate_storage {
    my ($class, $storeid, $scfg, $cache) = @_;

    # Fail fast with the controller's own message rather than letting every
    # later call fail one at a time.
    my $client = $class->_client($scfg);
    $client->request('GET', '/v1/resources');

    return 1;
}

sub deactivate_storage {
    my ($class, $storeid, $scfg, $cache) = @_;
    return 1;
}

sub check_connection {
    my ($class, $storeid, $scfg) = @_;

    my $ok = eval {
        my $client = $class->_client($scfg);
        $client->request('GET', '/v1/resources');
        1;
    };

    return $ok ? 1 : 0;
}

# ---------------------------------------------------------------------------
# Activation
# ---------------------------------------------------------------------------

sub activate_volume {
    my ($class, $storeid, $scfg, $volname, $snapname, $cache) = @_;

    die "activating a snapshot is not supported by the sds storage plugin\n"
        if defined $snapname;

    return activate($class, $scfg, $volname);
}

sub deactivate_volume {
    my ($class, $storeid, $scfg, $volname, $snapname, $cache) = @_;

    return 1 if defined $snapname;

    return deactivate($class, $scfg, $volname);
}

# ---------------------------------------------------------------------------
# Resize
# ---------------------------------------------------------------------------

sub volume_resize {
    my ($class, $scfg, $storeid, $volname, $size, $running) = @_;

    my ($vtype, $name) = $class->parse_volname($volname);
    my $resname = sds_resource_name($scfg, $name);
    my $sizegb  = bytes_to_gb($size);
    my $client  = $class->_client($scfg);

    $client->request('PATCH', "/v1/resources/$resname/volumes/0",
        { resource => $resname, volumeId => 0, sizeGb => $sizegb });

    return gb_to_bytes($sizegb);
}

# ---------------------------------------------------------------------------
# Snapshots
# ---------------------------------------------------------------------------

sub volume_snapshot {
    my ($class, $scfg, $storeid, $volname, $snap) = @_;

    my ($vtype, $name) = $class->parse_volname($volname);
    my $resname = sds_resource_name($scfg, $name);
    my ($volpath, $node) = $class->_backing_volume_target($scfg, $resname);
    my $client = $class->_client($scfg);

    $client->request('POST', "/v1/volumes/$volpath/snapshots",
        { volume => $volpath, snapshotName => $snap, node => $node });

    return 1;
}

sub volume_snapshot_rollback {
    my ($class, $scfg, $storeid, $volname, $snap) = @_;

    my ($vtype, $name) = $class->parse_volname($volname);
    my $resname = sds_resource_name($scfg, $name);
    my ($volpath, $node) = $class->_backing_volume_target($scfg, $resname);
    my $client = $class->_client($scfg);

    $client->request('POST', "/v1/volumes/$volpath/snapshots/$snap/restore",
        { volume => $volpath, snapshotName => $snap, node => $node });

    return 1;
}

sub volume_snapshot_delete {
    my ($class, $scfg, $storeid, $volname, $snap, $running) = @_;

    my ($vtype, $name) = $class->parse_volname($volname);
    my $resname = sds_resource_name($scfg, $name);
    my ($volpath, $node) = $class->_backing_volume_target($scfg, $resname);
    my $client = $class->_client($scfg);

    # DELETE has no request body, so the node cannot travel in one: grpc-gateway
    # binds any leftover field as a query parameter. Omitting it made the
    # controller fail with "failed to delete snapshot: []" — an empty host list.
    $client->request('DELETE', "/v1/volumes/$volpath/snapshots/$snap?node=" . _uri_escape($node));

    return 1;
}

sub volume_has_feature {
    my ($class, $scfg, $feature, $storeid, $volname, $snapname, $running) = @_;

    # Snapshots are taken on the backing LV, so rolling back a running guest is
    # refused (PVE stops it first). Clones and templates are not supported: they
    # need image-level copy-on-write, which a raw DRBD device does not provide.
    my $features = {
        snapshot => { current => 1 },
        copy     => { current => 1 },
    };

    my ($vtype, $name, $vmid, $basename, $basevmid, $isBase) = $class->parse_volname($volname);

    my $key = defined($snapname) ? 'snap' : ($isBase ? 'base' : 'current');

    return 1 if $features->{$feature}->{$key};
    return undef;
}

sub rename_volume {
    my ($class, $scfg, $storeid, $source_volname, $target_vmid, $target_volname) = @_;
    die "renaming volumes is not supported by the sds storage plugin\n";
}

1;
