package MockClient;

# A stand-in for the plugin's REST client. It records every request and answers
# from a per-test routing table, so tests assert on the exact sds endpoint and
# payload the plugin produced.

use strict;
use warnings;

sub new {
    my ($class, %args) = @_;
    my $self = {
        calls    => [],
        # routes: "METHOD /path" => hashref response, or coderef, or a
        # PVE-style die string to simulate a controller error.
        routes   => $args{routes} // {},
        fallback => $args{fallback},
    };
    return bless $self, $class;
}

sub request {
    my ($self, $method, $path, $payload) = @_;

    push @{ $self->{calls} }, { method => $method, path => $path, payload => $payload };

    my $key   = "$method $path";
    my $route = $self->{routes}{$key};

    if (!defined $route) {
        return $self->{fallback}->($method, $path, $payload) if $self->{fallback};
        return {};
    }

    return $route->($method, $path, $payload) if ref($route) eq 'CODE';
    die $route if !ref($route);
    return $route;
}

# --- assertions helpers ----------------------------------------------------

sub calls_for {
    my ($self, $method, $path) = @_;
    return grep { $_->{method} eq $method && $_->{path} eq $path } @{ $self->{calls} };
}

sub called {
    my ($self, $method, $path) = @_;
    return scalar($self->calls_for($method, $path)) ? 1 : 0;
}

sub paths {
    my ($self) = @_;
    return map { "$_->{method} $_->{path}" } @{ $self->{calls} };
}

1;
