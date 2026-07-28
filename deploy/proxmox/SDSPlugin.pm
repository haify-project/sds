package PVE::Storage::Custom::SDSPlugin;

# SDS storage plugin for Proxmox VE.
#
# Backs VM/CT disks with DRBD resources managed by sds-controller, so guests get
# synchronous replication, HA restart on a surviving node, and RAM-only live
# migration (the disk is already on every node, so nothing is copied).
#
# The plugin holds NO storage logic of its own: every operation is a REST call
# to sds-controller's grpc-gateway. It talks HTTP directly with HTTP::Tiny +
# JSON::PP, both of which ship with Proxmox VE, so installing the plugin means
# copying this one file — no extra packages, no sds binaries on the PVE nodes.
#
# See docs/superpowers/specs/2026-07-02-proxmox-storage-plugin-design.md.

use strict;
use warnings;

use HTTP::Tiny;
use JSON::PP qw(encode_json decode_json);

use PVE::INotify;
use PVE::Storage::Plugin;

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
            description => "sds-controller address as host or host:port (REST port defaults to 3375).",
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
        nodes          => { optional => 1 },
        disable        => { optional => 1 },
        content        => { optional => 1 },
        shared         => { optional => 1 },
        bwlimit        => { optional => 1 },
    };
}

# ---------------------------------------------------------------------------
# REST client
# ---------------------------------------------------------------------------

sub _client {
    my ($class, $scfg) = @_;
    return $CLIENT_FACTORY->($scfg) if $CLIENT_FACTORY;
    return PVE::Storage::Custom::SDSPlugin::RestClient->new($scfg);
}

# ---------------------------------------------------------------------------
# Naming
#
# One PVE disk = one sds DRBD resource, so each disk resizes, snapshots and
# deletes independently (the same model the CSI driver uses for a PVC).
#
# PVE volume "vm-<vmid>-disk-<n>"  <->  sds resource "<prefix>-<vmid>-<n>".
# VM ids are unique cluster-wide, so the mapping is collision-free, and it is
# reversible, which is what makes list_images possible without a side table.
# ---------------------------------------------------------------------------

sub resource_prefix {
    my ($scfg) = @_;
    my $prefix = $scfg->{resourceprefix};
    return (defined($prefix) && length($prefix)) ? $prefix : 'pve';
}

sub sds_resource_name {
    my ($scfg, $volname) = @_;
    my ($vmid, $idx) = ($volname =~ m/^vm-(\d+)-disk-(\d+)$/);
    die "unable to map volume '$volname' to an sds resource\n" if !defined $vmid;
    return resource_prefix($scfg) . "-$vmid-$idx";
}

# Inverse of sds_resource_name. Returns (volname, vmid), or an empty list when
# the resource does not belong to this storage.
sub volname_from_resource {
    my ($scfg, $resname) = @_;
    my $prefix = resource_prefix($scfg);
    my ($vmid, $idx) = ($resname =~ m/^\Q$prefix\E-(\d+)-(\d+)$/);
    return () if !defined $vmid;
    return ("vm-$vmid-disk-$idx", $vmid);
}

# PVE speaks KiB in alloc_image and bytes in volume_resize; sds allocates whole
# gigabytes. Always round UP: a guest must never get less space than it asked
# for, and handing back a smaller disk than the config records corrupts guests.
sub kib_to_gb {
    my ($kib) = @_;
    my $gb = int(($kib + 1048575) / 1048576);
    return $gb > 0 ? $gb : 1;
}

sub bytes_to_gb {
    my ($bytes) = @_;
    my $gb = int(($bytes + 1073741823) / 1073741824);
    return $gb > 0 ? $gb : 1;
}

sub gb_to_bytes {
    my ($gb) = @_;
    return int($gb) * 1073741824;
}

# ---------------------------------------------------------------------------
# sds helpers
# ---------------------------------------------------------------------------

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

# The volume's "<pool>/<lv>" path, which is what the snapshot API keys on.
sub _backing_volume_path {
    my ($class, $scfg, $resname) = @_;
    my $info = $class->_get_resource($scfg, $resname);
    my $vol  = ($info->{volumes} && @{ $info->{volumes} }) ? $info->{volumes}[0] : undef;
    die "resource '$resname' has no volumes\n" if !$vol;
    my $pool = $vol->{pool};
    my $lv   = $vol->{backingVolume} // $vol->{backing_volume};
    die "resource '$resname' has no backing volume recorded\n"
        if !defined($pool) || !defined($lv) || !length($pool) || !length($lv);
    return "$pool/$lv";
}

# True when this node already participates in the resource's DRBD mesh, either
# as a replica or as a diskless client.
sub _node_participates {
    my ($info, $node) = @_;
    return 0 if !$info;
    for my $list ($info->{nodes}, $info->{disklessClients}, $info->{diskless_clients}) {
        next if !$list;
        for my $n (@$list) {
            return 1 if defined($n) && $n eq $node;
        }
    }
    return 0;
}

