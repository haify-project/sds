package controller

import (
	"strings"
	"testing"
)

func TestPickNodesByFreeSpace_MostFreeFirst(t *testing.T) {
	cands := []placementCandidate{
		{node: "n1", freeGB: 100},
		{node: "n2", freeGB: 300},
		{node: "n3", freeGB: 200},
	}
	got, err := pickNodesByFreeSpace(cands, 2)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Most free space wins: n2 (300) then n3 (200).
	if strings.Join(got, ",") != "n2,n3" {
		t.Errorf("got %v, want [n2 n3]", got)
	}
}

func TestPickNodesByFreeSpace_DeterministicTieBreak(t *testing.T) {
	// Equal free space must break ties by name so placement is reproducible.
	cands := []placementCandidate{
		{node: "nB", freeGB: 200},
		{node: "nA", freeGB: 200},
		{node: "nC", freeGB: 200},
	}
	got, err := pickNodesByFreeSpace(cands, 2)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if strings.Join(got, ",") != "nA,nB" {
		t.Errorf("tie-break should be by name: got %v, want [nA nB]", got)
	}
}

func TestPickNodesByFreeSpace_InsufficientCapacity(t *testing.T) {
	cands := []placementCandidate{{node: "n1", freeGB: 100}}
	_, err := pickNodesByFreeSpace(cands, 3)
	if err == nil {
		t.Fatal("want error when fewer candidates than replicas")
	}
	if !strings.Contains(err.Error(), "insufficient capacity") {
		t.Errorf("error should mention insufficient capacity, got: %v", err)
	}
}

func TestPickNodesByFreeSpace_ExactCount(t *testing.T) {
	cands := []placementCandidate{{node: "n1", freeGB: 50}, {node: "n2", freeGB: 60}}
	got, err := pickNodesByFreeSpace(cands, 2)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 2 {
		t.Errorf("want 2 nodes, got %v", got)
	}
}

func TestPickNodesByFreeSpace_ReplicasValidated(t *testing.T) {
	if _, err := pickNodesByFreeSpace([]placementCandidate{{node: "n1", freeGB: 10}}, 0); err == nil {
		t.Fatal("want error for replicas < 1")
	}
}

func TestPickNodesByFreeSpace_DoesNotMutateInput(t *testing.T) {
	cands := []placementCandidate{{node: "n1", freeGB: 100}, {node: "n2", freeGB: 300}, {node: "n3", freeGB: 200}}
	_, _ = pickNodesByFreeSpace(cands, 2)
	// The caller's slice order must be preserved (sort operates on a copy).
	if cands[0].node != "n1" || cands[1].node != "n2" || cands[2].node != "n3" {
		t.Errorf("input slice was mutated: %v", cands)
	}
}

func TestPickNodesOnDifferentDomains_SpreadsAcrossRacks(t *testing.T) {
	// Two racks; n1,n2 in A, n3 in B. A 2-replica spread must take one from A
	// and one from B — never both from A even though A has the most free space.
	cands := []placementCandidate{
		{node: "n1", freeGB: 300, domain: "A"},
		{node: "n2", freeGB: 250, domain: "A"},
		{node: "n3", freeGB: 100, domain: "B"},
	}
	got, err := pickNodesOnDifferentDomains(cands, 2)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Best of rack A is n1 (300); rack B's only node is n3. Domains ranked by
	// their best node's free space: A(300) then B(100).
	if strings.Join(got, ",") != "n1,n3" {
		t.Errorf("got %v, want [n1 n3] (one per rack)", got)
	}
}

func TestPickNodesOnDifferentDomains_BestNodePerDomain(t *testing.T) {
	// Within a domain the most-free node represents it.
	cands := []placementCandidate{
		{node: "a-small", freeGB: 50, domain: "A"},
		{node: "a-big", freeGB: 500, domain: "A"},
		{node: "b1", freeGB: 200, domain: "B"},
	}
	got, err := pickNodesOnDifferentDomains(cands, 2)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if strings.Join(got, ",") != "a-big,b1" {
		t.Errorf("got %v, want [a-big b1] (most-free per domain)", got)
	}
}

func TestPickNodesOnDifferentDomains_NotEnoughDomains(t *testing.T) {
	// Three nodes but only two racks: cannot place 3 replicas on different racks.
	cands := []placementCandidate{
		{node: "n1", freeGB: 100, domain: "A"},
		{node: "n2", freeGB: 90, domain: "A"},
		{node: "n3", freeGB: 80, domain: "B"},
	}
	_, err := pickNodesOnDifferentDomains(cands, 3)
	if err == nil {
		t.Fatal("want error when fewer domains than replicas")
	}
	if !strings.Contains(err.Error(), "fault domains") {
		t.Errorf("error should mention fault domains, got: %v", err)
	}
}

func TestPickNodesOnDifferentDomains_UnlabeledExcluded(t *testing.T) {
	// A node with no domain cannot satisfy a spread constraint and is skipped.
	cands := []placementCandidate{
		{node: "labeled", freeGB: 100, domain: "A"},
		{node: "unlabeled", freeGB: 999, domain: ""},
	}
	_, err := pickNodesOnDifferentDomains(cands, 2)
	if err == nil {
		t.Fatal("want error: only one usable domain (unlabeled excluded)")
	}
}

func TestPickNodesOnDifferentDomains_DeterministicDomainTieBreak(t *testing.T) {
	// Domains whose best node has equal free space rank by that node's name.
	cands := []placementCandidate{
		{node: "z", freeGB: 100, domain: "Z"},
		{node: "a", freeGB: 100, domain: "A"},
		{node: "m", freeGB: 100, domain: "M"},
	}
	got, err := pickNodesOnDifferentDomains(cands, 2)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if strings.Join(got, ",") != "a,m" {
		t.Errorf("tie-break should be by node name: got %v, want [a m]", got)
	}
}
