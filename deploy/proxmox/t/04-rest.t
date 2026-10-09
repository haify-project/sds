#!/usr/bin/perl
# The REST client's error semantics, and the snapshot endpoints.
#
# The controller answers application-level failures with HTTP 200 and
# success=false, and protojson OMITS false fields — so a missing success key is
# a failure. Getting this wrong would make every failed operation look like it
# worked, which is the worst possible failure mode for a storage plugin.

use strict;
use warnings;
no warnings 'once';

use FindBin;
use lib "$FindBin::Bin/lib", "$FindBin::Bin/..";

use PVEStub;
use MockClient;
use JSON::PP qw(encode_json);
use Test::More tests => 24;

require "$FindBin::Bin/../HaifyPlugin.pm";
my $P  = 'PVE::Storage::Custom::HaifyPlugin';
my $RC = 'PVE::Storage::Custom::Haify::Client';

# --- a fake HTTP::Tiny ------------------------------------------------------

{
    package FakeUA;
    sub new { my ($class, $reply) = @_; return bless { reply => $reply, seen => [] }, $class }
    sub request {
        my ($self, $method, $url, $opts) = @_;
        push @{ $self->{seen} }, { method => $method, url => $url, opts => $opts };
        return $self->{reply};
    }
}

sub client_with {
    my ($scfg, $reply) = @_;
    my $c = $RC->new($scfg);
    $c->{ua} = FakeUA->new($reply);
    return $c;
}

# --- endpoint construction --------------------------------------------------

my $c = client_with({ controller => '10.0.0.1' },
    { status => 200, success => 1, content => encode_json({ success => JSON::PP::true }) });
$c->request('GET', '/v1/resources');
is($c->{ua}{seen}[0]{url}, 'http://10.0.0.1:3375/v1/resources', 'REST port defaults to 3375');

$c = client_with({ controller => '10.0.0.1:9999' },
    { status => 200, success => 1, content => encode_json({ success => JSON::PP::true }) });
$c->request('GET', '/v1/resources');
is($c->{ua}{seen}[0]{url}, 'http://10.0.0.1:9999/v1/resources', 'an explicit port is honoured');

$c = client_with({ controller => 'fd00::1' },
    { status => 200, success => 1, content => encode_json({ success => JSON::PP::true }) });
$c->request('GET', '/v1/resources');
is($c->{ua}{seen}[0]{url}, 'http://[fd00::1]:3375/v1/resources', 'bare IPv6 is bracketed');

$c = client_with({ controller => '[fd00::1]:9999' },
    { status => 200, success => 1, content => encode_json({ success => JSON::PP::true }) });
$c->request('GET', '/v1/resources');
is($c->{ua}{seen}[0]{url}, 'http://[fd00::1]:9999/v1/resources', 'bracketed IPv6 with port');

eval { $RC->new({}) };
like($@, qr/'controller' is not configured/, 'a missing controller is a clear error');

# --- auth -------------------------------------------------------------------

$c = client_with({ controller => 'c', apitoken => 'secret-token' },
    { status => 200, success => 1, content => encode_json({ success => JSON::PP::true }) });
$c->request('GET', '/v1/resources');
is($c->{ua}{seen}[0]{opts}{headers}{Authorization}, 'Bearer secret-token', 'the token is sent');

$c = client_with({ controller => 'c' },
    { status => 200, success => 1, content => encode_json({ success => JSON::PP::true }) });
$c->request('GET', '/v1/resources');
ok(!exists $c->{ua}{seen}[0]{opts}{headers}{Authorization}, 'no token, no header');

# --- payload ----------------------------------------------------------------

$c = client_with({ controller => 'c' },
    { status => 200, success => 1, content => encode_json({ success => JSON::PP::true }) });
$c->request('POST', '/v1/resources', { name => 'pve-100-0' });
like($c->{ua}{seen}[0]{opts}{content}, qr/"name":"pve-100-0"/, 'the payload is JSON-encoded');
is($c->{ua}{seen}[0]{opts}{headers}{'Content-Type'}, 'application/json', 'content type is set');

# --- error semantics --------------------------------------------------------

# HTTP 200 + success omitted (protojson drops false) = the operation FAILED.
$c = client_with({ controller => 'c' },
    { status => 200, success => 1, content => encode_json({ message => 'pool vg0 not found on node n1' }) });
eval { $c->request('POST', '/v1/resources', {}) };
like($@, qr/pool vg0 not found/, 'HTTP 200 with success omitted is treated as a failure');

# HTTP 200 + explicit success=false.
$c = client_with({ controller => 'c' },
    { status => 200, success => 1,
      content => encode_json({ success => JSON::PP::false, message => 'resource exists' }) });
eval { $c->request('POST', '/v1/resources', {}) };
like($@, qr/resource exists/, 'explicit success=false is a failure');

# A genuine success must NOT be mistaken for a failure.
$c = client_with({ controller => 'c' },
    { status => 200, success => 1,
      content => encode_json({ success => JSON::PP::true, message => 'Resource created' }) });
my $out = $c->request('POST', '/v1/resources', {});
is($out->{message}, 'Resource created', 'a successful call returns its data');

# grpc-gateway renders gRPC errors as a non-2xx with {"code","message"}.
$c = client_with({ controller => 'c' },
    { status => 404, success => 0, reason => 'Not Found',
      content => encode_json({ code => 5, message => 'resource not found' }) });
