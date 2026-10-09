package controller

import (
	"context"
	"strings"
	"testing"

	"github.com/haify-project/haify/pkg/database"
	"github.com/haify-project/haify/pkg/deployment"
)

// A 20 GiB volume cannot be synced onto a node whose 100 GiB thin pool has
// 10 GiB left, and can onto one that has 90.
func TestAssertPoolRoomRefusesAFullThinPool(t *testing.T) {
	ctx := context.Background()
	ctrl := newBasicTestController(&fakeDeploymentClient{})
	ctrl.db = newTestDB(t)
	for name, addr := range map[string]string{"n1": "10.0.0.1", "n2": "10.0.0.2"} {
		if _, err := ctrl.nodes.RegisterNode(ctx, name, addr); err != nil {
			t.Fatal(err)
		}
	}
	dep := thinClusterDeployment(vgsLineNoFreeExtents, "")
	dep.lvsThinReportFunc = func(_ context.Context, hosts []string, vg string) (*deployment.ExecResult, error) {
		res := &deployment.ExecResult{Hosts: make(map[string]*deployment.HostResult, len(hosts))}
		for _, h := range hosts {
			pct := "10.00"
			if h == "10.0.0.1" {
				pct = "90.00"
			}
			res.Hosts[h] = &deployment.HostResult{Host: h, Success: true,
				Output: "  haify_vg0|haify_vg0_thin|thin-pool|107374182400|" + pct + "|1.00|twi-aotz--"}
		}
		return res, nil
	}
	ctrl.deployment = dep
	ctrl.resources.SetDeployment(dep)
	if err := ctrl.db.SavePool(ctx, &database.Pool{Name: "haify_vg0", Type: "thin_pool", Node: "n1", Devices: "/dev/vdb"}); err != nil {
		t.Fatal(err)
	}
	if err := ctrl.db.SaveResource(ctx, &database.Resource{Name: "r", Nodes: "n2"}); err != nil {
		t.Fatal(err)
	}
	if err := ctrl.db.SaveVolume(ctx, &database.Volume{ResourceName: "r", VolumeID: 0, Pool: "haify_vg0", SizeGB: 20}); err != nil {
		t.Fatal(err)
	}

	err := ctrl.resources.assertPoolRoom(ctx, "r", "n1")
	if err == nil || !strings.Contains(err.Error(), "--ignore-free-space") || !strings.Contains(err.Error(), "10.0 GiB free") {
		t.Fatalf("a 90%%-full pool must be refused with the way out named: %v", err)
	}
	if err := ctrl.resources.assertPoolRoom(ctx, "r", "n2"); err != nil {
		t.Fatalf("a pool with room must be accepted: %v", err)
	}
	if err := ctrl.resources.assertPoolRoom(ctx, "no-such-resource", "n1"); err != nil {
		t.Fatalf("an unreadable volume is not a refusal: %v", err)
	}
}

// A resize resyncs the added area onto every replica: one replica short of the
// growth is enough to drop a disk, so the refusal names it.
func TestAssertPoolRoomForGrowthNamesTheReplicaThatCannotHoldIt(t *testing.T) {
	ctx := context.Background()
	ctrl := newBasicTestController(&fakeDeploymentClient{})
	ctrl.db = newTestDB(t)
	for name, addr := range map[string]string{"n1": "10.0.0.1", "n2": "10.0.0.2"} {
		if _, err := ctrl.nodes.RegisterNode(ctx, name, addr); err != nil {
			t.Fatal(err)
		}
	}
	dep := thinClusterDeployment(vgsLineNoFreeExtents, "")
	dep.lvsThinReportFunc = func(_ context.Context, hosts []string, vg string) (*deployment.ExecResult, error) {
		res := &deployment.ExecResult{Hosts: make(map[string]*deployment.HostResult, len(hosts))}
		for _, h := range hosts {
			pct := "10.00"
			if h == "10.0.0.1" {
				pct = "90.00" // 10 GiB free of 100
			}
			res.Hosts[h] = &deployment.HostResult{Host: h, Success: true,
				Output: "  haify_vg0|haify_vg0_thin|thin-pool|107374182400|" + pct + "|1.00|twi-aotz--"}
		}
		return res, nil
	}
	ctrl.deployment = dep
	ctrl.resources.SetDeployment(dep)
	if err := ctrl.db.SavePool(ctx, &database.Pool{Name: "haify_vg0", Type: "thin_pool", Node: "n1", Devices: "/dev/vdb"}); err != nil {
		t.Fatal(err)
	}
	if err := ctrl.db.SaveResource(ctx, &database.Resource{Name: "r", Nodes: "n1,n2"}); err != nil {
		t.Fatal(err)
	}
	if err := ctrl.db.SaveVolume(ctx, &database.Volume{ResourceName: "r", VolumeID: 0, Pool: "haify_vg0", SizeGB: 5, Device: "/dev/haify_vg0/r_data"}); err != nil {
		t.Fatal(err)
	}

	err := ctrl.resources.assertPoolRoomForGrowth(ctx, "r", 0, 20) // grows by 15 GiB
	if err == nil || !strings.Contains(err.Error(), "n1 (10.0 GiB free)") || strings.Contains(err.Error(), "n2 (") {
		t.Fatalf("the replica that cannot hold 15 GiB must be named, and only it: %v", err)
	}
	if err := ctrl.resources.assertPoolRoomForGrowth(ctx, "r", 0, 10); err != nil { // 5 GiB
		t.Fatalf("a growth every pool can hold is accepted: %v", err)
	}
	if err := ctrl.resources.assertPoolRoomForGrowth(ctx, "r", 0, 5); err != nil {
		t.Fatalf("no growth, nothing to check: %v", err)
	}
}

