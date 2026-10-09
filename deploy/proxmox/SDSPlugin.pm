package PVE::Storage::Custom::SDSPlugin;

# Haify storage plugin for Proxmox VE.
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
    parse_vm_volname kib_to_gb bytes_to_gb gb_to_bytes volume_size_bytes);
use PVE::Storage::Custom::SDS::Activation qw(activate deactivate controller_unreachable local_device_path);
use PVE::Storage::Custom::SDS::Templates ();
use PVE::Storage::Custom::SDS::Api qw(negotiate_apiver);
use PVE::Storage::Custom::SDS::Token ();
use PVE::Storage::Custom::SDS::Inventory ();
use PVE::Storage::Custom::SDS::Snapshots ();

use base qw(PVE::Storage::Plugin);

our $VERSION = '0.1.0';


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
    return negotiate_apiver(PVE::Storage::APIVER(), PVE::Storage::APIAGE());
}

# PVE hands a sensitive property (apitoken) to these hooks instead of writing
# it to storage.cfg; SDS/Token.pm keeps it under /etc/pve/priv.
sub on_add_hook    { my ($class, $storeid, $scfg, %p) = @_; return PVE::Storage::Custom::SDS::Token::on_add($storeid, \%p); }
sub on_update_hook { my ($class, $storeid, $scfg, %p) = @_; return PVE::Storage::Custom::SDS::Token::on_update($storeid, \%p); }
sub on_delete_hook { my ($class, $storeid, $scfg) = @_; return PVE::Storage::Custom::SDS::Token::on_delete($storeid); }

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
            description => "What a new volume does when its node loses quorum or every UpToDate replica: suspend-io (default) freezes the guest's I/O until it is back, io-error fails it.",
            type        => 'string',
            enum        => [ 'suspend-io', 'io-error' ],
        },
        exactsize => {
            description => "Give each new or resized disk exactly the size PVE asks for (the default). 0 rounds up to "
                . "whole GiB instead, which breaks restoring a vzdump backup and online Move Disk onto this storage: "
                . "both need a disk of the source's exact size.",
            type    => 'boolean',
            default => 1,
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
        exactsize      => { optional => 1 },
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
    return PVE::Storage::Custom::SDS::Client->new($scfg, PVE::Storage::Custom::SDS::Token::token_for($scfg));
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

    # Prefer the Primary: its data is the replica the guest is actually writing.
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
    # A template's disk (create_base).
    if ($volname =~ m/^base-(\d+)-disk-\d+$/) {
        return ('images', $volname, $1, undef, undef, 1, 'raw');
    }

    die "unable to parse sds volume name '$volname'\n";
}

