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
