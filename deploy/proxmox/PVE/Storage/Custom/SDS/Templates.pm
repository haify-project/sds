package PVE::Storage::Custom::SDS::Templates;

# Templates, clones and reassigning disks, for SDSPlugin.pm.
#
# A DRBD device has no image-level copy-on-write, so there are no linked
# clones in the qcow2 sense. What PVE needs is still possible:
#
#   - create_base turns a stopped VM's disk into a template disk by renaming
#     its resource from <prefix>-<vmid>-<n> to <prefix>-base-<vmid>-<n>
#     (volume base-<vmid>-disk-<n>); the data stays where it is.
#   - clone_image, which PVE calls for a linked clone, makes a full copy: a
#     new disk on the base's own replica nodes, filled from the base's backing
#     volume on one of them. The clone is independent of the base, which can
#     then be deleted without breaking it.
#   - rename_volume, which PVE calls to reassign a disk to another VM,
#     renames the resource.
#
# The controller renames only a resource nothing else refers to by name:
# not Primary anywhere, without snapshots, HA, gateways, schedules or
# backups (pkg/controller/resource_rename.go). A failed rename says which.

use strict;
use warnings;

use Exporter qw(import);
use PVE::Storage::Custom::SDS::Naming qw(sds_resource_name parse_vm_volname volume_size_bytes);

our @EXPORT_OK = qw(create_base clone_image rename_volume);

sub _rename {
    my ($plugin, $scfg, $from, $to) = @_;
    my $client = $plugin->_client($scfg);
    $client->request('POST', "/v1/resources/$from/rename", { name => $from, newName => $to });
}

sub create_base {
    my ($plugin, $storeid, $scfg, $volname) = @_;
    my ($vmid, $idx) = parse_vm_volname($volname);
    die "only a VM disk (vm-<vmid>-disk-<n>) can become a template disk, not '$volname'\n"
        if !defined($vmid) || !defined($idx);
    my $base = "base-$vmid-disk-$idx";
    _rename($plugin, $scfg, sds_resource_name($scfg, $volname), sds_resource_name($scfg, $base));
    return $base;
}

sub rename_volume {
    my ($plugin, $scfg, $storeid, $source_volname, $target_vmid, $target_volname) = @_;
    my ($src_vmid) = parse_vm_volname($source_volname);
    die "cannot rename '$source_volname'\n" if !defined $src_vmid;
    $target_volname //= $plugin->find_free_diskname($storeid, $scfg, $target_vmid, 'raw');
    my ($dst_vmid) = parse_vm_volname($target_volname);
    die "target '$target_volname' does not belong to VM $target_vmid\n"
        if !defined($dst_vmid) || $dst_vmid ne $target_vmid;
    _rename($plugin, $scfg, sds_resource_name($scfg, $source_volname), sds_resource_name($scfg, $target_volname));
    return "$storeid:$target_volname";
}

sub clone_image {
    my ($plugin, $scfg, $storeid, $volname, $vmid, $snap) = @_;
    die "cloning from a snapshot is not supported by the sds storage plugin\n" if defined $snap;
    my (undef, undef, undef, undef, undef, $isBase) = $plugin->parse_volname($volname);
    die "only a template disk can be cloned, not '$volname'\n" if !$isBase;

    my $baseres = sds_resource_name($scfg, $volname);
    my $info    = $plugin->_get_resource($scfg, $baseres);
    my $vol     = ($info->{volumes} && @{ $info->{volumes} }) ? $info->{volumes}[0] : undef;
    my $nodes   = $info->{nodes} // [];
    die "template '$volname' has no volume or no diskful node to copy from\n" if !$vol || !@$nodes;
    my $pool = $vol->{pool};
    my $lv   = $vol->{backingVolume} // $vol->{backing_volume};
    die "template '$volname' has no backing volume recorded\n" if !$pool || !$lv;

    # The copy runs on one node holding both, so the clone goes on the base's
    # own replica nodes, at the base's exact size.
    my $bytes = volume_size_bytes($vol);
    my $name  = $plugin->find_free_diskname($storeid, $scfg, $vmid, 'raw');
    my %clone_scfg = (%$scfg, sdsnodes => join(',', @$nodes), exactsize => 1);
    delete $clone_scfg{replicas};
    $plugin->alloc_image($storeid, \%clone_scfg, $vmid, 'raw', $name, int(($bytes + 1023) / 1024));

    my $source = ($scfg->{storagetype} // '') eq 'zfs' ? "/dev/zvol/$pool/$lv" : "/dev/$pool/$lv";
    my $client = $plugin->_client($scfg);
    my $newres = sds_resource_name($scfg, $name);
    eval {
        $client->request('POST', "/v1/resources/$newres/populate",
            { resource => $newres, volumeId => 0, sourceDevice => $source, node => $nodes->[0] });
    };
    if (my $err = $@) {
        # A half-filled disk must not stay behind looking like a clone.
        eval { $client->request('DELETE', "/v1/resources/$newres") };
        die "copying template '$volname' failed: $err";
    }
    return $name;
}

1;
