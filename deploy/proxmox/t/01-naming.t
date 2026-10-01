#!/usr/bin/perl
# Naming, size conversion and API-version handling — the pure logic that decides
# what sds ever sees.

use strict;
use warnings;

use FindBin;
use lib "$FindBin::Bin/lib", "$FindBin::Bin/..";

use PVEStub;
use Test::More tests => 22;

require "$FindBin::Bin/../SDSPlugin.pm";
my $P = 'PVE::Storage::Custom::SDSPlugin';

my $scfg = { controller => '10.0.0.1' };

# --- volume name <-> resource name -----------------------------------------

is(PVE::Storage::Custom::SDSPlugin::sds_resource_name($scfg, 'vm-100-disk-0'), 'pve-100-0', 'default prefix');
is(PVE::Storage::Custom::SDSPlugin::sds_resource_name({ %$scfg, resourceprefix => 'clusterb' }, 'vm-100-disk-2'),
    'clusterb-100-2', 'custom prefix');

my ($volname, $vmid) = PVE::Storage::Custom::SDSPlugin::volname_from_resource($scfg, 'pve-100-0');
is($volname, 'vm-100-disk-0', 'reverse mapping returns the volume name');
is($vmid, 100, 'reverse mapping returns the owning vmid');

for my $round (qw(vm-100-disk-0 vm-999999-disk-15)) {
    my $res = PVE::Storage::Custom::SDSPlugin::sds_resource_name($scfg, $round);
    my ($back) = PVE::Storage::Custom::SDSPlugin::volname_from_resource($scfg, $res);
    is($back, $round, "round trip $round");
}

# Resources belonging to another storage / another PVE cluster must not be
# claimed — list_images relies on this to filter.
ok(!defined((PVE::Storage::Custom::SDSPlugin::volname_from_resource($scfg, 'data'))[0]),
    'unrelated resource is not claimed');
ok(!defined((PVE::Storage::Custom::SDSPlugin::volname_from_resource($scfg, 'clusterb-100-0'))[0]),
    'another prefix is not claimed');
ok(!defined((PVE::Storage::Custom::SDSPlugin::volname_from_resource($scfg, 'pve-100-0-extra'))[0]),
    'near-miss name is not claimed');

eval { PVE::Storage::Custom::SDSPlugin::sds_resource_name($scfg, 'some-random-volume') };
like($@, qr/unable to map/, 'unmappable volume name dies');

# --- parse_volname ----------------------------------------------------------

my @parsed = $P->parse_volname('vm-100-disk-0');
is($parsed[0], 'images', 'vtype');
is($parsed[2], 100, 'vmid');
is($parsed[6], 'raw', 'format is always raw');

eval { $P->parse_volname('base-100-disk-0') };
like($@, qr/unable to parse/, 'base images are rejected');

# --- size rounding ----------------------------------------------------------
#
# Rounding must always go UP: giving a guest less space than its config records
# corrupts it, while a little slack is harmless.

is(PVE::Storage::Custom::SDSPlugin::kib_to_gb(1048576), 1, 'exactly 1 GiB stays 1');
is(PVE::Storage::Custom::SDSPlugin::kib_to_gb(1048577), 2, '1 GiB + 1 KiB rounds up');
is(PVE::Storage::Custom::SDSPlugin::kib_to_gb(1), 1, 'a tiny disk still gets 1 GB');
is(PVE::Storage::Custom::SDSPlugin::kib_to_gb(0), 1, 'zero never becomes a 0 GB volume');
is(PVE::Storage::Custom::SDSPlugin::bytes_to_gb(1073741824), 1, 'exactly 1 GiB in bytes');
is(PVE::Storage::Custom::SDSPlugin::bytes_to_gb(1073741825), 2, 'one byte over rounds up');
is(PVE::Storage::Custom::SDSPlugin::gb_to_bytes(3), 3221225472, 'gb to bytes');

# --- API version negotiation ------------------------------------------------
# PVE::Storage stub reports APIVER 11 / APIAGE 2, so 11 is inside the window.

is($P->api(), 11, 'declares the version it was built against when supported');
