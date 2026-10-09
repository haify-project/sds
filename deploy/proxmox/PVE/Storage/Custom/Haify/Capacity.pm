package PVE::Storage::Custom::Haify::Capacity;

# Turns the controller's GET /v1/pools answer into the (total, free) pair PVE
# shows for the storage. Pure: no I/O, so the arithmetic is testable on its own.

use strict;
use warnings;

use Exporter qw(import);
our @EXPORT_OK = qw(pool_capacity node_capacity);

use PVE::Storage::Custom::Haify::Naming qw(gb_to_bytes same_pool);

# Field lookup that accepts both protojson's lowerCamelCase and the proto
# field name, since either marshaller setting may sit in front of the API.
sub _field {
    my ($pool, $camel, $snake) = @_;
    return $pool->{$camel} // $pool->{$snake};
}

# One node's copy of a pool, as (total_bytes, free_bytes).
#
# total_gb/free_gb describe the VOLUME GROUP. On a pool carrying a thin pool
# almost all of the group is allocated to the thin pool LV, so the group's free
# space stays near zero however empty the thin pool is; reporting it would make
# PVE treat thin-backed storage as full. Thin volumes are carved from the thin
# pool, so for them the thin pool's own size and data usage are the truth.
#
# The one exception is a storage pinned to thick LVs (`storagetype lvm`): those
# are allocated from the group's free extents, so the group figures are right.
sub node_capacity {
    my ($pool, $storagetype) = @_;

    my $thin_lv   = _field($pool, 'thinPoolLv', 'thin_pool_lv') // '';
    my $thin_size = _field($pool, 'thinSizeBytes', 'thin_size_bytes') // 0;
    my $thick     = defined($storagetype) && $storagetype eq 'lvm';

    if (length($thin_lv) && $thin_size > 0 && !$thick) {
        # protojson renders uint64 as a JSON string; numeric context copes.
        my $pct = _field($pool, 'thinDataPercent', 'thin_data_percent') // 0;
        $pct = 0   if $pct < 0;
        $pct = 100 if $pct > 100;
        my $total = int($thin_size);
        my $used  = int($total * $pct / 100);
        return ($total, $total - $used);
    }

    # Exact byte figures when the controller sends them; the whole-GiB fields
    # are kept for older controllers.
    my $total_bytes = _field($pool, 'totalBytes', 'total_bytes');
    if ($total_bytes) {
        return (int($total_bytes), int(_field($pool, 'freeBytes', 'free_bytes') // 0));
    }
    return (
        gb_to_bytes(_field($pool, 'totalGb', 'total_gb') // 0),
        gb_to_bytes(_field($pool, 'freeGb',  'free_gb')  // 0),
    );
}

# Capacity of the storage across every node's copy of the wanted pool.
#
# A pool exists once per node and a replica must fit on EVERY node that holds
# one, so the usable capacity is the smallest node's, not the sum. Reporting
# the sum would let PVE accept a disk that cannot be placed.
sub pool_capacity {
    my ($pools, $want, $storagetype) = @_;
    my ($total, $free);

    for my $pool (@{ $pools // [] }) {
        next if defined($want) && length($want) && !same_pool($pool->{name} // '', $want);

        my ($ptotal, $pfree) = node_capacity($pool, $storagetype);
        $total = $ptotal if !defined($total) || $ptotal < $total;
        $free  = $pfree  if !defined($free)  || $pfree  < $free;
    }

    return ($total // 0, $free // 0);
}

1;
