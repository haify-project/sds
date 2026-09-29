package controller

import (
	"context"
	"testing"

	"github.com/liliang-cn/sds/pkg/database"
)

func TestWithThinResyncDefaults(t *testing.T) {
	ctx := context.Background()
	ctrl := newPlacementTestCluster(t, vgsLineNoFreeExtents, "")
	rm := ctrl.resources
	vols := []resolvedVolume{{id: 0, pool: "vg0"}}

	if got := rm.withThinResyncDefaults(ctx, nil, "lvm", vols); got["disk/rs-discard-granularity"] != "" {
		t.Fatalf("a thick pool must not get discards during resync: %v", got)
	}

	if err := ctrl.db.SavePool(ctx, &database.Pool{Name: "sds_vg0", Type: "thin_pool", Node: "n1", Devices: "/dev/vdb"}); err != nil {
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
