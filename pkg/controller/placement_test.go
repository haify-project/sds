package controller

import (
	"strings"
	"testing"
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
