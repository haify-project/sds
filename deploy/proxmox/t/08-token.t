#!/usr/bin/perl
# The API token: PVE gives a sensitive property only to the plugin's hooks, so
# the plugin keeps it, and finds it again for a storage it is handed without
# its ID.

use strict;
use warnings;
no warnings 'once';

use FindBin;
use lib "$FindBin::Bin/lib", "$FindBin::Bin/..";

use PVEStub;
use File::Temp qw(tempdir);
use Test::More tests => 12;

require "$FindBin::Bin/../SDSPlugin.pm";
my $P = 'PVE::Storage::Custom::SDSPlugin';
my $T = 'PVE::Storage::Custom::SDS::Token';

my $dir = tempdir(CLEANUP => 1);
$PVE::Storage::Custom::SDS::Token::DIR = "$dir/storage";

my $sds0 = { type => 'sds', controller => '10.0.0.1,10.0.0.2', sdspool => 'vg0' };
my $other = { type => 'sds', controller => '10.9.9.9' };
my $cfg = { ids => { sds0 => $sds0, other => $other, local => { type => 'dir', path => '/var/lib/vz' } } };
$PVE::Storage::Custom::SDS::Token::CONFIG = sub { return $cfg };

$P->on_add_hook('sds0', $sds0, apitoken => 'tok-1');
is($T->can('get')->('sds0'), 'tok-1', 'on_add_hook keeps the token');
is((stat($T->can('path')->('sds0')))[2] & 0777, 0600, 'readable by root only');
is($T->can('token_for')->($sds0), 'tok-1', 'found for the section PVE passes');
is($T->can('token_for')->({ %$sds0 }), 'tok-1', 'and for a copy of it (a clone builds one)');
is($T->can('token_for')->($other), undef, 'another storage does not get it');

$P->on_update_hook('sds0', $sds0);
is($T->can('get')->('sds0'), 'tok-1', 'an update that leaves the token out keeps it');
$P->on_update_hook('sds0', $sds0, apitoken => 'tok-2');
is($T->can('get')->('sds0'), 'tok-2', 'an update with a token replaces it');
$P->on_update_hook('sds0', $sds0, apitoken => undef);
is($T->can('get')->('sds0'), undef, 'deleting it removes the file');

$P->on_add_hook('sds0', $sds0, apitoken => 'tok-3');
is($T->can('token_for')->({ %$sds0, apitoken => 'from-cfg' }), 'from-cfg', 'a token written into storage.cfg by hand wins');

local $PVE::Storage::Custom::SDSPlugin::CLIENT_FACTORY;
my $client = $P->_client($sds0);
is($client->{token}, 'tok-3', 'the REST client sends the kept token');

$P->on_delete_hook('sds0', $sds0);
ok(!-e $T->can('path')->('sds0'), 'on_delete_hook removes it');
is($P->_client($sds0)->{token}, undef, 'and the client then sends none');
