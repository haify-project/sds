#!/usr/bin/perl
# Several controller addresses, and HTTPS.
#
# Under Self-HA the controller runs on whichever node holds haify-meta, so a
# single address in storage.cfg went dark every time it moved. And the bearer
# token went over plain HTTP.

use strict;
use warnings;
no warnings 'once';

use FindBin;
use lib "$FindBin::Bin/lib", "$FindBin::Bin/..";

use PVEStub;
use JSON::PP qw(encode_json);
use Test::More tests => 14;

require "$FindBin::Bin/../HaifyPlugin.pm";
my $RC = 'PVE::Storage::Custom::Haify::Client';

{
    package ScriptedUA;
    # Answers by host: a reply hash, or a list of them consumed in turn.
    sub new { my ($class, %by_host) = @_; return bless { by_host => \%by_host, seen => [] }, $class }
    sub request {
        my ($self, $method, $url, $opts) = @_;
        push @{ $self->{seen} }, $url;
        my ($host) = $url =~ m{^https?://([^/]+)};
        my $r = $self->{by_host}{$host};
        return ref($r) eq 'ARRAY' ? shift(@$r) : $r;
    }
}

my $OK      = { status => 200, success => 1, content => encode_json({ success => JSON::PP::true }) };
my $REFUSED = { status => 599, success => 0, content => "Could not connect to 'x': Connection refused\n" };
my $TIMEOUT = { status => 599, success => 0, content => "Timed out while waiting for socket to become ready for reading\n" };

sub client {
    my ($controller, %by_host) = @_;
    my $c = $RC->new({ controller => $controller });
    $c->{ua} = ScriptedUA->new(%by_host);
    return $c;
}

# --- parsing ------------------------------------------------------------------

my @eps = PVE::Storage::Custom::Haify::Client::parse_controllers(
    ' n1 , n2:9999,https://n3, https://[fd00::1]:8443/, [fd00::2]');
is_deeply([ map { "$_->{scheme} $_->{host} $_->{port}" } @eps ],
    [ 'http n1 3375', 'http n2 9999', 'https n3 3375', 'https fd00::1 8443', 'http fd00::2 3375' ],
    'a comma-separated list of addresses, each with an optional scheme and port');
eval { $RC->new({ controller => ' , ' }) };
like($@, qr/names no address/, 'a list with no address in it is a clear error');

# --- failing over --------------------------------------------------------------

my $c = client('n1,n2,n3', 'n1:3375' => $REFUSED, 'n2:3375' => $OK, 'n3:3375' => $OK);
ok(eval { $c->request('GET', '/v1/resources'); 1 }, 'a refused connection moves on to the next address');
is_deeply($c->{ua}{seen}, [ 'http://n1:3375/v1/resources', 'http://n2:3375/v1/resources' ], 'in order');

$c->{ua}{seen} = [];
$c->request('GET', '/v1/resources');
is_deeply($c->{ua}{seen}, [ 'http://n2:3375/v1/resources' ], 'the next request starts with the one that answered');

my $c2 = client('n1,n2,n3', 'n1:3375' => $OK, 'n2:3375' => $OK);
$c2->request('GET', '/v1/resources');
is($c2->{ua}{seen}[0], 'http://n2:3375/v1/resources', 'so does a new client for the same storage');

# A timeout after connecting is not a refusal: the controller may have done
# it, and a POST sent again elsewhere could do it twice.
$c = client('t1,t2', 't1:3375' => $TIMEOUT, 't2:3375' => $OK);
eval { $c->request('POST', '/v1/resources/x/primary', {}) };
like($@, qr/^haify controller unreachable at t1:3375: Timed out/, 'a timeout is reported, not retried');
is(scalar(@{ $c->{ua}{seen} }), 1, 'and t2 is never asked');

$c = client('r1,r2', 'r1:3375' => $REFUSED, 'r2:3375' => $REFUSED);
eval { $c->request('GET', '/v1/resources') };
like($@, qr/^haify controller unreachable at r1:3375: Could not connect.*; r2:3375: Could not connect/,
    'nobody answering names every address tried');

# --- https --------------------------------------------------------------------------

$c = $RC->new({ controller => 'https://ctl.example', controllerca => '/etc/pve/haify-ca.pem' });
ok($c->{ua}{verify_SSL}, "the controller's certificate is verified");
is($c->{ua}{SSL_options}{SSL_ca_file}, '/etc/pve/haify-ca.pem', 'against the configured CA');
$c->{ua} = ScriptedUA->new('ctl.example:3375' => $OK);
$c->request('GET', '/v1/resources');
is($c->{ua}{seen}[0], 'https://ctl.example:3375/v1/resources', 'and spoken to over https');

$c = $RC->new({ controller => 'https://ctl.example' });
ok($c->{ua}{verify_SSL}, 'verification stays on without a CA file: the system store is used');
ok(!exists $c->{ua}{SSL_options}{SSL_ca_file}, 'with no CA file forced on it');
