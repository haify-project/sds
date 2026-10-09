#!/usr/bin/perl
# Allocation, listing, capacity and resize: what the plugin asks sds for.

use strict;
use warnings;
no warnings 'once';    # the plugin's test seams are set, not read, from here

use FindBin;
use lib "$FindBin::Bin/lib", "$FindBin::Bin/..";

use PVEStub;
use MockClient;
use JSON::PP ();
use Test::More tests => 38;

require "$FindBin::Bin/../SDSPlugin.pm";
my $P = 'PVE::Storage::Custom::SDSPlugin';

my $mock;
sub with_mock {
    my (%args) = @_;
    $mock = MockClient->new(%args);
    $PVE::Storage::Custom::SDSPlugin::CLIENT_FACTORY = sub { return $mock };
    return $mock;
}

my $base_scfg = { controller => '10.0.0.1', sdspool => 'vg0' };

# --- alloc_image ------------------------------------------------------------

with_mock();
my $name = $P->alloc_image('sds0', { %$base_scfg, sdsnodes => 'n1, n2' }, 100, 'raw', undef, 20971520);
is($name, 'vm-100-disk-0', 'allocates the first free disk name');

my ($create) = $mock->calls_for('POST', '/v1/resources');
ok($create, 'a resource was created');
is($create->{payload}{name}, 'pve-100-0', 'resource name derived from the volume');
is($create->{payload}{sizeGb}, 20, '20 GiB requested in KiB becomes 20 GB');
is($create->{payload}{sizeBytes}, 20971520 * 1024, 'and the device is exactly that by default');
is($create->{payload}{pool}, 'vg0', 'pool from storage.cfg');
is_deeply($create->{payload}{nodes}, [ 'n1', 'n2' ], 'explicit node list is split and trimmed');
ok(!exists $create->{payload}{replicas}, 'replicas is not sent when nodes are explicit');
is_deeply($create->{payload}{labels}, { 'sds.pve/managed-by' => 'pve' },
    'the resource is labelled as a PVE disk, so sds keeps promoters off it');

# Without an explicit node list, sds auto-places the requested replica count.
with_mock();
$P->alloc_image('sds0', { %$base_scfg, replicas => 3 }, 101, 'raw', 'vm-101-disk-0', 1048576);
($create) = $mock->calls_for('POST', '/v1/resources');
is($create->{payload}{replicas}, 3, 'replica count drives auto-placement');
ok(!exists $create->{payload}{nodes}, 'no node list is sent when auto-placing');

with_mock();
eval { $P->alloc_image('sds0', $base_scfg, 100, 'qcow2', 'vm-100-disk-0', 1048576) };
like($@, qr/unsupported format/, 'qcow2 is refused: DRBD exports a raw device');

with_mock();
eval { $P->alloc_image('sds0', $base_scfg, 100, 'raw', 'vm-999-disk-0', 1048576) };
like($@, qr/illegal name/, 'a disk name from another vmid is refused');

# Not every volume PVE allocates is a disk: cloud-init drives, snapshot RAM
# state and backup fleecing images have names of their own.
with_mock();
is($P->alloc_image('sds0', $base_scfg, 100, 'raw', 'vm-100-cloudinit', 4096), 'vm-100-cloudinit',
    'a cloud-init drive can be allocated');
($create) = $mock->calls_for('POST', '/v1/resources');
is($create->{payload}{name}, 'pve-100-cloudinit', 'and maps to its own resource');

with_mock();
eval { $P->alloc_image('sds0', $base_scfg, 100, 'raw', 'vm-100-state-two words', 4096) };
like($@, qr/illegal name/, 'a name that is not resource-safe is refused');

# --- free_image -------------------------------------------------------------

with_mock();
$P->free_image('sds0', $base_scfg, 'vm-100-disk-0', 0);
ok($mock->called('DELETE', '/v1/resources/pve-100-0'), 'delete targets the mapped resource');

# --- list_images ------------------------------------------------------------

my $resources = {
    resources => [
        { name => 'pve-100-0', volumes => [ { volumeId => 0, sizeGb => 20 } ] },
        { name => 'pve-100-1', volumes => [ { volumeId => 0, sizeGb => 5 } ] },
        { name => 'pve-101-0', volumes => [ { volumeId => 0, sizeGb => 8 } ] },
        # Not ours: a hand-made sds resource sharing the cluster.
        { name => 'mysqlha',   volumes => [ { volumeId => 0, sizeGb => 50 } ] },
    ],
};

with_mock(routes => { 'GET /v1/resources' => $resources });
my $all = $P->list_images('sds0', $base_scfg);
is(scalar(@$all), 3, 'only resources matching the prefix are listed');
is_deeply([ sort map { $_->{volid} } @$all ],
    [ 'sds0:vm-100-disk-0', 'sds0:vm-100-disk-1', 'sds0:vm-101-disk-0' ],
    'volids are reconstructed from resource names');
is($all->[0]{size}, 20 * 1073741824, 'size is reported in bytes');

with_mock(routes => { 'GET /v1/resources' => $resources });
my $for_vm = $P->list_images('sds0', $base_scfg, 100);
is(scalar(@$for_vm), 2, 'filtered by vmid');

with_mock(routes => { 'GET /v1/resources' => $resources });
my $picked = $P->list_images('sds0', $base_scfg, undef, [ 'sds0:vm-101-disk-0' ]);
is_deeply([ map { $_->{volid} } @$picked ], [ 'sds0:vm-101-disk-0' ], 'filtered by vollist');

