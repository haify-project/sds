package PVE::Storage::Custom::Haify::Client;

# REST client for haify-controller's grpc-gateway, used by HaifyPlugin.pm.
#
# `controller` in storage.cfg is a comma-separated list of addresses, each
# `host`, `host:port`, `[v6]:port`, optionally prefixed with `https://` (or
# `http://`). With Self-HA only one controller runs at a time, on whichever
# node holds haify-meta, so listing the candidates is what keeps the plugin
# working when the controller moves: an address that refuses the connection is
# skipped for the next. Only a failed connect moves on — a request that
# reached a controller and then timed out is not sent to another one, because
# it may have been carried out.

use strict;
use warnings;

use HTTP::Tiny;
use JSON::PP qw(encode_json decode_json);

use Exporter qw(import);
our @EXPORT_OK = qw(_uri_escape parse_controllers);

my $DEFAULT_REST_PORT = 3375;

# The address that answered last, per controller list, so a pvedaemon worker
# does not knock on a stopped controller before every request.
my %PREFERRED;

# Splits a controller setting into endpoints: { scheme, host, port }.
sub parse_controllers {
    my ($controller) = @_;
    my @out;
    for my $entry (grep { length } map { s/^\s+|\s+$//gr } split(/,/, $controller // '')) {
        my $scheme = 'http';
        if ($entry =~ s{^(https?)://}{}i) {
            $scheme = lc($1);
        }
        $entry =~ s{/+$}{};
        my ($host, $port);
        if ($entry =~ m/^\[(.+)\]:(\d+)$/) {          # [::1]:3375
            ($host, $port) = ($1, $2);
        } elsif ($entry =~ m/^\[(.+)\]$/) {           # [::1]
            ($host, $port) = ($1, $DEFAULT_REST_PORT);
        } elsif ($entry =~ m/^([^:]+):(\d+)$/) {      # host:3375
            ($host, $port) = ($1, $2);
        } else {
            ($host, $port) = ($entry, $DEFAULT_REST_PORT);
        }
        push @out, { scheme => $scheme, host => $host, port => $port };
    }
    return @out;
}

# new builds a client for a storage; $token, when given, is its API token
# (Haify/Token.pm), else the one storage.cfg carries.
sub new {
    my ($class, $scfg, $token) = @_;

    my $controller = $scfg->{controller}
        or die "haify storage: 'controller' is not configured\n";
    my @endpoints = parse_controllers($controller);
    die "haify storage: 'controller' names no address\n" if !@endpoints;

    my %ua_opts = (timeout => 60, agent => "haify-pve-plugin/$PVE::Storage::Custom::HaifyPlugin::VERSION ");
    if (grep { $_->{scheme} eq 'https' } @endpoints) {
        # The controller's certificate is checked, name included, against the
        # system store or the CA bundle storage.cfg names. A token sent to an
        # unverified peer is a token given away.
        $ua_opts{verify_SSL} = 1;
        $ua_opts{SSL_options} = { SSL_ca_file => $scfg->{controllerca} } if $scfg->{controllerca};
    }

    my $self = {
        key       => $controller,
        endpoints => \@endpoints,
        token     => $token // $scfg->{apitoken},
        ua        => HTTP::Tiny->new(%ua_opts),
    };

    return bless $self, $class;
}

sub _url {
    my ($ep, $path) = @_;
    my $host = $ep->{host};
    $host = "[$host]" if $host =~ m/:/;    # bare IPv6
    return "$ep->{scheme}://$host:$ep->{port}$path";
}

sub _label {
    my ($ep) = @_;
    return "$ep->{host}:$ep->{port}";
}

sub request {
    my ($self, $method, $path, $payload) = @_;

    my $opts = {
        headers => {
            'Content-Type' => 'application/json',
            'Accept'       => 'application/json',
        },
    };
    $opts->{headers}{'Authorization'} = "Bearer $self->{token}"
        if defined($self->{token}) && length($self->{token});
    $opts->{content} = encode_json($payload) if defined $payload;

    # Start with the address that answered last, then try the rest in order.
    my @eps   = @{ $self->{endpoints} };
    my $first = $PREFERRED{ $self->{key} } // 0;
    $first = 0 if $first > $#eps;
    my @order = (@eps[$first .. $#eps], @eps[0 .. $first - 1]);

    my ($res, @failed);
    for my $ep (@order) {
        $res = $self->{ua}->request($method, _url($ep, $path), $opts);
        my $refused = ($res->{status} // 0) == 599 && ($res->{content} // '') =~ m/^Could not connect/;
        if ($refused && $ep != $order[-1]) {
            push @failed, _label($ep) . ': ' . _reason($res);
            next;
        }
        # HTTP::Tiny reports transport failures as status 599 with the reason
        # in the body; surfacing that verbatim is far more useful than
        # "storage offline".
        if (($res->{status} // 0) == 599) {
            push @failed, _label($ep) . ': ' . _reason($res);
            die "haify controller unreachable at " . join('; ', @failed) . "\n";
        }
        for my $i (0 .. $#eps) {
            $PREFERRED{ $self->{key} } = $i if $eps[$i] == $ep;
        }
        last;
    }

    my $data;
    if (defined($res->{content}) && length($res->{content})) {
        $data = eval { decode_json($res->{content}) };
        if (!defined $data) {
            die "haify controller returned unparsable response for $method $path: $res->{content}\n"
                if $res->{success};
            die "haify controller error ($res->{status}) for $method $path: $res->{content}\n";
        }
    }

    if (!$res->{success}) {
        # grpc-gateway renders gRPC errors as {"code":..,"message":..}.
        my $msg = (ref($data) eq 'HASH' && defined($data->{message})) ? $data->{message} : ($res->{reason} // 'request failed');
        die "haify controller error ($res->{status}) for $method $path: $msg\n";
    }

    # The controller answers application-level failures with HTTP 200 and
    # success=false. protojson omits false fields, so a MISSING success key is a
    # failure too — never treat "absent" as "fine".
    if (ref($data) eq 'HASH' && exists($data->{message}) && !$data->{success}) {
        die "haify: $data->{message}\n";
    }

    return $data // {};
}

sub _reason {
    my ($res) = @_;
    my $why = $res->{content} // 'unknown error';
    chomp($why);
    return $why;
}

# Percent-escape a value for use in a query string. Node names are tame, but a
# silently mangled parameter here would be hard to trace back.
sub _uri_escape {
    my ($value) = @_;
    $value = '' if !defined $value;
    $value =~ s/([^A-Za-z0-9\-\._~])/sprintf("%%%02X", ord($1))/ge;
    return $value;
}

1;
