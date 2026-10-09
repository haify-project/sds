package PVE::Storage::Custom::SDS::Api;

# Which PVE storage API version the plugin declares. Pure, so the choice is
# testable without a PVE.

use strict;
use warnings;

use Exporter qw(import);
our @EXPORT_OK = qw(negotiate_apiver);

# The storage API version this plugin was written against.
our $PLUGIN_APIVER = 11;
# The newest storage API version checked against PVE's ApiChangeLog.
our $PLUGIN_APIVER_MAX = 16;

# negotiate_apiver picks the storage API version to declare to a PVE that
# speaks $apiver and still accepts versions down to $apiver - $apiage.
#
# Every version from $PLUGIN_APIVER to $PLUGIN_APIVER_MAX has been checked
# against PVE's ApiChangeLog and is implemented, so the plugin declares PVE's
# own version when it falls in that range: declaring an older one makes PVE
# print "implementing an older storage API" on every command. A PVE newer than
# anything checked gets $PLUGIN_APIVER_MAX, and one whose window excludes the
# whole range the nearest version it accepts, so the plugin still loads and a
# real incompatibility surfaces as a concrete method error rather than the
# storage silently disappearing from the UI.
sub negotiate_apiver {
    my ($apiver, $apiage) = @_;
    my $oldest = $apiver - $apiage;
    my $want   = $apiver < $PLUGIN_APIVER_MAX ? $apiver : $PLUGIN_APIVER_MAX;
    $want = $PLUGIN_APIVER if $want < $PLUGIN_APIVER;
    return $oldest if $want < $oldest;
    return $apiver if $want > $apiver;
    return $want;
}

1;