# protojson renders uint64 as a JSON STRING (a 64-bit int is not safe as a JSON
# number), so sizes really arrive as "2", not 2. Verified against a live
# controller. Treating that as 0 would report every disk as empty.
with_mock(routes => {
    'GET /v1/resources' => {
        resources => [ { name => 'pve-100-0', volumes => [ { sizeGb => "20" } ] } ],
    },
});
my $stringy = $P->list_images('sds0', $base_scfg);
is($stringy->[0]{size}, 20 * 1073741824, 'string-typed sizes from protojson are handled');

# --- status -----------------------------------------------------------------
#
# The pool exists on every node and a replica must fit on ALL of them, so the
# usable capacity is the smallest node's. Summing would let PVE accept a disk
# that cannot actually be placed.

with_mock(routes => {
    'GET /v1/pools' => {
        pools => [
            { name => 'sds_vg0', node => 'n1', totalGb => 100, freeGb => 60 },
            { name => 'sds_vg0', node => 'n2', totalGb => 100, freeGb => 10 },
            { name => 'other', node => 'n1', totalGb => 999, freeGb => 999 },
        ],
    },
});
my ($total, $avail, $used, $active) = $P->status('sds0', $base_scfg);
is($avail, 10 * 1073741824, 'free space is the tightest node, not the sum');
is($total, 100 * 1073741824, 'total is a single node worth of capacity');
is($active, 1, 'storage reports active');

# The controller lists pools under their managed name; `sdspool sds_vg0` and
# `sdspool vg0` both mean that pool.
($total) = $P->status('sds0', { %$base_scfg, sdspool => 'sds_vg0' });
is($total, 100 * 1073741824, 'the prefixed pool name matches too');

# On a thin pool the volume group is all thin pool, so the group's free space
# is ~0 however empty the pool is. Capacity must come from the thin pool itself
# (protojson sends uint64 as strings), still bounded by the smallest node.
my $thin_pools = {
    pools => [
        { name => 'sds_vg0', node => 'n1', totalGb => 100, freeGb => 0, thin => JSON::PP::true,
          thinPoolLv => 'thinpool', thinSizeBytes => '' . (95 * 1073741824), thinDataPercent => 20 },
        { name => 'sds_vg0', node => 'n2', totalGb => 100, freeGb => 0, thin => JSON::PP::true,
          thinPoolLv => 'thinpool', thinSizeBytes => '' . (95 * 1073741824), thinDataPercent => 60 },
    ],
};
with_mock(routes => { 'GET /v1/pools' => $thin_pools });
($total, $avail, $used) = $P->status('sds0', $base_scfg);
is($total, 95 * 1073741824, 'a thin pool reports the thin pool size, not the group');
is($avail, 38 * 1073741824, "thin free space is the fullest node's unused data");
is($used, $total - $avail, 'used is total minus free');

# Thick LVs come from the group's free extents, so a storage pinned to them
# keeps the group figures even when the group carries a thin pool.
with_mock(routes => { 'GET /v1/pools' => $thin_pools });
($total, $avail) = $P->status('sds0', { %$base_scfg, storagetype => 'lvm' });
is($avail, 0, "storagetype lvm reports the group's free extents");

# --- resize -----------------------------------------------------------------

my $gib_scfg = { %$base_scfg, exactsize => 0 };
with_mock();
my $newsize = $P->volume_resize($gib_scfg, 'sds0', 'vm-100-disk-0', 32 * 1073741824, 1);
my ($patch) = $mock->calls_for('PATCH', '/v1/resources/pve-100-0/volumes/0');
is($patch->{payload}{sizeGb}, 32, 'resize converts bytes to whole GB');
is($newsize, 32 * 1073741824, 'returns the size actually allocated');

# --- exactsize ----------------------------------------------------------------
#
# A vzdump restore and an online Move Disk both need a target of the source's
# exact size; whole GiB is not it, so exact is the default and `exactsize 0`
# the way out.

with_mock();
$P->alloc_image('sds0', $gib_scfg, 100, 'raw', 'vm-100-disk-0', 10 * 1048576 + 3);
($create) = $mock->calls_for('POST', '/v1/resources');
ok(!exists $create->{payload}{sizeBytes}, 'exactsize 0 rounds up to whole GiB');

my $exact_scfg = { %$base_scfg, exactsize => 1 };
with_mock();
$P->alloc_image('sds0', $exact_scfg, 100, 'raw', 'vm-100-disk-0', 10 * 1048576 + 3);
($create) = $mock->calls_for('POST', '/v1/resources');
is($create->{payload}{sizeBytes}, (10 * 1048576 + 3) * 1024, 'exactsize asks for the size PVE gave, in bytes');

with_mock();
$newsize = $P->volume_resize($exact_scfg, 'sds0', 'vm-100-disk-0', 5 * 1073741824 + 4096, 1);
($patch) = $mock->calls_for('PATCH', '/v1/resources/pve-100-0/volumes/0');
is($patch->{payload}{sizeBytes}, 5 * 1073741824 + 4096, 'an exact resize sends bytes');
is($newsize, 5 * 1073741824 + 4096, 'and returns them');

with_mock(routes => { 'GET /v1/resources/pve-100-0' => {
    resource => { name => 'pve-100-0', volumes => [ { sizeGb => 6, sizeBytes => '' . (5 * 1073741824 + 4096) } ] } } });
my ($exact) = $P->volume_size_info($exact_scfg, 'sds0', 'vm-100-disk-0');
is($exact, 5 * 1073741824 + 4096, 'the exact size is reported, not the GiB it was allocated in');
