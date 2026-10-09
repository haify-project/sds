package PVE::Storage::Custom::SDS::Inventory;

# What PVE's web interface shows about a disk on sds: the nodes holding a replica,
# the node running the guest, and whether the replicas are in step.
#
# list_images adds these fields to each volume it returns, and PVE's storage
# content API passes them to the browser unchanged; the web interface's Haify
# views (gui/sds-storage.js) read them from there. The live state comes from
# this node's own DRBD (one `drbdsetup status --json`, a few milliseconds), so
# no controller call is made per disk. A resource that is not up on this node
# reports the placement the controller recorded and no live state.

use strict;
use warnings;

use JSON::PP;

# Test seam: runs `drbdsetup status --json` and returns its output, or undef.
our $DRBD_STATUS = sub {
    my $pid = open(my $fh, '-|');
    return undef if !defined $pid;
    if ($pid == 0) {
        open(STDERR, '>', '/dev/null');
        exec('drbdsetup', 'status', '--json') or exit 127;
    }
    local $/;
    my $out = <$fh>;
    close($fh);
    return $? == 0 ? $out : undef;
};

# local_states reads every resource up on this node, by name.
sub local_states {
    my $out = eval { $DRBD_STATUS->() };
    return {} if !defined($out) || !length($out);
    my $list = eval { JSON::PP->new->decode($out) };
    return {} if ref($list) ne 'ARRAY';
    return { map { ($_->{name} // '') => $_ } @$list };
}

# fields returns the sds-* fields for one resource: $info is the controller's
# record (nodes, disklessNodes, disklessClients), $state this node's DRBD view
# of it (undef when it is not up here), $self this node's name.
sub fields {
    my ($info, $state, $self) = @_;
    my @replicas   = @{ $info->{nodes} // [] };
    my @diskless = (@{ $info->{disklessNodes} // [] }, @{ $info->{disklessClients} // [] });
    my %f = (
        'sds-resource' => $info->{name},
        'sds-replicas'   => join(',', @replicas),
        'sds-diskless' => join(',', @diskless),
    );
    if (!$state) {
        $f{'sds-state'} = 'unknown';
        return \%f;
    }

    my %disk;          # node -> disk state, for the nodes holding a replica
    my $primary = '';
    my @trouble;
    my $syncing;

    my $local = ($state->{devices} // [])->[0] // {};
    $disk{$self} = $local->{'disk-state'} if !$local->{client};
    $primary = $self if ($state->{role} // '') eq 'Primary';

    for my $c (@{ $state->{connections} // [] }) {
        my $peer = $c->{name} // next;
        my $pd = ($c->{peer_devices} // [])->[0] // {};
        $primary = $peer if ($c->{'peer-role'} // '') eq 'Primary';
        if (($c->{'connection-state'} // '') ne 'Connected') {
            push @trouble, "$peer unreachable" if grep { $_ eq $peer } @replicas;
            next;
        }
        next if $pd->{'peer-client'};
        $disk{$peer} = $pd->{'peer-disk-state'} // 'unknown';
        my $rs = $pd->{'replication-state'} // '';
        if ($rs =~ m/^(Sync|PausedSync|StartingSync|WFBitMap)/) {
            my $pct = $pd->{'percent-in-sync'} // 0;
            $syncing = sprintf('syncing %d%% with %s', $pct, $peer);
        }
    }
    for my $node (@replicas) {
        my $ds = $disk{$node} // next;
        push @trouble, "$ds on $node" if $ds ne 'UpToDate' && !$syncing;
    }

    $f{'sds-primary'} = $primary;
    $f{'sds-disks'} = join(',', map { "$_:" . ($disk{$_} // 'unknown') } @replicas);
    if (@trouble) {
        $f{'sds-state'}  = 'degraded';
        $f{'sds-detail'} = join(', ', @trouble);
    } elsif ($syncing) {
        $f{'sds-state'}  = 'syncing';
        $f{'sds-detail'} = $syncing;
    } else {
        $f{'sds-state'} = 'ok';
    }
    return \%f;
}

1;
