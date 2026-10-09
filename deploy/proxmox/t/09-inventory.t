#!/usr/bin/perl
# The sds-* fields the web interface reads, from this node's DRBD view.

use strict;
use warnings;

use FindBin;
use lib "$FindBin::Bin/lib", "$FindBin::Bin/..";

use JSON::PP;
use Test::More tests => 15;

use PVE::Storage::Custom::SDS::Inventory;
my $I = 'PVE::Storage::Custom::SDS::Inventory';

my $info = { name => 'pve-101-0', nodes => [ 'pve-a', 'pve-b' ], disklessNodes => [ 'pve-c' ] };

sub peer {
    my ($name, %o) = @_;
    return { name => $name, 'connection-state' => $o{conn} // 'Connected', 'peer-role' => $o{role} // 'Secondary',
             peer_devices => [ { 'peer-disk-state' => $o{disk} // 'UpToDate', 'replication-state' => $o{rs} // 'Established',
                                 'peer-client' => $o{client} ? JSON::PP::true : JSON::PP::false, 'percent-in-sync' => $o{pct} // 100 } ] };
}
sub state {
    my (%o) = @_;
    return { name => 'pve-101-0', role => $o{role} // 'Primary',
             devices => [ { 'disk-state' => $o{disk} // 'UpToDate', client => JSON::PP::false } ],
             connections => $o{peers} };
}

# On pve-b, running the guest; pve-a holds the other replica, pve-c is the tiebreaker.
my $f = $I->can('fields')->($info, state(peers => [ peer('pve-a'), peer('pve-c', disk => 'Diskless', client => 1) ]), 'pve-b');
is($f->{'sds-replicas'}, 'pve-a,pve-b', 'replicas as the controller placed them');
is($f->{'sds-diskless'}, 'pve-c', 'the node without a replica');
is($f->{'sds-primary'}, 'pve-b', 'Primary is the node running the guest');
is($f->{'sds-state'}, 'ok', 'every replica UpToDate');
is($f->{'sds-disks'}, 'pve-a:UpToDate,pve-b:UpToDate', 'disk state per replica');

# Seen from pve-c (no replica here): the Primary is a peer.
$f = $I->can('fields')->($info, { name => 'pve-101-0', role => 'Secondary', devices => [ { 'disk-state' => 'Diskless', client => JSON::PP::true } ],
    connections => [ peer('pve-a'), peer('pve-b', role => 'Primary') ] }, 'pve-c');
is($f->{'sds-primary'}, 'pve-b', 'a peer that is Primary');
is($f->{'sds-disks'}, 'pve-a:UpToDate,pve-b:UpToDate', 'a diskless node still sees both replicas');

$f = $I->can('fields')->($info, state(peers => [ peer('pve-a', disk => 'Inconsistent', rs => 'SyncSource', pct => 42.5) ]), 'pve-b');
is($f->{'sds-state'}, 'syncing', 'a resync is syncing, not degraded');
is($f->{'sds-detail'}, 'syncing 42% with pve-a', 'with how far it got');

$f = $I->can('fields')->($info, state(peers => [ peer('pve-a', conn => 'Connecting') ]), 'pve-b');
is($f->{'sds-state'}, 'degraded', 'a replica that cannot be reached is degraded');
is($f->{'sds-detail'}, 'pve-a unreachable', 'and named');

$f = $I->can('fields')->($info, state(disk => 'Outdated', role => 'Secondary', peers => [ peer('pve-a') ]), 'pve-b');
is($f->{'sds-detail'}, 'Outdated on pve-b', 'an outdated local replica is named');

$f = $I->can('fields')->($info, undef, 'pve-d');
is($f->{'sds-state'}, 'unknown', 'not up on this node: no live state');
ok(!exists $f->{'sds-primary'}, 'and no guess at the Primary');

local $PVE::Storage::Custom::SDS::Inventory::DRBD_STATUS = sub { return encode_json([ state(peers => []) ]) };
is(ref($I->can('local_states')->()->{'pve-101-0'}), 'HASH', 'local_states indexes resources by name');
