package PVE::Storage::Custom::SDS::Token;

# The controller API token of an sds storage.
#
# apitoken is a sensitive property: PVE's storage API (web interface and
# `pvesm add/set` alike) takes it out of the parameters before it writes
# storage.cfg and hands it to the plugin's on_add_hook / on_update_hook to
# keep. Without the hooks it was simply dropped. It is kept the way PBS keeps
# its password: one root-only file per storage under /etc/pve/priv, which
# pmxcfs shares with every node.
#
# A token written into storage.cfg by hand still works and wins.

use strict;
use warnings;

# Test seams: where tokens are kept, and the storage configuration to look a
# storage up in.
our $DIR = '/etc/pve/priv/storage';
our $CONFIG = sub { require PVE::Storage; return PVE::Storage::config() };

sub path {
    my ($storeid) = @_;
    return "$DIR/$storeid.sds-token";
}

sub set {
    my ($storeid, $token) = @_;
    mkdir $DIR if !-d $DIR;
    my $file = path($storeid);
    open(my $fh, '>', "$file.tmp") or die "cannot write $file: $!\n";
    chmod 0600, "$file.tmp";
    print $fh "$token\n";
    close($fh) or die "cannot write $file: $!\n";
    rename("$file.tmp", $file) or die "cannot write $file: $!\n";
}

sub get {
    my ($storeid) = @_;
    open(my $fh, '<', path($storeid)) or return undef;
    my $token = <$fh>;
    close($fh);
    return undef if !defined $token;
    chomp $token;
    return length($token) ? $token : undef;
}

sub on_add {
    my ($storeid, $param) = @_;
    set($storeid, $param->{apitoken}) if defined($param->{apitoken}) && length($param->{apitoken});
    return undef;
}

# A token left out of an update is kept; one given empty, or deleted, goes.
sub on_update {
    my ($storeid, $param) = @_;
    return undef if !exists $param->{apitoken};
    if (defined($param->{apitoken}) && length($param->{apitoken})) {
        set($storeid, $param->{apitoken});
    } else {
        unlink path($storeid);
    }
    return undef;
}

sub on_delete {
    my ($storeid) = @_;
    unlink path($storeid);
    return undef;
}

# token_for finds the token for a storage's configuration. PVE passes the
# plugin a storage's section without its ID, so the ID is looked up: the same
# section first, then the one sds storage with the same controller list (the
# plugin also builds sections of its own, for a clone).
sub token_for {
    my ($scfg) = @_;
    return $scfg->{apitoken} if defined($scfg->{apitoken}) && length($scfg->{apitoken});
    my $ids = eval { $CONFIG->()->{ids} } // {};
    my @same;
    for my $id (sort keys %$ids) {
        my $s = $ids->{$id};
        next if ($s->{type} // '') ne 'sds';
        return get($id) if $s == $scfg;
        push @same, $id if ($s->{controller} // '') eq ($scfg->{controller} // '');
    }
    for my $id (@same) {
        my $token = get($id);
        return $token if defined $token;
    }
    return undef;
}

1;
