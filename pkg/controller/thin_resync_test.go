package controller

import (
	"context"
	"testing"

	"github.com/haify-project/haify/pkg/database"
)

func TestWithThinResyncDefaults(t *testing.T) {
	ctx := context.Background()
	ctrl := newPlacementTestCluster(t, vgsLineNoFreeExtents, "")
	rm := ctrl.resources
	vols := []resolvedVolume{{id: 0, pool: "vg0"}}

	if got := rm.withThinResyncDefaults(ctx, nil, "lvm", vols); got["disk/rs-discard-granularity"] != "" {
		t.Fatalf("a thick pool must not get discards during resync: %v", got)
	}

	if err := ctrl.db.SavePool(ctx, &database.Pool{Name: "haify_vg0", Type: "thin_pool", Node: "n1", Devices: "/dev/vdb"}); err != nil {
		t.Fatalf("save pool: %v", err)
	}
	in := map[string]string{"net/max-buffers": "8000"}
	got := rm.withThinResyncDefaults(ctx, in, "lvm", vols)
	if got["disk/rs-discard-granularity"] != rsDiscardGranularity || got["net/max-buffers"] != "8000" {
		t.Fatalf("thin pool: got %v", got)
	}
	if _, set := in["disk/rs-discard-granularity"]; set {
		t.Fatal("the caller's options were modified")
	}

	own := map[string]string{"disk/rs-discard-granularity": "1048576"}
	if got := rm.withThinResyncDefaults(ctx, own, "lvm", vols); got["disk/rs-discard-granularity"] != "1048576" {
		t.Fatalf("an explicit value must win: %v", got)
	}

	if got := rm.withThinResyncDefaults(ctx, nil, "zfs-thin", []resolvedVolume{{pool: "tank"}}); got["disk/rs-discard-granularity"] != rsDiscardGranularity {
		t.Fatalf("zfs-thin: got %v", got)
	}
}

// A volume with disk options — written by set-options, and by default for a
// resource on thin storage — has a `disk { ... }` block after the `disk <path>;`
// line. The block is not a backing device: reading its "{" as one made a resize
// run `lvresize -L 2G -y {`.
func TestParseResourceConfigVolumesIgnoresTheDiskOptionsBlock(t *testing.T) {
	cfg := `resource r5 {
    volume 0 {
        device    minor 4;
        disk      /dev/haify_tp/r5_data;
        meta-disk internal;
        disk {
            rs-discard-granularity 65536;
        }
    }
    on sdt1 {
        address 10.0.0.1:7104;
        node-id 0;
    }
}
`
	vols := parseResourceConfigVolumes(cfg)
	if len(vols) != 1 || vols[0].DiskPath != "/dev/haify_tp/r5_data" || vols[0].Minor != 4 {
		t.Fatalf("got %+v", vols)
	}
}
