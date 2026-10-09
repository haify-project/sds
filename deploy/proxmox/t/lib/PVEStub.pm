package PVEStub;

# Minimal stand-ins for the Proxmox VE modules HaifyPlugin.pm loads, so the plugin
# can be unit-tested on any machine with a plain Perl — no PVE installation.
#
# Only what the plugin actually touches is stubbed: the storage plugin base
# class, the API version constants, and the node name. Everything else the
# plugin does is its own logic, which is exactly what these tests cover.

use strict;
use warnings;

our $NODENAME = 'pve-test';

BEGIN {
    $INC{'PVE/INotify.pm'}         = __FILE__;
    $INC{'PVE/Storage/Plugin.pm'}  = __FILE__;
    $INC{'PVE/Storage.pm'}         = __FILE__;
}

{
    package PVE::INotify;
    sub nodename { return $PVEStub::NODENAME; }
}

{
    package PVE::Storage;
    use constant APIVER => 11;
    use constant APIAGE => 2;
}

{
    package PVE::Storage::Plugin;

    sub find_free_diskname {
        my ($class, $storeid, $scfg, $vmid, $fmt, $add_fmt_suffix) = @_;
        my $images = $class->list_images($storeid, $scfg, $vmid);
        my $used   = {};
        for my $img (@$images) {
            $used->{$1} = 1 if $img->{volid} =~ m/vm-\Q$vmid\E-disk-(\d+)$/;
        }
        my $idx = 0;
        $idx++ while $used->{$idx};
        return "vm-$vmid-disk-$idx";
    }
}

1;