eval { $c->request('GET', '/v1/resources/nope') };
like($@, qr/resource not found/, 'a gateway error surfaces the controller message');

# HTTP::Tiny reports transport failure as status 599.
$c = client_with({ controller => 'node1' },
    { status => 599, success => 0, content => "Connection refused" });
eval { $c->request('GET', '/v1/resources') };
like($@, qr/unreachable at node1:3375/, 'an unreachable controller says so, with the address');

# --- snapshots --------------------------------------------------------------
#
# The snapshot API keys on the backing "<pool>/<lv>" path, not the DRBD resource
# name, so the plugin has to look it up first.

my $info = {
    resource => {
        name    => 'pve-100-0',
        nodes   => [ 'n1', 'n2' ],
        volumes => [ { volumeId => 0, device => '/dev/drbd1000', pool => 'vg0',
                       backingVolume => 'pve-100-0_00' } ],
    },
};
my $status = { status => { nodeStates => { n1 => { role => 'Secondary' }, n2 => { role => 'Primary' } } } };

my $mock = MockClient->new(routes => {
    'GET /v1/resources/pve-100-0'        => $info,
    'GET /v1/resources/pve-100-0/status' => $status,
});
$PVE::Storage::Custom::HaifyPlugin::CLIENT_FACTORY = sub { return $mock };

my $scfg = { controller => 'c' };
$PVEStub::NODENAME = 'pve1';    # a compute-only hypervisor: holds no replica

# A snapshot is a resource snapshot, on every replica at once, so the
# hypervisor — usually holding no replica — never needs to be the one it runs on.
$P->volume_snapshot($scfg, 'haify0', 'vm-100-disk-0', 'before-upgrade');
my ($snap_call) = $mock->calls_for('POST', '/v1/resources/pve-100-0/snapshots');
ok($snap_call, 'snapshot is a resource snapshot');
is($snap_call->{payload}{name}, 'before-upgrade', 'named as PVE named it');

# One taken this way is rolled back and deleted the same way.
my $mock2 = MockClient->new(routes => {
    'GET /v1/resources/pve-100-0/snapshots' => { names => [ 'new-style' ] },
});
$PVE::Storage::Custom::HaifyPlugin::CLIENT_FACTORY = sub { return $mock2 };
$P->volume_snapshot_rollback($scfg, 'haify0', 'vm-100-disk-0', 'new-style');
ok($mock2->called('POST', '/v1/resources/pve-100-0/snapshots/new-style/rollback'),
    'a resource snapshot rolls back every replica together');

$PVE::Storage::Custom::HaifyPlugin::CLIENT_FACTORY = sub { return $mock };

$P->volume_snapshot_rollback($scfg, 'haify0', 'vm-100-disk-0', 'before-upgrade');
ok($mock->called('POST', '/v1/volumes/vg0/pve-100-0_00/snapshots/before-upgrade/restore'),
    'rollback restores the named snapshot');

# Snapshots from earlier plugin versions are on one replica, under their own
# name, and are rolled back and deleted the old way.
# Found on real hardware: DELETE carries no body, so grpc-gateway can only take
# the node from a QUERY parameter. Without it the controller failed with
# "failed to delete snapshot: []" — it had no host to run on.
$P->volume_snapshot_delete($scfg, 'haify0', 'vm-100-disk-0', 'before-upgrade');
ok($mock->called('DELETE', '/v1/volumes/vg0/pve-100-0_00/snapshots/before-upgrade?node=n2'),
    'snapshot delete passes the node as a query parameter');

is(PVE::Storage::Custom::HaifyPlugin::_uri_escape('node a/b'), 'node%20a%2Fb', 'query values are escaped');

# A resource with no recorded backing volume must fail loudly rather than
# building a nonsense path like "/v1/volumes///snapshots".
$mock = MockClient->new(routes => {
    'GET /v1/resources/pve-100-0' => { resource => { name => 'pve-100-0', volumes => [ { volumeId => 0 } ] } },
});
$PVE::Storage::Custom::HaifyPlugin::CLIENT_FACTORY = sub { return $mock };
eval { $P->volume_snapshot_delete($scfg, 'haify0', 'vm-100-disk-0', 'snap') };
like($@, qr/no backing volume/, 'a missing backing volume is an explicit error');

# Opening a snapshot read-only (vzdump's snapshot mode) works where a replica
# is, and says where to go elsewhere.
$mock = MockClient->new(routes => { 'GET /v1/resources/pve-100-0' => $info });
$PVE::Storage::Custom::HaifyPlugin::CLIENT_FACTORY = sub { return $mock };
eval { $P->path($scfg, 'vm-100-disk-0', 'haify0', 'before-upgrade') };
like($@, qr/on its replica nodes \(n1 n2\), not on pve1/, 'a node without a replica has no snapshot to open');
$PVEStub::NODENAME = 'n1';
is($P->path($scfg, 'vm-100-disk-0', 'haify0', 'before-upgrade'), '/dev/vg0/pve-100-0_00_snap_before-upgrade',
    "a replica node opens its own copy of the snapshot");
my @ran;
local $PVE::Storage::Custom::Haify::Activation::RUN = sub { push @ran, join(' ', @_); return (0, '') };
$P->activate_volume('haify0', $scfg, 'vm-100-disk-0', 'before-upgrade');
is($ran[0], 'lvchange -ay -K vg0/pve-100-0_00_snap_before-upgrade', 'activated despite the skip flag');
$PVEStub::NODENAME = 'pve1';
