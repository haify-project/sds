package controller

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/haify-project/sds/pkg/config"
	"github.com/haify-project/sds/pkg/database"
	"github.com/haify-project/sds/pkg/deployment"
)

// A node going offline is dated, and the date is cleared when it answers.
func TestMarkHealthDatesOffline(t *testing.T) {
	ctrl := newBasicTestController(&fakeDeploymentClient{})
	nm := ctrl.nodes
	nm.nodes["10.0.0.9"] = &NodeInfo{Name: "n9", Address: "10.0.0.9", State: NodeStateOnline}
	nm.markHealth("10.0.0.9", NodeStateOffline)
	since := nm.nodes["10.0.0.9"].OfflineSince
	require.False(t, since.IsZero())
	nm.markHealth("10.0.0.9", NodeStateOffline)
	assert.Equal(t, since, nm.nodes["10.0.0.9"].OfflineSince, "the first failure dates it")
	nm.markHealth("10.0.0.9", NodeStateOnline)
	assert.True(t, nm.nodes["10.0.0.9"].OfflineSince.IsZero())

	nm.nodes["10.0.0.9"].State = NodeStateEvicted
	nm.markHealth("10.0.0.9", NodeStateOnline)
	assert.Equal(t, NodeStateEvicted, nm.nodes["10.0.0.9"].State, "an evicted node stays evicted until restored")
}

// Auto-evict acts only on nodes offline long enough, and not at all when so
// many are offline that the controller may be the one cut off.
func TestAutoEvictCandidates(t *testing.T) {
	ctrl := newBasicTestController(&fakeDeploymentClient{})
	ctrl.config = &config.Config{SelfHeal: config.SelfHealConfig{AutoEvict: "on", AfterMinutes: 60, MaxOfflinePercent: 34}}
	e := &autoEvictor{c: ctrl, announced: map[string]bool{}}
	now := time.Now()
	node := func(name string, offline time.Duration, labels map[string]string) *NodeInfo {
		n := &NodeInfo{Name: name, State: NodeStateOnline, Labels: labels}
		if offline > 0 {
			n.OfflineSince = now.Add(-offline)
			n.State = NodeStateOffline
		}
		return n
	}
	nodes := []*NodeInfo{node("a", 2*time.Hour, nil), node("b", 0, nil), node("c", 0, nil), node("d", 0, nil)}
	due, gate := e.evictCandidates(nodes, now)
	assert.Empty(t, gate)
	require.Len(t, due, 1)
	assert.Equal(t, "a", due[0].Name)

	nodes[0].OfflineSince = now.Add(-10 * time.Minute)
	due, _ = e.evictCandidates(nodes, now)
	assert.Empty(t, due, "a reboot is not a failure")

	nodes[0].Labels = map[string]string{autoEvictOptOut: "false"}
	nodes[0].OfflineSince = now.Add(-2 * time.Hour)
	due, _ = e.evictCandidates(nodes, now)
	assert.Empty(t, due, "opted out")

	nodes = []*NodeInfo{node("a", 2*time.Hour, nil), node("b", 2*time.Hour, nil), node("c", 0, nil), node("d", 0, nil)}
	due, gate = e.evictCandidates(nodes, now)
	assert.Empty(t, due)
	assert.Contains(t, gate, "looks like a partition")
}

// Adding a replica with a member away is refused unless allowed, and only
// with the majority answering.
func TestUnreachableMembers(t *testing.T) {
	dep := &fakeDeploymentClient{}
	dep.execFunc = func(_ context.Context, hosts []string, cmd string, _ ...deployment.ExecOption) (*deployment.ExecResult, error) {
		if cmd == "true" && hosts[0] == "n3" {
			return failedExecResult(hosts, "unreachable"), nil
		}
		return successExecResult(hosts, ""), nil
	}
	ctrl := newBasicTestController(dep)
	res := &database.Resource{Name: "db"}
	_, err := ctrl.resources.unreachableMembers(context.Background(), res, []string{"n1", "n2", "n3"}, nil, false)
	assert.ErrorContains(t, err, "allow-unreachable")
	away, err := ctrl.resources.unreachableMembers(context.Background(), res, []string{"n1", "n2", "n3"}, nil, true)
	require.NoError(t, err)
	assert.Equal(t, []string{"n3"}, away)
	_, err = ctrl.resources.unreachableMembers(context.Background(), res, []string{"n1", "n3"}, nil, true)
	assert.ErrorContains(t, err, "needs the majority")
}

