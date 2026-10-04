#!/usr/bin/perl
# Activation, diskless attach and the dual-primary bracket.
#
# This is the file that matters most: a dual-primary window left open is the one
# way this plugin can corrupt a guest's filesystem, so every path that opens it
# is checked for closing it again.

use strict;
use warnings;
no warnings 'once';    # the plugin's test seams are set, not read, from here

use FindBin;
use lib "$FindBin::Bin/lib", "$FindBin::Bin/..";

use PVEStub;
use MockClient;
use Test::More tests => 26;
use File::Path qw(make_path);
use File::Temp qw(tempdir);

require "$FindBin::Bin/../SDSPlugin.pm";
my $P = 'PVE::Storage::Custom::SDSPlugin';

# The device never exists in a unit test; pretend the promote published it.
$PVE::Storage::Custom::SDSPlugin::DEVICE_CHECK = sub { return 1 };
# Keep the "device never appeared" case from actually waiting 20 seconds.
$PVE::Storage::Custom::SDSPlugin::DEVICE_WAIT_SECONDS = 0;

my $scfg = { controller => '10.0.0.1', sdspool => 'vg0' };
my $mock;

# A stand-in for pmxcfs's /etc/pve/nodes: guest_config('pve1', "lock: migrate")
# says VM 100's config lives on pve1 and is being migrated away from it.
my $pve_nodes = tempdir(CLEANUP => 1);
$PVE::Storage::Custom::SDS::Migration::NODES_DIR = $pve_nodes;
sub guest_config {
    my ($owner, $body) = @_;
    system('rm', '-rf', glob("$pve_nodes/*"));
    return if !defined $owner;
    make_path("$pve_nodes/$owner/qemu-server");
    open(my $fh, '>', "$pve_nodes/$owner/qemu-server/100.conf") or die $!;
    print $fh "boot: order=scsi0\n$body\nscsi0: sds0:vm-100-disk-0,size=20G\n";
    close($fh);
}

# resource_info builds a GET /v1/resources/<name> reply.
sub resource_info {
    my (%args) = @_;
    return {
        resource => {
            name            => 'pve-100-0',
            nodes           => $args{nodes} // [ 'n1', 'n2' ],
            disklessClients => $args{clients} // [],
            volumes         => [ { volumeId => 0, device => '/dev/drbd1000', sizeGb => 20,
                                   pool => 'vg0', backingVolume => 'pve-100-0_00' } ],
        },
    };
}

# resource_status builds a GET /v1/resources/<name>/status reply.
sub resource_status {
    my (%states) = @_;
    return { status => { name => 'pve-100-0', nodeStates => { %states } } };
}

