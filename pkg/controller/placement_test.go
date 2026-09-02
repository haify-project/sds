package controller

import (
	"context"
	"strings"
	"testing"

	"github.com/liliang-cn/sds/pkg/database"
	"github.com/liliang-cn/sds/pkg/deployment"
)

// n builds a placement node with optional labels for terse test tables.
func n(name string, freeGB uint64, labels map[string]string) placementNode {
	return placementNode{node: name, freeGB: freeGB, labels: labels}
}

func join(s []string) string { return strings.Join(s, ",") }

func TestSelectConstrained_CapacityOnly(t *testing.T) {
	nodes := []placementNode{
		n("n1", 100, nil),
		n("n2", 300, nil),
		n("n3", 200, nil),
	}
	got, err := selectConstrained(nodes, 2, placementConstraints{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if join(got) != "n2,n3" {
		t.Errorf("capacity-only should pick most-free: got %v, want [n2 n3]", got)
	}
}

func TestSelectConstrained_CapacityTieBreakByName(t *testing.T) {
	nodes := []placementNode{n("nB", 200, nil), n("nA", 200, nil), n("nC", 200, nil)}
	got, err := selectConstrained(nodes, 2, placementConstraints{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if join(got) != "nA,nB" {
		t.Errorf("tie-break by name: got %v, want [nA nB]", got)
	}
}

func TestSelectConstrained_InsufficientCapacity(t *testing.T) {
	_, err := selectConstrained([]placementNode{n("n1", 100, nil)}, 3, placementConstraints{})
	if err == nil || !strings.Contains(err.Error(), "insufficient capacity") {
		t.Fatalf("want insufficient-capacity error, got %v", err)
	}
}

func TestSelectConstrained_OnDifferentSpreadsRacks(t *testing.T) {
	// n1,n2 in rack A (most free), n3 in rack B. Spread must cross racks rather
	// than take the two most-free (both rack A).
	nodes := []placementNode{
		n("n1", 300, map[string]string{"rack": "A"}),
		n("n2", 250, map[string]string{"rack": "A"}),
		n("n3", 100, map[string]string{"rack": "B"}),
	}
	got, err := selectConstrained(nodes, 2, placementConstraints{onDifferent: []string{"rack"}})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if join(got) != "n1,n3" {
		t.Errorf("on-different rack: got %v, want [n1 n3]", got)
	}
}

func TestSelectConstrained_OnDifferentNotEnoughDomains(t *testing.T) {
	nodes := []placementNode{
		n("n1", 100, map[string]string{"rack": "A"}),
		n("n2", 90, map[string]string{"rack": "A"}),
		n("n3", 80, map[string]string{"rack": "B"}),
	}
	_, err := selectConstrained(nodes, 3, placementConstraints{onDifferent: []string{"rack"}})
	if err == nil || !strings.Contains(err.Error(), "fault domains") {
		t.Fatalf("want fault-domains error, got %v", err)
	}
}

func TestSelectConstrained_OnDifferentExcludesUnlabeled(t *testing.T) {
	nodes := []placementNode{
		n("labeled", 100, map[string]string{"rack": "A"}),
		n("bare", 999, nil), // missing rack -> ineligible under a rack constraint
	}
	_, err := selectConstrained(nodes, 2, placementConstraints{onDifferent: []string{"rack"}})
	if err == nil {
		t.Fatal("unlabeled node must be excluded, leaving too few domains")
	}
}

func TestSelectConstrained_OnSameKeepsOneZone(t *testing.T) {
	// zone east has two nodes that can hold both replicas; zone west has one.
	// on-same zone must keep both replicas in east.
	nodes := []placementNode{
		n("e1", 300, map[string]string{"zone": "east"}),
		n("e2", 250, map[string]string{"zone": "east"}),
		n("w1", 400, map[string]string{"zone": "west"}),
	}
	got, err := selectConstrained(nodes, 2, placementConstraints{onSame: []string{"zone"}})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if join(got) != "e1,e2" {
		t.Errorf("on-same zone: got %v, want [e1 e2] (only east has 2 nodes)", got)
	}
}

func TestSelectConstrained_OnSamePicksHigherCapacityGroup(t *testing.T) {
	// Both zones can hold 2 replicas; the higher-total-capacity zone wins.
	nodes := []placementNode{
		n("e1", 100, map[string]string{"zone": "east"}),
		n("e2", 100, map[string]string{"zone": "east"}),
		n("w1", 500, map[string]string{"zone": "west"}),
		n("w2", 500, map[string]string{"zone": "west"}),
	}
	got, err := selectConstrained(nodes, 2, placementConstraints{onSame: []string{"zone"}})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if join(got) != "w1,w2" {
		t.Errorf("on-same should pick the higher-capacity zone: got %v, want [w1 w2]", got)
	}
}

func TestSelectConstrained_OnSameAndOnDifferentCombined(t *testing.T) {
	// Keep replicas in one zone (on-same zone) but on different racks within it.
	// east: r1@A, r2@B ; west: only rack A -> cannot spread 2 racks.
	nodes := []placementNode{
		n("e-a", 100, map[string]string{"zone": "east", "rack": "A"}),
		n("e-b", 90, map[string]string{"zone": "east", "rack": "B"}),
		n("w-a1", 500, map[string]string{"zone": "west", "rack": "A"}),
		n("w-a2", 500, map[string]string{"zone": "west", "rack": "A"}),
	}
	got, err := selectConstrained(nodes, 2, placementConstraints{
		onSame:      []string{"zone"},
		onDifferent: []string{"rack"},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// west has more capacity but only rack A (fails on-different rack), so the
	// only satisfying group is east.
	if join(got) != "e-a,e-b" {
		t.Errorf("combined constraints: got %v, want [e-a e-b]", got)
	}
}

func TestSelectConstrained_DoesNotMutateInput(t *testing.T) {
	nodes := []placementNode{n("n1", 100, nil), n("n2", 300, nil), n("n3", 200, nil)}
	_, _ = selectConstrained(nodes, 2, placementConstraints{})
	if nodes[0].node != "n1" || nodes[1].node != "n2" || nodes[2].node != "n3" {
		t.Errorf("input slice was mutated: %v", nodes)
	}
}

func TestSelectConstrained_ReplicasValidated(t *testing.T) {
	if _, err := selectConstrained([]placementNode{n("n1", 10, nil)}, 0, placementConstraints{}); err == nil {
		t.Fatal("want error for replicas < 1")
	}
}

// --- capacity reading -------------------------------------------------------

const testThinPoolBytes = 100 << 30 // 100GiB of thin pool data space

// thinPoolInfo builds the pool report of a thin pool as ListPools produces it:
// the volume group reports no free space (SDS gave every extent to the pool)
// and the utilisation carries the only usable figure.
func thinPoolInfo(dataPct, metaPct float64) *PoolInfo {
	return &PoolInfo{
		Name: "sds_vg0", Type: "vg", Node: "n1", TotalGB: 100, FreeGB: 0,
		ThinUsage: &PoolThinInfo{
			PoolLV:      "sds_vg0_thin",
			SizeBytes:   testThinPoolBytes,
			DataPercent: dataPct,
			MetaPercent: metaPct,
		},
	}
}

func TestPoolPlacementCapacity_ThinUsesUtilisationNotVGFree(t *testing.T) {
	c := poolPlacementCapacity(thinPoolInfo(20, 1), false)
	if !c.known || !c.thin || c.full {
		t.Fatalf("thin pool at 20%% should be known, thin and not full: %+v", c)
	}
	if c.freeGB != 80 {
		t.Errorf("free space should come from the thin pool, got %dGB, want 80GB", c.freeGB)
	}
}

func TestPoolPlacementCapacity_ThinAdmitsOverProvisioning(t *testing.T) {
	// The regression: an empty thin pool used to be filtered out because its
	// volume group reports zero free. It must accept a volume larger than the
	// pool itself — that is what thin provisioning is for.
	c := poolPlacementCapacity(thinPoolInfo(0, 0), false)
	if !c.admits(500) {
		t.Fatalf("empty thin pool must admit a 500GB volume: %+v", c)
	}
	if !poolPlacementCapacity(thinPoolInfo(ThinPoolNearFullPercent, 10), false).admits(500) {
		t.Error("a near-full thin pool is a reason to extend it, not to refuse placement")
	}
}

func TestPoolPlacementCapacity_ThinRejectsWhenExhausted(t *testing.T) {
	cases := map[string]*PoolInfo{
		"data past the full threshold": thinPoolInfo(ThinPoolFullPercent, 10),
		"metadata past it":             thinPoolInfo(10, ThinPoolFullPercent+2),
	}
	for name, p := range cases {
		if poolPlacementCapacity(p, false).admits(1) {
			t.Errorf("%s: exhausted thin pool must not take another replica", name)
		}
	}

	outOfSpace := thinPoolInfo(60, 10)
	outOfSpace.ThinUsage.OutOfSpace = true
	if poolPlacementCapacity(outOfSpace, false).admits(1) {
		t.Error("LVM's own out-of-space verdict must reject regardless of the percentages")
	}
}

func TestPoolPlacementCapacity_ThickVolumeGroupUnchanged(t *testing.T) {
	thick := &PoolInfo{Name: "sds_vg0", Type: "vg", Node: "n1", TotalGB: 100, FreeGB: 40}
	c := poolPlacementCapacity(thick, false)
	if !c.known || c.thin || c.freeGB != 40 {
		t.Fatalf("thick group should report its own free space: %+v", c)
	}
	if !c.admits(40) {
		t.Error("thick group must admit a volume that exactly fits")
	}
	if c.admits(41) {
		t.Error("thick group free space is a hard limit and must reject an oversized volume")
	}

	// A genuinely full group stays a rejection: unlike a thin pool, zero free
	// extents in a thick group is a measurement, not an artefact.
	if poolPlacementCapacity(&PoolInfo{Name: "sds_vg0", FreeGB: 0}, false).admits(1) {
		t.Error("full thick group must be rejected")
	}
}

func TestPoolPlacementCapacity_UnknownIsNotFull(t *testing.T) {
	// Three ways a pool can fail to describe its room. None may reject.
	cases := map[string]poolCapacity{
		"thin pool recorded but not reported": poolPlacementCapacity(&PoolInfo{Name: "sds_vg0", FreeGB: 0}, true),
		"pool flagged thin by the database":   poolPlacementCapacity(&PoolInfo{Name: "sds_vg0", FreeGB: 0, Thin: true}, false),
		"utilisation without a pool size": poolPlacementCapacity(&PoolInfo{
			Name: "sds_vg0", ThinUsage: &PoolThinInfo{PoolLV: "sds_vg0_thin"},
		}, false),
	}
	for name, c := range cases {
		if c.known {
			t.Errorf("%s: capacity must read as unknown, got %+v", name, c)
		}
		if !c.admits(9999) {
			t.Errorf("%s: unknown capacity must not reject", name)
		}
		if c.freeGB != 0 {
			t.Errorf("%s: unknown capacity must rank last (freeGB 0), got %d", name, c.freeGB)
		}
	}
}

// --- auto-placement end to end ----------------------------------------------

// vgsLineNoFreeExtents is what a thin pool's volume group reports: the pool LV
// holds every extent, so vg_free is zero and stays zero.
const vgsLineNoFreeExtents = "  sds_vg0|107390828544|0|/dev/vdb"

// newPlacementTestCluster builds a two-node controller whose pool listing is
// driven by the given vgs and lvs reports, with both nodes registered online.
func newPlacementTestCluster(t *testing.T, vgs, thin string) *Controller {
	t.Helper()
	ctrl := newBasicTestController(thinClusterDeployment(vgs, thin))
	ctrl.db = newTestDB(t)
	for name, addr := range map[string]string{"n1": "10.0.0.1", "n2": "10.0.0.2"} {
		if _, err := ctrl.nodes.RegisterNode(context.Background(), name, addr); err != nil {
			t.Fatalf("register %s: %v", name, err)
		}
	}
	return ctrl
}

func TestSelectPlacementNodes_EmptyThinClusterHasCandidates(t *testing.T) {
	// The bug: on a thin pool the volume group reports no free space for the
	// pool's entire life, so every node was filtered out and an empty cluster
	// answered "insufficient capacity". The requested size is larger than the
	// pool on purpose — thin volumes are allowed to be.
	ctrl := newPlacementTestCluster(t, vgsLineNoFreeExtents,
		"  sds_vg0|sds_vg0_thin|thin-pool|107374182400|0.00|0.50|twi-aotz--")

	got, err := ctrl.resources.selectPlacementNodes(context.Background(), "vg0", 500, 2, nil, nil, nil)
	if err != nil {
		t.Fatalf("empty thin cluster must place: %v", err)
	}
	if join(got) != "n1,n2" {
		t.Errorf("got %v, want both nodes", got)
	}
}

func TestSelectPlacementNodes_ThinRanksByUtilisation(t *testing.T) {
	// Same pool, different fullness per node: the emptier node must win, which
	// only works if ranking reads the thin utilisation rather than vg_free
	// (identical, and zero, on both).
	ctrl := newBasicTestController(&fakeDeploymentClient{
		execFunc: func(ctx context.Context, hosts []string, cmd string, opts ...deployment.ExecOption) (*deployment.ExecResult, error) {
			if strings.Contains(cmd, "vgs") {
				return successExecResult(hosts, vgsLineNoFreeExtents), nil
			}
			return successExecResult(hosts, ""), nil
		},
		lvsThinReportFunc: func(ctx context.Context, hosts []string, vgName string) (*deployment.ExecResult, error) {
			res := &deployment.ExecResult{Hosts: make(map[string]*deployment.HostResult, len(hosts))}
			for _, h := range hosts {
				pct := "10.00"
				if h == "10.0.0.1" {
					pct = "90.00"
				}
				res.Hosts[h] = &deployment.HostResult{Host: h, Success: true,
					Output: "  sds_vg0|sds_vg0_thin|thin-pool|107374182400|" + pct + "|1.00|twi-aotz--"}
			}
			return res, nil
		},
	})
	ctrl.db = newTestDB(t)
	for name, addr := range map[string]string{"n1": "10.0.0.1", "n2": "10.0.0.2"} {
		if _, err := ctrl.nodes.RegisterNode(context.Background(), name, addr); err != nil {
			t.Fatalf("register %s: %v", name, err)
		}
	}

	got, err := ctrl.resources.selectPlacementNodes(context.Background(), "vg0", 10, 1, nil, nil, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if join(got) != "n2" {
		t.Errorf("got %v, want the emptier thin pool [n2]", got)
	}
}

func TestSelectPlacementNodes_ExhaustedThinPoolIsRejected(t *testing.T) {
	ctrl := newPlacementTestCluster(t, vgsLineNoFreeExtents,
		"  sds_vg0|sds_vg0_thin|thin-pool|107374182400|97.00|3.00|twi-aotz--")

	_, err := ctrl.resources.selectPlacementNodes(context.Background(), "vg0", 1, 1, nil, nil, nil)
	if err == nil || !strings.Contains(err.Error(), "insufficient capacity") {
		t.Fatalf("a thin pool past the full threshold must be refused, got %v", err)
	}
}

func TestSelectPlacementNodes_ThickVolumeGroupUnchanged(t *testing.T) {
	// 200 GiB free, no thin pool in the group: the old hard limit still applies.
	const thickVGS = "  sds_vg0|214748364800|214748364800|/dev/vdb"
	ctrl := newPlacementTestCluster(t, thickVGS, "")

	got, err := ctrl.resources.selectPlacementNodes(context.Background(), "vg0", 200, 2, nil, nil, nil)
	if err != nil {
		t.Fatalf("a volume that fits must place: %v", err)
	}
	if join(got) != "n1,n2" {
		t.Errorf("got %v, want both nodes", got)
	}

	if _, err := ctrl.resources.selectPlacementNodes(context.Background(), "vg0", 500, 1, nil, nil, nil); err == nil {
		t.Error("a thick group must still refuse a volume larger than its free space")
	}
}

func TestSelectPlacementNodes_UnreadableThinPoolStillPlaces(t *testing.T) {
	// The pool exists and is thin, but its utilisation did not come back (an
	// lvs that failed, a node answering slowly). vg_free is zero as always, so
	// without the recorded pool type this looks exactly like a full group.
	ctrl := newPlacementTestCluster(t, vgsLineNoFreeExtents, "")

	if _, err := ctrl.resources.selectPlacementNodes(context.Background(), "vg0", 10, 1, nil, nil, nil); err == nil {
		t.Fatal("with nothing on file the group is indistinguishable from a full one and is refused")
	}

	if err := ctrl.db.SavePool(context.Background(), &database.Pool{
		Name: "sds_vg0", Type: "thin_pool", Node: "n1", Devices: "/dev/vdb",
	}); err != nil {
		t.Fatalf("save pool: %v", err)
	}

	got, err := ctrl.resources.selectPlacementNodes(context.Background(), "vg0", 10, 2, nil, nil, nil)
	if err != nil {
		t.Fatalf("a thin pool of unknown fullness must not be treated as full: %v", err)
	}
	if join(got) != "n1,n2" {
		t.Errorf("got %v, want both nodes", got)
	}
}