// The recorded size can lag the volume — a resize that failed after the LVs
// grew leaves it behind — and a retry that asks for the size the volume
// already has is not growth. The live size decides.
func TestAssertPoolRoomForGrowthMeasuresFromTheLiveVolume(t *testing.T) {
	ctx := context.Background()
	ctrl := newBasicTestController(&fakeDeploymentClient{})
	ctrl.db = newTestDB(t)
	for name, addr := range map[string]string{"n1": "10.0.0.1", "n2": "10.0.0.2"} {
		if _, err := ctrl.nodes.RegisterNode(ctx, name, addr); err != nil {
			t.Fatal(err)
		}
	}
	dep := thinClusterDeployment(vgsLineNoFreeExtents, "")
	dep.lvsThinReportFunc = func(_ context.Context, hosts []string, vg string) (*deployment.ExecResult, error) {
		res := &deployment.ExecResult{Hosts: make(map[string]*deployment.HostResult, len(hosts))}
		for _, h := range hosts {
			res.Hosts[h] = &deployment.HostResult{Host: h, Success: true,
				Output: "  haify_vg0|haify_vg0_thin|thin-pool|107374182400|99.00|1.00|twi-aotz--"} // 1 GiB free
		}
		return res, nil
	}
	base := dep.execFunc
	dep.execFunc = func(c context.Context, hosts []string, cmd string, o ...deployment.ExecOption) (*deployment.ExecResult, error) {
		if strings.HasPrefix(cmd, "sudo lvs --noheadings --nosuffix --units b -o lv_size") {
			return successExecResult(hosts, "  4294967296\n"), nil // 4 GiB, whatever the record says
		}
		return base(c, hosts, cmd, o...)
	}
	ctrl.deployment = dep
	ctrl.resources.SetDeployment(dep)
	if err := ctrl.db.SavePool(ctx, &database.Pool{Name: "haify_vg0", Type: "thin_pool", Node: "n1", Devices: "/dev/vdb"}); err != nil {
		t.Fatal(err)
	}
	if err := ctrl.db.SaveResource(ctx, &database.Resource{Name: "r", Nodes: "n1,n2"}); err != nil {
		t.Fatal(err)
	}
	// Recorded as 2 GiB; it is 4.
	if err := ctrl.db.SaveVolume(ctx, &database.Volume{ResourceName: "r", VolumeName: "r_data", VolumeID: 0, Pool: "haify_vg0", SizeGB: 2, Device: "/dev/haify_vg0/r_data"}); err != nil {
		t.Fatal(err)
	}
	if err := ctrl.resources.assertPoolRoomForGrowth(ctx, "r", 0, 4); err != nil {
		t.Fatalf("asking for the size it already has is a retry, not growth: %v", err)
	}
	if err := ctrl.resources.assertPoolRoomForGrowth(ctx, "r", 0, 6); err == nil {
		t.Fatal("growing by 2 GiB into 1 GiB of free space must be refused")
	}
}