sub setup {
    my (%args) = @_;
    my $routes = {
        'GET /v1/resources/pve-100-0'        => $args{info}   // resource_info(),
        'GET /v1/resources/pve-100-0/status' => $args{status} // resource_status(),
    };
    $routes->{ $_ } = $args{routes}{$_} for keys %{ $args{routes} // {} };

    $mock = MockClient->new(routes => $routes);
    $PVE::Storage::Custom::SDSPlugin::CLIENT_FACTORY = sub { return $mock };
    return $mock;
}

sub dual_primary_calls {
    my @calls = $mock->calls_for('POST', '/v1/resources/pve-100-0/dual-primary');
    return @calls;
}

# --- plain activation on a replica node -------------------------------------

$PVEStub::NODENAME = 'n1';
setup();
$P->activate_volume('sds0', $scfg, 'vm-100-disk-0');

ok(!$mock->called('POST', '/v1/resources/pve-100-0/diskless-clients'),
    'a node that already holds a replica is not attached again');
my ($promote) = $mock->calls_for('POST', '/v1/resources/pve-100-0/primary');
ok($promote, 'the volume is promoted');
is($promote->{payload}{node}, 'n1', 'promoted on this node');
ok($promote->{payload}{quorumGuarded}, 'promote is quorum-guarded, so HA cannot split-brain');
is(scalar(dual_primary_calls()), 0, 'no dual-primary window when no peer is Primary');

# --- activation on a compute-only host --------------------------------------
#
# This is the normal Proxmox topology: the hypervisor stores no replica and
# attaches diskless just to see /dev/drbdN.

$PVEStub::NODENAME = 'pve1';
setup();
$P->activate_volume('sds0', $scfg, 'vm-100-disk-0');

my ($attach) = $mock->calls_for('POST', '/v1/resources/pve-100-0/diskless-clients');
ok($attach, 'a non-replica node attaches as a diskless client');
is($attach->{payload}{node}, 'pve1', 'attaches itself');

# Already attached: no second attach.
setup(info => resource_info(clients => [ 'pve1' ]));
$P->activate_volume('sds0', $scfg, 'vm-100-disk-0');
ok(!$mock->called('POST', '/v1/resources/pve-100-0/diskless-clients'),
    'an already-attached client is not re-attached');

# --- live migration: the dual-primary window --------------------------------

$PVEStub::NODENAME = 'pve2';
guest_config('pve1', 'lock: migrate');
setup(
    info   => resource_info(clients => [ 'pve1', 'pve2' ]),
    status => resource_status(pve1 => { role => 'Primary' }, n1 => { role => 'Secondary' }),
);
$P->activate_volume('sds0', $scfg, 'vm-100-disk-0');

my @dual = dual_primary_calls();
is(scalar(@dual), 1, 'a peer holding Primary opens exactly one dual-primary window');
ok($dual[0]{payload}{enable}, 'the window is opened, not closed');

# Ordering matters: the window must be open BEFORE the promote is attempted,
# otherwise DRBD refuses the second Primary and the migration fails.
my @paths = $mock->paths();
my ($dual_at) = grep { $paths[$_] =~ m{dual-primary} } 0 .. $#paths;
my ($prom_at) = grep { $paths[$_] =~ m{/primary$} } 0 .. $#paths;
ok($dual_at < $prom_at, 'dual-primary is opened before the promote');

# --- the window always closes -----------------------------------------------

$PVEStub::NODENAME = 'pve2';
setup(
    info   => resource_info(clients => [ 'pve1', 'pve2' ]),
    status => resource_status(pve1 => { role => 'Primary' }),
    routes => { 'POST /v1/resources/pve-100-0/primary' => "sds: promote refused: no quorum\n" },
);
eval { $P->activate_volume('sds0', $scfg, 'vm-100-disk-0') };
like($@, qr/promote refused/, 'a failed promote propagates the controller message');

@dual = dual_primary_calls();
is(scalar(@dual), 2, 'the window opened for the migration is closed again');
ok(!$dual[1]{payload}{enable}, 'the second call closes it');

# Same guarantee when the device never shows up.
$PVE::Storage::Custom::SDSPlugin::DEVICE_CHECK = sub { return 0 };
setup(
    info   => resource_info(clients => [ 'pve1', 'pve2' ]),
    status => resource_status(pve1 => { role => 'Primary' }),
);
eval { $P->activate_volume('sds0', $scfg, 'vm-100-disk-0') };
like($@, qr/did not appear/, 'a device that never appears is an error, not a silent success');
@dual = dual_primary_calls();
ok(scalar(@dual) == 2 && !$dual[1]{payload}{enable}, 'the window is closed on device timeout too');
$PVE::Storage::Custom::SDSPlugin::DEVICE_CHECK = sub { return 1 };

# --- a leftover Primary is not a migration ----------------------------------
#
# A deactivate that failed earlier leaves the old node Primary. Starting the
# guest elsewhere must not quietly run it with two writers allowed.

sub leftover_primary_refused {
    my ($why) = @_;
    setup(
        info   => resource_info(clients => [ 'pve1', 'pve2' ]),
        status => resource_status(pve1 => { role => 'Primary' }),
    );
    eval { $P->activate_volume('sds0', $scfg, 'vm-100-disk-0') };
    my $err = $@;
    ok($err =~ /still Primary on pve1/ && !dual_primary_calls()
        && !$mock->called('POST', '/v1/resources/pve-100-0/primary'), $why);
    return $err;
}

guest_config('pve1', '');
my $err = leftover_primary_refused('no migration lock: refused, no window, no promote');
like($err, qr/sds resource secondary pve-100-0 pve1/, 'the error says how to demote the leftover');

guest_config('pve2', 'lock: migrate');
leftover_primary_refused('a config already on this node is not an incoming migration');

guest_config('pve1', "\n[snap1]\nlock: migrate");
leftover_primary_refused('a lock inside a snapshot section does not count');

guest_config(undef);
leftover_primary_refused('no VM config at all (a container): refused');

# --- deactivate -------------------------------------------------------------

$PVEStub::NODENAME = 'pve1';
setup();
$P->deactivate_volume('sds0', $scfg, 'vm-100-disk-0');

my ($demote) = $mock->calls_for('POST', '/v1/resources/pve-100-0/secondary');
ok($demote, 'the volume is demoted');
@dual = dual_primary_calls();
is(scalar(@dual), 1, 'deactivate always closes the window, even if it never opened one');
ok(!$dual[0]{payload}{enable}, 'closing, not opening');

# The migration SOURCE deactivates after hand-off. If its demote fails we must
# still close the window — that is the case this bracket exists for.
setup(routes => { 'POST /v1/resources/pve-100-0/secondary' => "sds: demote failed\n" });
eval { $P->deactivate_volume('sds0', $scfg, 'vm-100-disk-0') };
like($@, qr/demote failed/, 'the demote error is still reported');
@dual = dual_primary_calls();
ok(scalar(@dual) == 1 && !$dual[0]{payload}{enable},
    'the window is closed even when the demote failed');