# The name of another node currently holding Primary, or undef. A live migration
# is exactly this situation: the target activates while the source is still
# Primary, which is the only moment dual-primary may be opened.
sub _other_primary_node {
    my ($status, $node) = @_;
    return undef if !$status;
    my $states = $status->{nodeStates} // $status->{node_states} // {};
    for my $peer (sort keys %$states) {
        next if $peer eq $node;
        my $role = $states->{$peer}{role} // '';
        return $peer if lc($role) eq 'primary';
    }
    return undef;
}

sub _set_dual_primary {
    my ($class, $scfg, $resname, $enable) = @_;
    my $client = $class->_client($scfg);
    $client->request('POST', "/v1/resources/$resname/dual-primary",
        { resource => $resname, enable => $enable ? JSON::PP::true : JSON::PP::false });
}

sub _wait_for_device {
    my ($path) = @_;
    my $deadline = time() + $DEVICE_WAIT_SECONDS;
    while (1) {
        return 1 if $DEVICE_CHECK->($path);
        return 0 if time() >= $deadline;
        select(undef, undef, undef, $DEVICE_POLL_INTERVAL);
    }
}

# ---------------------------------------------------------------------------
# Volume naming / paths
# ---------------------------------------------------------------------------

sub parse_volname {
    my ($class, $volname) = @_;

    if ($volname =~ m/^vm-(\d+)-disk-(\d+)$/) {
        # (vtype, name, vmid, basename, basevmid, isBase, format)
        return ('images', $volname, $1, undef, undef, undef, 'raw');
    }

    die "unable to parse sds volume name '$volname'\n";
}

