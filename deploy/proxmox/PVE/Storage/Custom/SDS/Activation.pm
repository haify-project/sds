package PVE::Storage::Custom::SDS::Activation;

# activate_volume and deactivate_volume for SDSPlugin.pm.
#
# Both normally go through sds-controller, which knows the cluster: it attaches
# a compute-only host as a diskless client, opens the dual-primary window for a
# live migration, and promotes with a quorum guard. Two things changed that
# made this its own module:
#
#  - The controller is not always reachable. Every VM start went through it, so
#    an unreachable controller meant no VM could start and PVE HA could not
#    restart one anywhere — the storage layer's single point of failure was the
#    one component that holds no data. When the controller cannot be reached and
#    the volume is already up on this node, the node promotes or demotes it
#    itself with plain drbdadm, which DRBD's quorum still guards. Anything that
#    needs the cluster view — attaching, a migration — still needs the
#    controller.
#
#  - A diskless client used to stay attached forever. Every PVE host a guest had
#    ever run on stayed in its resource, and opening the migration window needed
#    every one of them to answer, so one host that was down blocked the live
#    migration of every guest that had once run there. Deactivating now
#    detaches the client, and the window is opened on the migration's two nodes
#    only.

use strict;
use warnings;

use JSON::PP ();
use PVE::Storage::Custom::SDS::Client qw(_uri_escape);
use PVE::Storage::Custom::SDS::Naming qw(sds_resource_name _node_participates _other_primary_node);
use PVE::Storage::Custom::SDS::Migration qw(assert_live_migration);

use Exporter qw(import);
our @EXPORT_OK = qw(activate deactivate controller_unreachable local_device_path);

# Test seam: runs a command on this node and returns (exit code, output with
# stderr folded in).
our $RUN = sub {
    my (@cmd) = @_;
    my $pid = open(my $fh, '-|');
    return (127, "cannot fork: $!") if !defined $pid;
    if ($pid == 0) {
        open(STDERR, '>&', \*STDOUT);
        exec(@cmd) or exit 127;
    }
    local $/;
    my $out = <$fh> // '';
    close($fh);
    return ($? >> 8, $out);
};

# True when an error says no controller could be reached at all, as opposed to
# a controller that answered and refused.
sub controller_unreachable {
    my ($err) = @_;
    return defined($err) && $err =~ m/^sds controller unreachable/;
}

# The node-local path DRBD publishes for a resource that is up here.
sub local_device_path {
    my ($resname) = @_;
    return "/dev/drbd/by-res/$resname/0";
}

sub _device_check { return $PVE::Storage::Custom::SDSPlugin::DEVICE_CHECK->(@_) }

sub _wait_for_device {
    my ($path) = @_;
    my $deadline = time() + $PVE::Storage::Custom::SDSPlugin::DEVICE_WAIT_SECONDS;
    while (1) {
        return 1 if _device_check($path);
        return 0 if time() >= $deadline;
        select(undef, undef, undef, $PVE::Storage::Custom::SDSPlugin::DEVICE_POLL_INTERVAL);
    }
}

# Opens (enable) or closes the dual-primary window. Opening names the two
# nodes of the migration: allow-two-primaries only matters on the connection
# between the two Primaries.
sub _set_dual_primary {
    my ($class, $scfg, $resname, $enable, $nodes) = @_;
    my $payload = { resource => $resname, enable => $enable ? JSON::PP::true : JSON::PP::false };
    $payload->{nodes} = $nodes if $enable && $nodes;
    $class->_client($scfg)->request('POST', "/v1/resources/$resname/dual-primary", $payload);
}

sub activate {
    my ($class, $scfg, $volname) = @_;

    my ($vtype, $name, $vmid) = $class->parse_volname($volname);
    my $resname = sds_resource_name($scfg, $name);
    my $node    = PVE::Storage::Custom::SDSPlugin::_nodename();

    my $info = eval { $class->_get_resource($scfg, $resname) };
    if (my $err = $@) {
        die $err if !controller_unreachable($err);
        return _activate_locally($resname, $err);
    }
    my $client = $class->_client($scfg);

    # A PVE host that stores no replica still has to see /dev/drbdN to run the
    # guest, so it joins the resource as a diskless client. This is what lets a
    # compute-only hypervisor take part at all, including as a migration target.
    if (!_node_participates($info, $node)) {
        $client->request('POST', "/v1/resources/$resname/diskless-clients",
            { resource => $resname, node => $node });
    }

    # Live migration is the one case where two nodes legitimately hold the disk
    # open at once. Open the dual-primary window only when another node is
    # still Primary AND PVE is migrating the guest from there; any other
    # Primary is a leftover, refused rather than joined (SDS/Migration.pm).
    my $status = $class->_get_status($scfg, $resname);
    my $peer   = _other_primary_node($status, $node);
    my $opened = 0;

    if (defined $peer) {
        assert_live_migration($vmid, $node, $peer, $volname, $resname);
        _set_dual_primary($class, $scfg, $resname, 1, [ $peer, $node ]);
        $opened = 1;
    }

    eval {
        # quorum-guarded: sds force-promotes only if this node holds DRBD
        # quorum and refuses otherwise, so an HA restart after a hard node
        # failure cannot split-brain.
        $client->request('POST', "/v1/resources/$resname/primary",
            { resource => $resname, node => $node, quorumGuarded => JSON::PP::true });
    };
    if (my $err = $@) {
        # Never leave the window open on a failed promote.
        eval { _set_dual_primary($class, $scfg, $resname, 0) } if $opened;
        die $err;
    }

    my $device = $class->filesystem_path($scfg, $volname);
    if (!_wait_for_device($device)) {
        eval { _set_dual_primary($class, $scfg, $resname, 0) } if $opened;
        die "sds volume '$volname' promoted but $device did not appear within "
            . "${PVE::Storage::Custom::SDSPlugin::DEVICE_WAIT_SECONDS}s\n";
    }

    return 1;
}

