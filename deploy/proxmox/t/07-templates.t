#!/usr/bin/perl
# Templates, full-copy clones and reassigning disks (Haify/Templates.pm).

use strict;
use warnings;
no warnings 'once';

use FindBin;
use lib "$FindBin::Bin/lib", "$FindBin::Bin/..";

use PVEStub;
use MockClient;
use Test::More tests => 12;

require "$FindBin::Bin/../HaifyPlugin.pm";
my $P = 'PVE::Storage::Custom::HaifyPlugin';

my $mock;
sub with_mock {
    my (%args) = @_;
    $mock = MockClient->new(%args);
    $PVE::Storage::Custom::HaifyPlugin::CLIENT_FACTORY = sub { return $mock };
    return $mock;
}
my $scfg = { controller => '10.0.0.1', haifypool => 'vg0' };

# A VM disk becomes a template disk by renaming its resource.
with_mock();
is($P->create_base('haify0', $scfg, 'vm-100-disk-1'), 'base-100-disk-1', 'create_base names the template disk');
my ($rename) = $mock->calls_for('POST', '/v1/resources/pve-100-1/rename');
is($rename->{payload}{newName}, 'pve-base-100-1', 'by renaming its resource');

eval { $P->create_base('haify0', $scfg, 'vm-100-cloudinit') };
like($@, qr/only a VM disk/, 'only a disk becomes a template');

# Reassigning a disk to another VM renames it to the target VM's next free name.
with_mock(routes => { 'GET /v1/resources' => { resources => [ { name => 'pve-200-0' } ] } });
is($P->rename_volume($scfg, 'haify0', 'vm-100-disk-0', 200), 'haify0:vm-200-disk-1', 'rename picks the next free disk');
($rename) = $mock->calls_for('POST', '/v1/resources/pve-100-0/rename');
is($rename->{payload}{newName}, 'pve-200-1', 'and renames the resource to it');

eval { $P->rename_volume($scfg, 'haify0', 'vm-100-disk-0', 200, 'vm-300-disk-0') };
like($@, qr/does not belong to VM 200/, 'a target name of another VM is refused');

# A clone is a full copy on the template's replica nodes, at its exact size.
my $base = { name => 'pve-base-100-0', nodes => [ 'n1', 'n2' ],
    volumes => [ { pool => 'haify_vg0', backingVolume => 'pve-base-100-0_data', sizeGb => 3, sizeBytes => '' . (2 * 1073741824 + 512) } ] };
with_mock(routes => {
    'GET /v1/resources/pve-base-100-0' => { resource => $base },
    'GET /v1/resources'                => { resources => [] },
});
is($P->clone_image($scfg, 'haify0', 'base-100-disk-0', 101), 'vm-101-disk-0', 'the clone is a new disk of the target VM');
my ($create) = $mock->calls_for('POST', '/v1/resources');
is_deeply($create->{payload}{nodes}, [ 'n1', 'n2' ], "it lives on the template's nodes");
ok($create->{payload}{sizeBytes} >= 2 * 1073741824 + 512, 'at least the exact size of the template');
my ($pop) = $mock->calls_for('POST', '/v1/resources/pve-101-0/populate');
is($pop->{payload}{sourceDevice}, '/dev/haify_vg0/pve-base-100-0_data', "filled from the template's backing volume");
is($pop->{payload}{node}, 'n1', 'on a node that holds both');

eval { $P->clone_image($scfg, 'haify0', 'vm-100-disk-0', 101) };
like($@, qr/only a template disk/, 'a plain disk is not cloned');
