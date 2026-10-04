package PVE::Storage::Custom::SDS::Snapshots;

# VM disk snapshots, for SDSPlugin.pm.
#
# A snapshot is a resource snapshot: the disk's backing volume snapshotted on
# every replica at once, with I/O suspended across them, so a rollback brings
# all replicas back together and DRBD resyncs nothing
# (pkg/controller/resource_snapshot.go). Snapshots taken by earlier plugin
# versions live on one replica only, under the snapshot's own name; rolling
# back to or deleting one of those still goes the old way.
#
# A snapshot can also be opened read-only on a node that holds a replica, by
# activating the LVM snapshot there. That is what vzdump's snapshot mode for
# containers needs: it mounts the snapshot and archives it. On a node without
# a replica there is no snapshot to open, and the error says where to run it.

use strict;
use warnings;

use Exporter qw(import);
use PVE::Storage::Custom::SDS::Client qw(_uri_escape);
use PVE::Storage::Custom::SDS::Naming qw(sds_resource_name);
use PVE::Storage::Custom::SDS::Activation ();

our @EXPORT_OK = qw(snapshot rollback delete_snapshot snapshot_path activate_snapshot deactivate_snapshot);

sub _resource_snapshots {
    my ($plugin, $scfg, $resname) = @_;
    my $res = $plugin->_client($scfg)->request('GET', "/v1/resources/$resname/snapshots");
    return { map { $_ => 1 } @{ $res->{names} // [] } };
}

sub snapshot {
    my ($plugin, $scfg, $volname, $snap) = @_;
    my $resname = sds_resource_name($scfg, $volname);
    $plugin->_client($scfg)->request('POST', "/v1/resources/$resname/snapshots",
        { resource => $resname, name => $snap });
    return 1;
}

sub rollback {
    my ($plugin, $scfg, $volname, $snap) = @_;
    my $resname = sds_resource_name($scfg, $volname);
    my $client  = $plugin->_client($scfg);
    if (_resource_snapshots($plugin, $scfg, $resname)->{$snap}) {
        $client->request('POST', "/v1/resources/$resname/snapshots/$snap/rollback",
            { resource => $resname, name => $snap });
        return 1;
    }
    # A snapshot from before resource snapshots: on one replica, which the
    # others then resync from.
    my ($volpath, $node) = $plugin->_backing_volume_target($scfg, $resname);
    $client->request('POST', "/v1/volumes/$volpath/snapshots/$snap/restore",
        { volume => $volpath, snapshotName => $snap, node => $node });
    return 1;
}

sub delete_snapshot {
    my ($plugin, $scfg, $volname, $snap) = @_;
    my $resname = sds_resource_name($scfg, $volname);
    my $client  = $plugin->_client($scfg);
    if (_resource_snapshots($plugin, $scfg, $resname)->{$snap}) {
        $client->request('DELETE', "/v1/resources/$resname/snapshots/$snap");
        return 1;
    }
    my ($volpath, $node) = $plugin->_backing_volume_target($scfg, $resname);
    # DELETE has no body; grpc-gateway takes the node as a query parameter.
    $client->request('DELETE', "/v1/volumes/$volpath/snapshots/$snap?node=" . _uri_escape($node));
    return 1;
}

# _local_snapshot_lv is "<vg>/<backing>_snap_<snap>" for a snapshot this node
# holds a replica of, or dies saying where it is.
sub _local_snapshot_lv {
    my ($plugin, $scfg, $volname, $snap) = @_;
    my $resname = sds_resource_name($scfg, $volname);
    my $info    = $plugin->_get_resource($scfg, $resname);
    my $node    = PVE::Storage::Custom::SDSPlugin::_nodename();
    my @nodes   = @{ $info->{nodes} // [] };
    die "snapshot '$snap' of $volname is on its replica nodes (@nodes), not on $node; run this there\n"
        if !grep { $_ eq $node } @nodes;
    my $vol = ($info->{volumes} && @{ $info->{volumes} }) ? $info->{volumes}[0] : {};
    my ($pool, $lv) = ($vol->{pool}, $vol->{backingVolume} // $vol->{backing_volume});
    die "$volname has no backing volume recorded\n" if !$pool || !$lv;
    die "opening a ZFS snapshot is not supported by the sds storage plugin\n"
        if ($scfg->{storagetype} // '') eq 'zfs';
    return "$pool/${lv}_snap_$snap";
}

sub snapshot_path {
    my ($plugin, $scfg, $volname, $snap) = @_;
    return '/dev/' . _local_snapshot_lv($plugin, $scfg, $volname, $snap);
}

# LVM creates thin snapshots with activation skipped; -K activates one anyway.
sub activate_snapshot {
    my ($plugin, $scfg, $volname, $snap) = @_;
    my $lv = _local_snapshot_lv($plugin, $scfg, $volname, $snap);
    my ($rc, $out) = $PVE::Storage::Custom::SDS::Activation::RUN->('lvchange', '-ay', '-K', $lv);
    die "activating snapshot $lv failed: $out\n" if $rc != 0;
    return 1;
}

sub deactivate_snapshot {
    my ($plugin, $scfg, $volname, $snap) = @_;
    my $lv = eval { _local_snapshot_lv($plugin, $scfg, $volname, $snap) } or return 1;
    $PVE::Storage::Custom::SDS::Activation::RUN->('lvchange', '-an', $lv);
    return 1;
}

1;