# Promotes a volume that is already up on this node, without the controller.
#
# Plain `drbdadm primary`, never --force: DRBD refuses it unless this node has
# quorum and can reach an UpToDate copy, which is the same guard the controller
# applies. It is also refused while any peer is Primary — normally DRBD would
# refuse that itself, but a dual-primary window left open by a crashed
# migration would let it through, and only the controller can tell a migration
# from a leftover.
sub _activate_locally {
    my ($resname, $why) = @_;
    chomp(my $reason = $why);

    die "$reason; and $resname is not up on this node, so it cannot be activated without the controller\n"
        if !_device_check(local_device_path($resname));

    my ($rc, $out) = $RUN->('drbdsetup', 'status', $resname);
    die "$reason; and reading $resname here failed: $out\n" if $rc != 0;

    my ($local_role) = $out =~ m/^\S+\s+role:(\S+)/m;
    return 1 if defined($local_role) && $local_role eq 'Primary';
    for my $line (split /\n/, $out) {
        if ($line =~ m/^  (\S+)\s.*\brole:Primary\b/ && $1 !~ m/:/) {
            die "$reason; and $1 holds $resname Primary. A live migration needs the controller\n";
        }
    }

    ($rc, $out) = $RUN->('drbdadm', 'primary', $resname);
    die "$reason; promoting $resname on this node failed too: $out\n" if $rc != 0;
    warn "sds: controller unreachable; promoted $resname on this node with drbdadm\n";
    return 1;
}

sub deactivate {
    my ($class, $scfg, $volname) = @_;

    my ($vtype, $name) = $class->parse_volname($volname);
    my $resname = sds_resource_name($scfg, $name);
    my $node    = PVE::Storage::Custom::SDSPlugin::_nodename();
    my $client  = $class->_client($scfg);

    my $demote_err;
    eval {
        $client->request('POST', "/v1/resources/$resname/secondary",
            { resource => $resname, node => $node });
    };
    if (my $err = $@) {
        return _deactivate_locally($resname, $err) if controller_unreachable($err);
        $demote_err = $err;
    }

    # Close the dual-primary window unconditionally: this is the source side of
    # a completed migration, and leaving it open is the failure mode the whole
    # bracket exists to prevent. Disabling is idempotent and verified by sds,
    # so it is safe even when no window was ever opened.
    eval { _set_dual_primary($class, $scfg, $resname, 0) };
    my $dual_err = $@;

    die $demote_err if $demote_err;
    die $dual_err   if $dual_err;

    _detach_if_client($class, $scfg, $resname, $node);
    return 1;
}

# Demotes without the controller. A deactivate that could not reach the
# controller used to fail and leave this node Primary — the leftover that later
# looked like a live migration. The window, if one was open, cannot be closed
# cluster-wide from here; this node's side is closed and the rest says how.
sub _deactivate_locally {
    my ($resname, $why) = @_;
    chomp(my $reason = $why);

    return 1 if !_device_check(local_device_path($resname));    # not up here: nothing to demote

    my ($rc, $out) = $RUN->('drbdadm', 'secondary', $resname);
    die "$reason; demoting $resname on this node failed too: $out\n" if $rc != 0;
    $RUN->('drbdadm', 'net-options', '--allow-two-primaries=no', $resname);
    warn "sds: controller unreachable; demoted $resname on this node with drbdadm. "
        . "Once it is back, run 'sds resource dual-primary $resname off' if a migration was under way\n";
    return 1;
}

# Detaches this node when it is only a diskless client of the resource. A
# failure is logged, never fatal: the guest is already stopped here, and an
# attachment left behind is what every deactivate did before.
sub _detach_if_client {
    my ($class, $scfg, $resname, $node) = @_;

    my $info = eval { $class->_get_resource($scfg, $resname) };
    return if !$info;
    my $is = sub { my ($list) = @_; return scalar grep { defined($_) && $_ eq $node } @{ $list // [] } };
    return if $is->($info->{nodes}) || $is->($info->{disklessNodes} // $info->{diskless_nodes});
    return if !$is->($info->{disklessClients} // $info->{diskless_clients});

    eval {
        $class->_client($scfg)->request('DELETE',
            "/v1/resources/$resname/diskless-clients/" . _uri_escape($node));
    };
    warn "sds: could not detach $node from $resname (it stays attached): $@" if $@;
}

1;
