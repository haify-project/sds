#!/usr/bin/perl
# What happens when the controller is out of reach, or a host is down: a VM
# must still start and stop on a node that already has its volume up, a
# migration must not need every host that ever ran the guest, and a host must
# not stay attached to every resource it ever ran.

use strict;
use warnings;
no warnings 'once';

use FindBin;
use lib "$FindBin::Bin/lib", "$FindBin::Bin/..";

use PVEStub;
use MockClient;
use File::Path qw(make_path);
use File::Temp qw(tempdir);
use Test::More tests => 31;

require "$FindBin::Bin/../HaifyPlugin.pm";
my $P = 'PVE::Storage::Custom::HaifyPlugin';

$PVE::Storage::Custom::HaifyPlugin::DEVICE_WAIT_SECONDS = 0;
my $device_up = 1;
$PVE::Storage::Custom::HaifyPlugin::DEVICE_CHECK = sub { return $device_up };

my $scfg = { controller => '10.0.0.1', haifypool => 'vg0' };
my $mock;
my $UNREACHABLE = "haify controller unreachable at 10.0.0.1:3375: Connection refused\n";

sub setup {
    my (%routes) = @_;
    $mock = MockClient->new(routes => {
        'GET /v1/resources/pve-100-0' => { resource => {
            name => 'pve-100-0', nodes => [ 'n1', 'n2' ], disklessClients => [ 'pve1', 'pve2' ],
            volumes => [ { volumeId => 0, device => '/dev/drbd1000' } ] } },
        'GET /v1/resources/pve-100-0/status' => { status => { nodeStates => {} } },
        %routes,
    });
    $PVE::Storage::Custom::HaifyPlugin::CLIENT_FACTORY = sub { return $mock };
}

my @ran;
my %answers;
$PVE::Storage::Custom::Haify::Activation::RUN = sub {
    push @ran, join(' ', @_);
    my $a = $answers{ join(' ', @_) } // [ 0, '' ];
    return @$a;
};

# --- the migration window needs the two nodes of the migration only ----------

my $pve_nodes = tempdir(CLEANUP => 1);
$PVE::Storage::Custom::Haify::Migration::NODES_DIR = $pve_nodes;
make_path("$pve_nodes/pve1/qemu-server");
open(my $fh, '>', "$pve_nodes/pve1/qemu-server/100.conf") or die $!;
print $fh "lock: migrate\n";
close($fh);

$PVEStub::NODENAME = 'pve2';
setup('GET /v1/resources/pve-100-0/status' => { status => { nodeStates => { pve1 => { role => 'Primary' } } } });
$P->activate_volume('haify0', $scfg, 'vm-100-disk-0');
my ($open) = $mock->calls_for('POST', '/v1/resources/pve-100-0/dual-primary');
is_deeply($open->{payload}{nodes}, [ 'pve1', 'pve2' ],
    'the window is opened on the source and the target, not on every host attached');

# --- deactivate detaches a diskless client --------------------------------------

$PVEStub::NODENAME = 'pve1';
setup();
$P->deactivate_volume('haify0', $scfg, 'vm-100-disk-0');
ok($mock->called('DELETE', '/v1/resources/pve-100-0/diskless-clients/pve1'),
    'a host that is only a diskless client is detached once the guest stops');

$PVEStub::NODENAME = 'n1';
setup();
$P->deactivate_volume('haify0', $scfg, 'vm-100-disk-0');
ok(!$mock->called('DELETE', '/v1/resources/pve-100-0/diskless-clients/n1'), 'a replica is never detached');

$PVEStub::NODENAME = 'pve1';
setup('DELETE /v1/resources/pve-100-0/diskless-clients/pve1' => "haify: detach failed\n");
my $warned = '';
{
    local $SIG{__WARN__} = sub { $warned .= $_[0] };
    ok(eval { $P->deactivate_volume('haify0', $scfg, 'vm-100-disk-0'); 1 }, 'a failed detach does not fail the deactivate');
}
like($warned, qr/stays attached/, 'it is reported');

# --- activate without the controller -----------------------------------------

$PVEStub::NODENAME = 'pve1';
setup('GET /v1/resources/pve-100-0' => $UNREACHABLE);
@ran = ();
%answers = ('drbdsetup status pve-100-0' => [ 0, "pve-100-0 role:Secondary\n  disk:Diskless\n  n1 role:Secondary\n    peer-disk:UpToDate\n" ]);
{
    local $SIG{__WARN__} = sub {};
    ok(eval { $P->activate_volume('haify0', $scfg, 'vm-100-disk-0'); 1 }, 'a volume up on this node starts without the controller');
}
is_deeply(\@ran, [ 'drbdsetup status pve-100-0', 'drbdadm primary pve-100-0' ], 'plain drbdadm primary, never --force');
ok(!$mock->called('POST', '/v1/resources/pve-100-0/primary'), 'nothing more is asked of an unreachable controller');

my ($path) = $P->filesystem_path($scfg, 'vm-100-disk-0');
like($path, qr{^/dev/drbd}, 'the path resolves locally too, so qemu can be started');

@ran = ();
%answers = ('drbdsetup status pve-100-0' => [ 0, "pve-100-0 role:Secondary\n  disk:Diskless\n  n1 role:Primary\n    peer-disk:UpToDate\n" ]);
eval { $P->activate_volume('haify0', $scfg, 'vm-100-disk-0') };
like($@, qr/n1 holds pve-100-0 Primary.*needs the controller/, 'another Primary is refused: only the controller can tell a migration');
ok(!grep({ /drbdadm primary/ } @ran), 'and nothing is promoted');