sub filesystem_path {
    my ($class, $scfg, $volname, $snapname) = @_;

    my ($vtype, $name, $vmid) = $class->parse_volname($volname);
    if (defined $snapname) {
        my $path = PVE::Storage::Custom::SDS::Snapshots::snapshot_path($class, $scfg, $name, $snapname);
        return wantarray ? ($path, $vmid, $vtype) : $path;
    }
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

# Templates, full-copy clones and reassigning: SDS/Templates.pm.
sub create_base {
    my ($class, $storeid, $scfg, $volname) = @_;
    return PVE::Storage::Custom::SDS::Templates::create_base($class, $storeid, $scfg, $volname);
}

sub clone_image {
    my ($class, $scfg, $storeid, $volname, $vmid, $snap) = @_;
    return PVE::Storage::Custom::SDS::Templates::clone_image($class, $scfg, $storeid, $volname, $vmid, $snap);
}

# Exact sizes unless storage.cfg says `exactsize 0`.
sub exact_size {
    my ($scfg) = @_;
    return !defined($scfg->{exactsize}) || $scfg->{exactsize} ? 1 : 0;
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
    # PVE passes KiB, and the device is exactly that rather than the next whole
    # GiB: a vzdump restore and an online Move Disk both refuse a disk that is
    # not byte-for-byte the source's size.
    $payload->{sizeBytes}   = $size * 1024         if exact_size($scfg);
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
    # Placement and live state for the web interface (SDS/Inventory.pm).
    my $states   = PVE::Storage::Custom::SDS::Inventory::local_states();
    my $self     = _nodename();

    for my $info (@$res_list) {
        my ($volname, $owner) = volname_from_resource($scfg, $info->{name} // '');
        next if !defined $volname;

        my $volid = "$storeid:$volname";

        if ($vollist) {
            next if !grep { $_ eq $volid } @$vollist;
        } elsif (defined $vmid) {
            next if $owner ne $vmid;
        }

        my $size = ($info->{volumes} && @{ $info->{volumes} }) ? volume_size_bytes($info->{volumes}[0]) : 0;

        push @$result, {
            volid  => $volid,
            format => 'raw',
            size   => $size,
            vmid   => $owner,
            %{ PVE::Storage::Custom::SDS::Inventory::fields($info, $states->{ $info->{name} }, $self) },
        };
    }

    return $result;
}

sub volume_size_info {
    my ($class, $scfg, $storeid, $volname, $timeout) = @_;

    my ($vtype, $name) = $class->parse_volname($volname);
    my $resname = sds_resource_name($scfg, $name);
    my $info    = eval { $class->_get_resource($scfg, $resname) };
    if (my $err = $@) {
        # PVE asks the size when it starts a guest, so a volume that is up
        # here answers from its device when the controller cannot.
        my $size = controller_unreachable($err) ? _local_device_bytes($resname) : undef;
        die $err if !$size;
        return wantarray ? ($size, 'raw', 0, undef) : $size;
    }

    my $size = ($info->{volumes} && @{ $info->{volumes} }) ? volume_size_bytes($info->{volumes}[0]) : 0;

    return wantarray ? ($size, 'raw', 0, undef) : $size;
}

# Test seams: where the kernel publishes block device sizes, and how a
# by-res link is resolved to its /dev/drbdN.
our $SYSFS_BLOCK = '/sys/class/block';
our $RESOLVE     = sub { return Cwd::abs_path($_[0]) };

# The size of a volume's DRBD device on this node, read from sysfs (which
# needs no open, so a Secondary answers too), or undef when it is not up here.
sub _local_device_bytes {
    my ($resname) = @_;
    my $local = local_device_path($resname);
    return undef if !$DEVICE_CHECK->($local);
    my $dev = $RESOLVE->($local) // return undef;
    $dev =~ s{^.*/}{};
    open(my $fh, '<', "$SYSFS_BLOCK/$dev/size") or return undef;
    my $sectors = <$fh>;
    close($fh);
    return undef if !defined($sectors) || $sectors !~ m/^(\d+)/ || !$1;
    return $1 * 512;
}

# ---------------------------------------------------------------------------
# Storage status
# ---------------------------------------------------------------------------

sub status {
    my ($class, $storeid, $scfg, $cache) = @_;

    my $client = $class->_client($scfg);
    my $res    = $client->request('GET', '/v1/pools');

    # Thin pools report the thin pool's own size and usage, and the smallest
    # node's replica bounds the storage: see SDS/Capacity.pm.
    my ($total, $free) = pool_capacity($res->{pools}, $scfg->{sdspool}, $scfg->{storagetype});

    return ($total, $free, $total - $free, 1);
}

sub activate_storage {
    my ($class, $storeid, $scfg, $cache) = @_;

    # Fail fast with the controller's own message rather than letting every
    # later call fail one at a time. An unreachable controller is not that
    # failure: see check_connection.
    eval { $class->_client($scfg)->request('GET', '/v1/resources') };
    if (my $err = $@) {
        die $err if !_usable_without_controller($err);
        warn "sds: controller unreachable; only volumes already set up on this node can be used\n";
    }

    return 1;
}

sub deactivate_storage {
    my ($class, $storeid, $scfg, $cache) = @_;
    return 1;
}

# PVE asks this before activating any volume, and refuses to start a guest on
# a storage that says no. With the controller unreachable the volumes this
# node already has are still DRBD devices it can promote (SDS/Activation.pm),
# so the storage counts as online while DRBD is loaded here; anything that
# needs the controller (a new disk, a snapshot) still fails, with its message.
sub check_connection {
    my ($class, $storeid, $scfg) = @_;

    my $ok = eval { $class->_client($scfg)->request('GET', '/v1/resources'); 1 };
    return 1 if $ok;
    return _usable_without_controller($@) ? 1 : 0;
}

sub _usable_without_controller {
    my ($err) = @_;
    return controller_unreachable($err) && -e $PVE::Storage::Custom::SDS::Activation::DRBD_PROC;
}

# ---------------------------------------------------------------------------
# Activation
# ---------------------------------------------------------------------------

sub activate_volume {
    my ($class, $storeid, $scfg, $volname, $snapname, $cache) = @_;

    return PVE::Storage::Custom::SDS::Snapshots::activate_snapshot($class, $scfg, $volname, $snapname)
        if defined $snapname;

    return activate($class, $scfg, $volname);
}

sub deactivate_volume {
    my ($class, $storeid, $scfg, $volname, $snapname, $cache) = @_;

    return PVE::Storage::Custom::SDS::Snapshots::deactivate_snapshot($class, $scfg, $volname, $snapname)
        if defined $snapname;

    return deactivate($class, $scfg, $volname);
}

# ---------------------------------------------------------------------------
# Resize
# ---------------------------------------------------------------------------

sub volume_resize {
    my ($class, $scfg, $storeid, $volname, $size, $running, $snapname) = @_;

    # API 15: PVE resizes a snapshot only for storages with
    # snapshot-as-volume-chain, which sds snapshots are not.
    die "resizing a snapshot is not supported by the sds storage plugin\n" if defined $snapname;

    my ($vtype, $name) = $class->parse_volname($volname);
    my $resname = sds_resource_name($scfg, $name);
    my $sizegb  = bytes_to_gb($size);
    my $client  = $class->_client($scfg);

    if (exact_size($scfg)) {
        $client->request('PATCH', "/v1/resources/$resname/volumes/0",
            { resource => $resname, volumeId => 0, sizeBytes => $size });
        # The controller rounds up to a whole 512-byte sector.
        return int(($size + 511) / 512) * 512;
    }
    $client->request('PATCH', "/v1/resources/$resname/volumes/0",
        { resource => $resname, volumeId => 0, sizeGb => $sizegb });

    return gb_to_bytes($sizegb);
}

# ---------------------------------------------------------------------------
# Snapshots
# ---------------------------------------------------------------------------

# Snapshots, and opening one read-only: SDS/Snapshots.pm.
sub volume_snapshot {
    my ($class, $scfg, $storeid, $volname, $snap) = @_;
    return PVE::Storage::Custom::SDS::Snapshots::snapshot($class, $scfg, $volname, $snap);
}

sub volume_snapshot_rollback {
    my ($class, $scfg, $storeid, $volname, $snap) = @_;
    return PVE::Storage::Custom::SDS::Snapshots::rollback($class, $scfg, $volname, $snap);
}

sub volume_snapshot_delete {
    my ($class, $scfg, $storeid, $volname, $snap, $running) = @_;
    return PVE::Storage::Custom::SDS::Snapshots::delete_snapshot($class, $scfg, $volname, $snap);
}

sub volume_has_feature {
    my ($class, $scfg, $feature, $storeid, $volname, $snapname, $running) = @_;

    # Snapshots are taken on the backing LV, so rolling back a running guest is
    # refused (PVE stops it first). A "linked" clone of a template is a full
    # copy (SDS/Templates.pm): a raw DRBD device has no image-level
    # copy-on-write. Renaming covers reassigning a disk to another VM.
    my $features = {
        snapshot => { current => 1 },
        copy     => { current => 1, base => 1 },
        clone    => { base => 1 },
        template => { current => 1 },
        rename   => { current => 1 },
    };

    my ($vtype, $name, $vmid, $basename, $basevmid, $isBase) = $class->parse_volname($volname);

    my $key = defined($snapname) ? 'snap' : ($isBase ? 'base' : 'current');

    return 1 if $features->{$feature}->{$key};
    return undef;
}

sub rename_volume {
    my ($class, $scfg, $storeid, $source_volname, $target_vmid, $target_volname) = @_;
    return PVE::Storage::Custom::SDS::Templates::rename_volume($class, $scfg, $storeid, $source_volname,
        $target_vmid, $target_volname);
}

1;