func TestWithPreferredNodes(t *testing.T) {
	conf := "[[promoter]]\n[promoter.resources.db]\nrunner = \"systemd\"\nstart = [\n]\n"
	out := withPreferredNodes(conf, []string{"node-a", "node-b"}, "start-only")
	assert.Contains(t, out, "[promoter.resources.db]\npreferred-nodes = [\"node-a\", \"node-b\"]\npreferred-nodes-policy = \"start-only\"\nrunner")
	again := withPreferredNodes(out, []string{"node-b"}, "")
	assert.Equal(t, 1, strings.Count(again, "preferred-nodes ="))
	assert.NotContains(t, again, "policy")
	assert.Equal(t, conf, withPreferredNodes(out, nil, ""), "an empty list removes it")
}

func TestSpread(t *testing.T) {
	hi, lo := spread(map[string]uint64{"a": 30, "b": 10, "c": 20})
	assert.Equal(t, "a", hi)
	assert.Equal(t, "b", lo)
}

func TestRebalanceNeverMovesToAFullerPool(t *testing.T) {
	// sdt3 holds the least by allocation but its thin pool is the most
	// written: moving a replica there would fill the node that has least room.
	thin := func(pct float64) *PoolInfo {
		return &PoolInfo{ThinUsage: &PoolThinInfo{SizeBytes: 10 << 30, DataPercent: pct}}
	}
	f3, _ := poolFill(thin(81.6))
	f2, _ := poolFill(thin(46))
	fill := map[string]float64{"sdt2": f2, "sdt3": f3}
	if !fuller(fill, "sdt3", "sdt2") {
		t.Fatal("sdt3 at 81.6% must count as fuller than sdt2 at 46%")
	}
	if fuller(fill, "sdt2", "sdt3") {
		t.Fatal("sdt2 is the emptier pool and may take the replica")
	}
	if fuller(fill, "sdt1", "sdt2") {
		t.Fatal("a node with no reported fill must not be refused")
	}
	if _, ok := poolFill(&PoolInfo{ThinUsage: &PoolThinInfo{}}); ok {
		t.Fatal("a thin pool reported without its size has no known fill")
	}
	thick, ok := poolFill(&PoolInfo{TotalBytes: 100, FreeBytes: 25})
	if !ok || thick != 0.75 {
		t.Fatalf("thick pool fill = %v, %v; want 0.75", thick, ok)
	}
}

// A resource's tiebreaker is never picked to take a replica: AddReplica and
// MoveReplica refuse it, so a plan that named it could never run.
func TestSelectAdditionalReplicasPassesOverBarredNodes(t *testing.T) {
	ctx := context.Background()
	ctrl := newPlacementTestCluster(t, "  sds_vg0|214748364800|214748364800|/dev/vdb", "")

	got, err := ctrl.resources.selectAdditionalReplicas(ctx, "vg0", 10, 1, nil, nil, nil, nil)
	if err != nil || join(got) != "n1" {
		t.Fatalf("unbarred pick = %v, %v; want n1 on name order", got, err)
	}
	got, err = ctrl.resources.selectAdditionalReplicas(ctx, "vg0", 10, 1, nil, []string{"n1"}, nil, nil)
	if err != nil || join(got) != "n2" {
		t.Fatalf("pick with n1 barred = %v, %v; want n2", got, err)
	}
	if _, err := ctrl.resources.selectAdditionalReplicas(ctx, "vg0", 10, 1, []string{"n2"}, []string{"n1"}, nil, nil); err == nil {
		t.Fatal("with n2 a replica and n1 barred there is no node left")
	}
	r := &database.Resource{DisklessNodes: "n1", DisklessClients: "n3,n4"}
	if got := join(nonReplicaMembers(r)); got != "n1,n3,n4" {
		t.Fatalf("nonReplicaMembers = %s", got)
	}
}
