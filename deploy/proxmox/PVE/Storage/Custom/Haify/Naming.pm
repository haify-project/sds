package PVE::Storage::Custom::Haify::Naming;

# Pure mapping between PVE and haify, used by HaifyPlugin.pm: volume names to
# resource names, PVE sizes to haify gigabytes, and which node holds what in a
# resource's state.

use strict;
use warnings;

use Exporter qw(import);
our @EXPORT_OK = qw(
    resource_prefix haify_resource_name volname_from_resource parse_vm_volname
    kib_to_gb bytes_to_gb gb_to_bytes same_pool volume_size_bytes
    _node_participates _other_primary_node
);

# ---------------------------------------------------------------------------
# Naming
#
# One PVE disk = one haify DRBD resource, so each disk resizes, snapshots and
# deletes independently (the same model the CSI driver uses for a PVC).
#
# PVE volume "vm-<vmid>-disk-<n>"  <->  haify resource "<prefix>-<vmid>-<n>".
# PVE volume "vm-<vmid>-<other>"   <->  haify resource "<prefix>-<vmid>-<other>",
# for the volumes PVE names otherwise: "vm-<vmid>-cloudinit", a snapshot's RAM
# "vm-<vmid>-state-<snap>", a backup's "vm-<vmid>-fleece-<n>". <other> starts
# with a letter, so it can never be read back as a disk number, and is not
# "disk-<n>", whose resource name is the short one.
# VM ids are unique cluster-wide, so the mapping is collision-free, and it is
# reversible, which is what makes list_images possible without a side table.
# ---------------------------------------------------------------------------

# The <other> part of a volume name: DRBD-, LVM- and shell-safe, and short
# enough that "<prefix>-<vmid>-<other>_data" stays a valid LV name.
my $OTHER_RE = qr/[A-Za-z][A-Za-z0-9_-]{0,63}/;

# parse_vm_volname splits a PVE volume name into (vmid, disk index) for a
# disk, (vmid, undef, other) for any other per-VM volume, or an empty list.
sub parse_vm_volname {
    my ($volname) = @_;
    return ($1, $2) if $volname =~ m/^vm-(\d+)-disk-(\d+)$/;
    return () if $volname =~ m/^vm-\d+-disk-/;
    return ($1, undef, $2) if $volname =~ m/^vm-(\d+)-($OTHER_RE)$/;
    return ();
}

sub resource_prefix {
    my ($scfg) = @_;
    my $prefix = $scfg->{resourceprefix};
    return (defined($prefix) && length($prefix)) ? $prefix : 'pve';
}

sub haify_resource_name {
    my ($scfg, $volname) = @_;
    # A template disk: base-<vmid>-disk-<n> <-> <prefix>-base-<vmid>-<n>.
    return resource_prefix($scfg) . "-base-$1-$2" if $volname =~ m/^base-(\d+)-disk-(\d+)$/;
    my ($vmid, $idx, $other) = parse_vm_volname($volname);
    die "unable to map volume '$volname' to an haify resource\n" if !defined $vmid;
    return resource_prefix($scfg) . "-$vmid-" . ($idx // $other);
}

# Inverse of haify_resource_name. Returns (volname, vmid), or an empty list when
# the resource does not belong to this storage.
sub volname_from_resource {
    my ($scfg, $resname) = @_;
    my $prefix = resource_prefix($scfg);
    return ("vm-$1-disk-$2", $1) if $resname =~ m/^\Q$prefix\E-(\d+)-(\d+)$/;
    return ("base-$1-disk-$2", $1) if $resname =~ m/^\Q$prefix\E-base-(\d+)-(\d+)$/;
    if ($resname =~ m/^\Q$prefix\E-(\d+)-($OTHER_RE)$/) {
        my ($vmid, $other) = ($1, $2);
        # "<prefix>-<vmid>-disk-<n>" has no volume: disk n maps to the short form.
        return () if $other =~ m/^disk-/;
        return ("vm-$vmid-$other", $vmid);
    }
    return ();
}

# PVE speaks KiB in alloc_image and bytes in volume_resize; haify allocates whole
# gigabytes. Always round UP: a guest must never get less space than it asked
# for, and handing back a smaller disk than the config records corrupts guests.
# The controller names every pool it manages "haify_<name>", and accepts either
# form when a resource is created, so `haifypool vg0` and `haifypool haify_vg0` name
# the same pool. A literal comparison made a storage configured the short way
# report zero capacity.
sub same_pool {
    my ($x, $y) = @_;
    s/^haify_// for ($x, $y);
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

# The size of a volume as the controller reports it: its exact size when it
# was given one (storage option exactsize), else its whole GiB. The REST API
# renders 64-bit integers as JSON strings, which Perl reads as numbers.
sub volume_size_bytes {
    my ($vol) = @_;
    my $exact = $vol->{sizeBytes} // $vol->{size_bytes} // 0;
    return int($exact) if $exact;
    return gb_to_bytes($vol->{sizeGb} // $vol->{size_gb} // 0);
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
