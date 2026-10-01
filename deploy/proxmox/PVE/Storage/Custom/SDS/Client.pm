package PVE::Storage::Custom::SDS::Client;

# REST client for sds-controller's grpc-gateway, used by SDSPlugin.pm.

use strict;
use warnings;

use HTTP::Tiny;
use JSON::PP qw(encode_json decode_json);

use Exporter qw(import);
our @EXPORT_OK = qw(_uri_escape);

my $DEFAULT_REST_PORT = 3375;

sub new {
    my ($class, $scfg) = @_;

    my $controller = $scfg->{controller}
        or die "sds storage: 'controller' is not configured\n";

    my ($host, $port);
    if ($controller =~ m/^\[(.+)\]:(\d+)$/) {          # [::1]:3375
        ($host, $port) = ($1, $2);
    } elsif ($controller =~ m/^([^:]+):(\d+)$/) {      # host:3375
        ($host, $port) = ($1, $2);
    } else {
        ($host, $port) = ($controller, $DEFAULT_REST_PORT);
    }

    my $self = {
        host  => $host,
        port  => $port,
        token => $scfg->{apitoken},
        ua    => HTTP::Tiny->new(timeout => 60, agent => "sds-pve-plugin/$PVE::Storage::Custom::SDSPlugin::VERSION "),
    };

    return bless $self, $class;
}

sub request {
    my ($self, $method, $path, $payload) = @_;

    my $host = $self->{host};
    $host = "[$host]" if $host =~ m/:/;    # bare IPv6
    my $url = "http://$host:$self->{port}$path";

    my $opts = {
        headers => {
            'Content-Type' => 'application/json',
            'Accept'       => 'application/json',
        },
    };
    $opts->{headers}{'Authorization'} = "Bearer $self->{token}"
        if defined($self->{token}) && length($self->{token});
    $opts->{content} = encode_json($payload) if defined $payload;

    my $res = $self->{ua}->request($method, $url, $opts);

    # HTTP::Tiny reports transport failures as status 599 with the reason in the
    # body; surfacing that verbatim is far more useful than "storage offline".
    die "sds controller unreachable at $host:$self->{port}: " . ($res->{content} // 'unknown error') . "\n"
        if ($res->{status} // 0) == 599;

    my $data;
    if (defined($res->{content}) && length($res->{content})) {
        $data = eval { decode_json($res->{content}) };
        if (!defined $data) {
            die "sds controller returned unparsable response for $method $path: $res->{content}\n"
                if $res->{success};
            die "sds controller error ($res->{status}) for $method $path: $res->{content}\n";
        }
    }

    if (!$res->{success}) {
        # grpc-gateway renders gRPC errors as {"code":..,"message":..}.
        my $msg = (ref($data) eq 'HASH' && defined($data->{message})) ? $data->{message} : ($res->{reason} // 'request failed');
        die "sds controller error ($res->{status}) for $method $path: $msg\n";
    }

    # The controller answers application-level failures with HTTP 200 and
    # success=false. protojson omits false fields, so a MISSING success key is a
    # failure too — never treat "absent" as "fine".
    if (ref($data) eq 'HASH' && exists($data->{message}) && !$data->{success}) {
        die "sds: $data->{message}\n";
    }

    return $data // {};
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
