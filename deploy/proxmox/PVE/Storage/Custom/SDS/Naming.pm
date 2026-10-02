package PVE::Storage::Custom::SDS::Naming;

# Pure mapping between PVE and sds, used by SDSPlugin.pm: volume names to
# resource names, PVE sizes to sds gigabytes, and which node holds what in a
# resource's state.

use strict;
use warnings;

use Exporter qw(import);
our @EXPORT_OK = qw(
    resource_prefix sds_resource_name volname_from_resource
    kib_to_gb bytes_to_gb gb_to_bytes same_pool
    _node_participates _other_primary_node
);

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
# The controller names every pool it manages "sds_<name>", and accepts either
# form when a resource is created, so `sdspool vg0` and `sdspool sds_vg0` name
# the same pool. A literal comparison made a storage configured the short way
# report zero capacity.
sub same_pool {
    my ($x, $y) = @_;
    s/^sds_// for ($x, $y);
    return $x eq $y;
}

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

1;