@ran = ();
%answers = ('drbdsetup status pve-100-0' => [ 0, "pve-100-0 role:Primary\n  disk:Diskless\n" ]);
ok(eval { $P->activate_volume('haify0', $scfg, 'vm-100-disk-0'); 1 }, 'already Primary here: nothing to do');
ok(!grep({ /drbdadm primary/ } @ran), 'and no promote is issued');

@ran = ();
%answers = (
    'drbdsetup status pve-100-0' => [ 0, "pve-100-0 role:Secondary\n" ],
    'drbdadm primary pve-100-0'  => [ 11, "State change failed: (-2) Need access to UpToDate data\n" ],
);
eval { $P->activate_volume('haify0', $scfg, 'vm-100-disk-0') };
like($@, qr/^haify controller unreachable.*promoting pve-100-0 on this node failed too: State change failed/s,
    'DRBD refusing is reported with both causes');

$device_up = 0;
eval { $P->activate_volume('haify0', $scfg, 'vm-100-disk-0') };
like($@, qr/not up on this node, so it cannot be activated without the controller/, 'a volume not up here needs the controller');
eval { $P->filesystem_path($scfg, 'vm-100-disk-0') };
like($@, qr/^haify controller unreachable/, 'and so does its path');
$device_up = 1;

setup('GET /v1/resources/pve-100-0' => "haify: resource not found\n");
eval { $P->activate_volume('haify0', $scfg, 'vm-100-disk-0') };
like($@, qr/not found/, 'a controller that answers and refuses is not bypassed');

# --- deactivate without the controller ---------------------------------------

setup('POST /v1/resources/pve-100-0/secondary' => $UNREACHABLE);
@ran = ();
%answers = ();
{
    local $SIG{__WARN__} = sub {};
    ok(eval { $P->deactivate_volume('haify0', $scfg, 'vm-100-disk-0'); 1 }, 'a guest stops cleanly without the controller');
}
is_deeply(\@ran, [ 'drbdadm secondary pve-100-0', 'drbdadm net-options --allow-two-primaries=no pve-100-0' ],
    'demoted here, and this side of any window closed — no leftover Primary for the next start to trip on');

@ran = ();
%answers = ('drbdadm secondary pve-100-0' => [ 11, "State change failed: (-12) Device is held open by someone\n" ]);
eval { $P->deactivate_volume('haify0', $scfg, 'vm-100-disk-0') };
like($@, qr/demoting pve-100-0 on this node failed too: State change failed/, 'a refused demote is an error');

# --- the storage stays online without the controller --------------------------
#
# PVE asks check_connection and activate_storage before it activates any
# volume, so the fallback above is only reachable if these let it through.

my $drbd_proc = File::Temp->new;
$PVE::Storage::Custom::Haify::Activation::DRBD_PROC = $drbd_proc->filename;
setup('GET /v1/resources' => $UNREACHABLE);
is($P->check_connection('haify0', $scfg), 1, 'online while DRBD is loaded here, controller or not');
{
    my $warned = '';
    local $SIG{__WARN__} = sub { $warned .= $_[0] };
    ok(eval { $P->activate_storage('haify0', $scfg, {}); 1 }, 'activate_storage lets the guest start');
    like($warned, qr/controller unreachable; only volumes already set up on this node/, 'and says what still works');
}
$PVE::Storage::Custom::Haify::Activation::DRBD_PROC = '/nonexistent/drbd';
is($P->check_connection('haify0', $scfg), 0, 'offline when DRBD is not loaded either');
ok(!eval { $P->activate_storage('haify0', $scfg, {}); 1 }, 'and activate_storage fails');
setup('GET /v1/resources' => "haify controller error (HTTP 403): forbidden\n");
$PVE::Storage::Custom::Haify::Activation::DRBD_PROC = $drbd_proc->filename;
is($P->check_connection('haify0', $scfg), 0, 'a controller that answers and refuses is not papered over');

# PVE reads a disk's size when it starts the guest: a volume up here answers
# from its device.
my $sysfs = tempdir(CLEANUP => 1);
make_path("$sysfs/drbd1000");
open(my $szfh, '>', "$sysfs/drbd1000/size") or die $!;
print $szfh "41943040\n";
close($szfh);
$PVE::Storage::Custom::HaifyPlugin::SYSFS_BLOCK = $sysfs;
$PVE::Storage::Custom::HaifyPlugin::RESOLVE = sub { return '/dev/drbd1000' };
setup('GET /v1/resources/pve-100-0' => $UNREACHABLE);
is(scalar $P->volume_size_info($scfg, 'haify0', 'vm-100-disk-0'), 41943040 * 512, 'size from sysfs without the controller');
$device_up = 0;
ok(!eval { $P->volume_size_info($scfg, 'haify0', 'vm-100-disk-0'); 1 }, 'and an error when the volume is not up here');
$device_up = 1;

# --- new volumes suspend I/O on lost quorum -----------------------------------

setup();
$P->alloc_image('haify0', $scfg, 100, 'raw', 'vm-100-disk-0', 1024 * 1024);
my ($create) = $mock->calls_for('POST', '/v1/resources');
is_deeply($create->{payload}{drbdOptions}, { 'on-no-quorum' => 'suspend-io', 'on-no-data-accessible' => 'suspend-io' },
    'a guest freezes on lost quorum instead of remounting read-only');

setup();
$P->alloc_image('haify0', { %$scfg, onnoquorum => 'io-error' }, 100, 'raw', 'vm-100-disk-0', 1024 * 1024);
($create) = $mock->calls_for('POST', '/v1/resources');
is($create->{payload}{drbdOptions}{'on-no-quorum'}, 'io-error', 'storage.cfg can choose io-error');
is($create->{payload}{drbdOptions}{'on-no-data-accessible'}, 'io-error', 'for both conditions');