sub filesystem_path {
    my ($class, $scfg, $volname, $snapname) = @_;

    die "sds volumes cannot be addressed by snapshot path\n" if defined $snapname;

    my ($vtype, $name, $vmid) = $class->parse_volname($volname);
    my $resname = sds_resource_name($scfg, $name);
    my $info    = $class->_get_resource($scfg, $resname);
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

    die "illegal name '$name' - should be 'vm-$vmid-disk-<n>'\n"
        if $name !~ m/^vm-\Q$vmid\E-disk-(\d+)$/;

    my $resname = sds_resource_name($scfg, $name);
    my $sizegb  = kib_to_gb($size);

    my $payload = {
        name        => $resname,
        sizeGb      => $sizegb,
        protocol    => 'C',
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

    my $want = $scfg->{sdspool};
    my ($total, $free);

    for my $pool (@{ $res->{pools} // [] }) {
        next if defined($want) && length($want) && ($pool->{name} // '') ne $want;

        my $ptotal = gb_to_bytes($pool->{totalGb} // $pool->{total_gb} // 0);
        my $pfree  = gb_to_bytes($pool->{freeGb}  // $pool->{free_gb}  // 0);

        # A pool exists once per node and a replica must fit on EVERY node that
        # holds one, so the usable capacity is the smallest node's, not the sum.
        # Reporting the sum would let PVE accept a disk that cannot be placed.
        $total = $ptotal if !defined($total) || $ptotal < $total;
        $free  = $pfree  if !defined($free)  || $pfree  < $free;
    }

    $total //= 0;
    $free  //= 0;

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

    my ($vtype, $name) = $class->parse_volname($volname);
    my $resname = sds_resource_name($scfg, $name);
    my $node    = _nodename();
    my $client  = $class->_client($scfg);

    # A PVE host that stores no replica still has to see /dev/drbdN to run the
    # guest, so it joins the resource as a diskless client. This is what lets a
    # compute-only hypervisor take part at all, including as a migration target.
    my $info = $class->_get_resource($scfg, $resname);
    if (!_node_participates($info, $node)) {
        $client->request('POST', "/v1/resources/$resname/diskless-clients",
            { resource => $resname, node => $node });
    }

    # Live migration is the one case where two nodes legitimately hold the disk
    # open at once. Detect it by another node still being Primary, and open the
    # dual-primary window only then.
    my $status = $class->_get_status($scfg, $resname);
    my $peer   = _other_primary_node($status, $node);
    my $opened = 0;

    if (defined $peer) {
        $class->_set_dual_primary($scfg, $resname, 1);
        $opened = 1;
    }

    eval {
        # quorum-guarded: sds force-promotes only if this node holds DRBD
        # quorum and refuses otherwise, so an HA restart after a hard node
        # failure cannot split-brain.
        $client->request('POST', "/v1/resources/$resname/primary",
            { resource => $resname, node => $node, quorumGuarded => JSON::PP::true });
    };
    if (my $err = $@) {
        # Never leave the window open on a failed promote.
        eval { $class->_set_dual_primary($scfg, $resname, 0) } if $opened;
        die $err;
    }

    my $device = $class->filesystem_path($scfg, $volname);
    if (!_wait_for_device($device)) {
        eval { $class->_set_dual_primary($scfg, $resname, 0) } if $opened;
        die "sds volume '$volname' promoted but $device did not appear within ${DEVICE_WAIT_SECONDS}s\n";
    }

    return 1;
}

sub deactivate_volume {
    my ($class, $storeid, $scfg, $volname, $snapname, $cache) = @_;

    return 1 if defined $snapname;

    my ($vtype, $name) = $class->parse_volname($volname);
    my $resname = sds_resource_name($scfg, $name);
    my $node    = _nodename();
    my $client  = $class->_client($scfg);

    my $demote_err;
    eval {
        $client->request('POST', "/v1/resources/$resname/secondary",
            { resource => $resname, node => $node });
    };
    $demote_err = $@ if $@;

    # Close the dual-primary window unconditionally: this is the source side of
    # a completed migration, and leaving it open is the failure mode the whole
    # bracket exists to prevent. Disabling is idempotent and verified by sds,
    # so it is safe even when no window was ever opened.
    eval { $class->_set_dual_primary($scfg, $resname, 0) };
    my $dual_err = $@;

    die $demote_err if $demote_err;
    die $dual_err   if $dual_err;

    return 1;
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
    my $volpath = $class->_backing_volume_path($scfg, $resname);
    my $client  = $class->_client($scfg);

    $client->request('POST', "/v1/volumes/$volpath/snapshots",
        { volume => $volpath, snapshotName => $snap, node => _nodename() });

    return 1;
}

sub volume_snapshot_rollback {
    my ($class, $scfg, $storeid, $volname, $snap) = @_;

    my ($vtype, $name) = $class->parse_volname($volname);
    my $resname = sds_resource_name($scfg, $name);
    my $volpath = $class->_backing_volume_path($scfg, $resname);
    my $client  = $class->_client($scfg);

    $client->request('POST', "/v1/volumes/$volpath/snapshots/$snap/restore",
        { volume => $volpath, snapshotName => $snap, node => _nodename() });

    return 1;
}

sub volume_snapshot_delete {
    my ($class, $scfg, $storeid, $volname, $snap, $running) = @_;

    my ($vtype, $name) = $class->parse_volname($volname);
    my $resname = sds_resource_name($scfg, $name);
    my $volpath = $class->_backing_volume_path($scfg, $resname);
    my $client  = $class->_client($scfg);

    $client->request('DELETE', "/v1/volumes/$volpath/snapshots/$snap");

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

# ---------------------------------------------------------------------------
# REST client
# ---------------------------------------------------------------------------

package PVE::Storage::Custom::SDSPlugin::RestClient;

use strict;
use warnings;

use HTTP::Tiny;
use JSON::PP qw(encode_json decode_json);

my $DEFAULT_REST_PORT = 3375;

sub new {
    my ($class, $scfg) = @_;

    my $controller = $scfg->{controller}
        or die "sds storage: 'controller' is not configured\n";

    my ($host, $port);
    if ($controller =~ m/^\[(.+)\]:(\d+)$/) {          # [::1]:3375
        ($host, $port) = ($1, $2);
    } elsif ($controller =~ m/^([^:]+):(\d+)$/) {      # host:3375
        ($host, $port) = ($1, $2);
    } else {
        ($host, $port) = ($controller, $DEFAULT_REST_PORT);
    }

    my $self = {
        host  => $host,
        port  => $port,
        token => $scfg->{apitoken},
        ua    => HTTP::Tiny->new(timeout => 60, agent => "sds-pve-plugin/$PVE::Storage::Custom::SDSPlugin::VERSION "),
    };

    return bless $self, $class;
}

sub request {
    my ($self, $method, $path, $payload) = @_;

    my $host = $self->{host};
    $host = "[$host]" if $host =~ m/:/;    # bare IPv6
    my $url = "http://$host:$self->{port}$path";

    my $opts = {
        headers => {
            'Content-Type' => 'application/json',
            'Accept'       => 'application/json',
        },
    };
    $opts->{headers}{'Authorization'} = "Bearer $self->{token}"
        if defined($self->{token}) && length($self->{token});
    $opts->{content} = encode_json($payload) if defined $payload;

    my $res = $self->{ua}->request($method, $url, $opts);

    # HTTP::Tiny reports transport failures as status 599 with the reason in the
    # body; surfacing that verbatim is far more useful than "storage offline".
    die "sds controller unreachable at $host:$self->{port}: " . ($res->{content} // 'unknown error') . "\n"
        if ($res->{status} // 0) == 599;

    my $data;
    if (defined($res->{content}) && length($res->{content})) {
        $data = eval { decode_json($res->{content}) };
        if (!defined $data) {
            die "sds controller returned unparsable response for $method $path: $res->{content}\n"
                if $res->{success};
            die "sds controller error ($res->{status}) for $method $path: $res->{content}\n";
        }
    }

    if (!$res->{success}) {
        # grpc-gateway renders gRPC errors as {"code":..,"message":..}.
        my $msg = (ref($data) eq 'HASH' && defined($data->{message})) ? $data->{message} : ($res->{reason} // 'request failed');
        die "sds controller error ($res->{status}) for $method $path: $msg\n";
    }

    # The controller answers application-level failures with HTTP 200 and
    # success=false. protojson omits false fields, so a MISSING success key is a
    # failure too — never treat "absent" as "fine".
    if (ref($data) eq 'HASH' && exists($data->{message}) && !$data->{success}) {
        die "sds: $data->{message}\n";
    }

    return $data // {};
}

1;
