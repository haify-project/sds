package controller

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Generated configs always say "majority", LAN or WAN alike. A numeric quorum is
// absolute from the moment the resource exists, so putting one in the generated
// config blocks the force-promote that gives a brand-new resource its first
// UpToDate generation — "1 of 1 nodes visible, need 2 for quorum". The primary-
// site number is applied afterwards, once the peers are connected.
func TestGeneratedConfigAlwaysUsesQuorumMajority(t *testing.T) {
	ctrl := newBasicTestController(&fakeDeploymentClient{})
	registerWANNodes(ctrl)

	cfg := ctrl.resources.generateDrbdConfig(
		"data", 7300,
		[]resolvedVolume{{id: 0, volumeName: "data_data", pool: "vg0", minor: 0, sizeGB: 1}},
		[]string{"node-a", "node-b", "node-dr"}, nil, "C", "lvm", nil,
		&wanConfig{DRNode: "node-dr", PrimaryNodes: []string{"node-a", "node-b"}},
	)
	assert.Contains(t, cfg, "quorum majority;",
		"a numeric quorum in the generated config would block the initial force-promote")
}

// The whole point of the change is that it is invisible to a LAN cluster. Every
// node of one can take over, so every node should have a say in whether taking
// over is safe — "majority" is right and must not move.
func TestLANResourceKeepsQuorumMajority(t *testing.T) {
	ctrl := newBasicTestController(&fakeDeploymentClient{})
	registerWANNodes(ctrl)

	for _, tc := range []struct {
		name            string
		nodes, diskless []string
	}{
		{"two nodes", []string{"node-a", "node-b"}, nil},
		{"two nodes plus tiebreaker", []string{"node-a", "node-b"}, []string{"node-dr"}},
		{"three nodes", []string{"node-a", "node-b", "node-dr"}, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := ctrl.resources.generateDrbdConfig(
				"data", 7100,
				[]resolvedVolume{{id: 0, volumeName: "data_data", pool: "vg0", minor: 0, sizeGB: 1}},
				tc.nodes, tc.diskless, "C", "lvm", nil, nil,
			)
			assert.Contains(t, cfg, "quorum majority;")
			assert.NotRegexp(t, `quorum\s+\d`, cfg, "a LAN resource must never get a numeric quorum")
		})
	}
}

// A DR is an asynchronous copy that is never promoted automatically, so it
// cannot take over — letting it vote on whether the primary site may write is
// backwards, and it costs real availability.
func TestWANResourceSizesQuorumToPrimarySite(t *testing.T) {
	assert.Equal(t, "majority", localSiteQuorum([]string{"a", "b"}, nil, nil),
		"no WAN config means no change")

	wan := &wanConfig{DRNode: "dr"}
	// {a, dr}: one node can take over, so it alone is quorate. The old majority
	// of 2 meant losing the WAN link stopped writes at home — the exact opposite
	// of what an async DR is for.
	assert.Equal(t, "1", localSiteQuorum([]string{"a", "dr"}, nil, wan))
	// {a, b, dr}: two local nodes, majority 2.
	assert.Equal(t, "2", localSiteQuorum([]string{"a", "b", "dr"}, nil, wan))
	// {a, b, e, dr}: three local nodes, still 2 — where "majority" of all four
	// would have demanded 3.
	assert.Equal(t, "2", localSiteQuorum([]string{"a", "b", "e", "dr"}, nil, wan))
	// A diskless tiebreaker is local and does vote.
	assert.Equal(t, "2", localSiteQuorum([]string{"a", "b", "dr"}, []string{"tb"}, wan))
}

// The property that matters: attaching a DR must never make a resource harder to
// keep alive locally.
func TestAddingDRNeverRaisesTheLocalBar(t *testing.T) {
	wan := &wanConfig{DRNode: "dr"}
	for _, localCount := range []int{1, 2, 3, 4, 5} {
		local := make([]string, localCount)
		for i := range local {
			local[i] = string(rune('a' + i))
		}
		lanBar := localCount/2 + 1 // what `majority` demands with no DR

		withDR := append(append([]string{}, local...), "dr")
		got := localSiteQuorum(withDR, nil, wan)

		assert.Equal(t, lanBar, atoiTest(t, got),
			"with %d local nodes, adding a DR must still need %d votes", localCount, lanBar)
	}
}

func TestSetLocalSiteQuorumRewritesExistingLine(t *testing.T) {
	cfg := `resource r {
    options {
        auto-promote no;
        quorum majority;
        on-no-quorum io-error;
    }
}
`
	out := setLocalSiteQuorum(cfg, 3)
	assert.Contains(t, out, "quorum 2;")
	assert.NotContains(t, out, "quorum majority;")
	// Indentation and the neighbouring options survive.
	assert.Contains(t, out, "        quorum 2;")
	assert.Contains(t, out, "on-no-quorum io-error;")
}

// A config relying on DRBD's built-in default has nothing to rewrite; leaving it
// implicit would let the new member shift a majority nobody ever wrote down.
func TestSetLocalSiteQuorumAddsWhenAbsent(t *testing.T) {
	cfg := "resource r {\n    options {\n        auto-promote no;\n    }\n}\n"
	out := setLocalSiteQuorum(cfg, 2)
	assert.Contains(t, out, "quorum 2;")
	assert.Equal(t, 1, strings.Count(out, "quorum"))
}

func atoiTest(t *testing.T, s string) int {
	t.Helper()
	n := 0
	for _, r := range s {
		require.True(t, r >= '0' && r <= '9', "expected a number, got %q", s)
		n = n*10 + int(r-'0')
	}
	return n
}
