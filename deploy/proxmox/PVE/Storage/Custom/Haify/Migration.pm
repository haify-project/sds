package PVE::Storage::Custom::Haify::Migration;

# Whether a second Primary is a live migration or a leftover.
#
# activate_volume opens DRBD's dual-primary window when another node already
# holds the volume Primary, because during a live migration the source keeps
# the disk open until the guest has moved. "Another node is Primary" is not
# evidence of a migration on its own, though: a deactivate that failed earlier
# (the controller was unreachable, the demote timed out) leaves the old node
# Primary, and the next start of the guest anywhere else would then run, for
# its whole lifetime, with two writers allowed and nothing to stop the old
# node from writing. That is how a guest filesystem gets corrupted.
#
# So the window is only opened when PVE itself says a live migration is under
# way: the guest's config still sits on the source node (PVE moves it to the
# target only once the migration finishes) and carries `lock: migrate`. The
# config is read from pmxcfs, which every cluster node sees.

use strict;
use warnings;

use Exporter qw(import);
our @EXPORT_OK = qw(live_migration_source assert_live_migration);

# Where pmxcfs keeps each node's guest configs. A test seam.
our $NODES_DIR = '/etc/pve/nodes';

# The PVE node VM $vmid is being live-migrated away from, or undef when it is
# not being migrated to $node. Containers never qualify: they migrate by
# restart, so their disk is never open on two nodes at once.
sub live_migration_source {
    my ($vmid, $node) = @_;
    return undef if !defined($vmid) || $vmid !~ m/^\d+$/;

    for my $conf (glob("$NODES_DIR/*/qemu-server/$vmid.conf")) {
        my ($owner) = $conf =~ m{/([^/]+)/qemu-server/\d+\.conf$};
        next if !defined($owner) || $owner eq $node;
        open(my $fh, '<', $conf) or next;
        while (my $line = <$fh>) {
            # Snapshot sections follow the current config; their lock lines,
            # if any, describe the past.
            last if $line =~ m/^\[/;
            if ($line =~ m/^lock:\s*migrate\s*$/) {
                close($fh);
                return $owner;
            }
        }
        close($fh);
    }
    return undef;
}

# Dies unless $peer holding $resname Primary is explained by a live migration
# of $vmid to $node.
sub assert_live_migration {
    my ($vmid, $node, $peer, $volname, $resname) = @_;
    return if defined live_migration_source($vmid, $node);

    die "haify volume '$volname' is still Primary on $peer, and no live migration of "
        . "VM $vmid to $node is in progress. Something on $peer still has it open, or "
        . "an earlier deactivate failed there. Refusing to allow a second writer: stop "
        . "whatever uses it on $peer, or demote it with "
        . "'haify resource secondary $resname $peer', then try again.\n";
}

1;
