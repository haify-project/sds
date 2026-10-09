package alert

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/haify-project/sds/pkg/event"
)

type mockPoolLister struct {
	list []PoolStatusInfo
	err  error
}

func (m *mockPoolLister) GetPoolStatusList(context.Context) ([]PoolStatusInfo, error) {
	return m.list, m.err
}

// poolAt builds a thin pool report at the given data utilisation.
func poolAt(data float64) PoolStatusInfo {
	return PoolStatusInfo{Name: "sds_sdspool", Node: "node-e", ThinPool: "sdsthin", DataPercent: data}
}

// poolHarness wires a monitor with only a pool lister. Resources is required by
// the Monitor's contract but nil is tolerated, and leaving it out keeps
// resource events out of the assertions.
func poolHarness(t *testing.T, pools *mockPoolLister) (*Monitor, func() []event.Event) {
	t.Helper()
	return newHarness(t, Options{Pools: pools})
}

func typesOf(evts []event.Event) []event.Type {
	out := make([]event.Type, 0, len(evts))
	for _, e := range evts {
		out = append(out, e.Type)
	}
	return out
}

func TestPoolUnderThresholdIsSilent(t *testing.T) {
	// 45% is the steady state of a healthy pool in this cluster. The whole
	// point of the change is that it must not look like the 100% case, and it
	// must not produce noise either.
	mon, drain := poolHarness(t, &mockPoolLister{list: []PoolStatusInfo{poolAt(45.50)}})
	mon.Poll(context.Background())

	assert.Empty(t, drain())
}

func TestPoolNearFullFiresWarningOnceAndResolves(t *testing.T) {
	lister := &mockPoolLister{list: []PoolStatusInfo{poolAt(91.02)}}
	mon, drain := poolHarness(t, lister)

	mon.Poll(context.Background())
	evts := drain()
	require.Len(t, evts, 1)
	assert.Equal(t, event.TypePoolDataNearFull, evts[0].Type)
	assert.Equal(t, event.SeverityWarning, evts[0].Severity)
	assert.Equal(t, event.StatusFiring, evts[0].Status)
	assert.Equal(t, "sds_sdspool", evts[0].Resource)
	assert.Equal(t, "node-e", evts[0].Node)
	assert.Contains(t, evts[0].Message, "91.02")
	assert.Equal(t, "91.02", evts[0].Details["percent"])

	// A level condition must not re-announce itself every poll.
	mon.Poll(context.Background())
	assert.Empty(t, drain())

	lister.list = []PoolStatusInfo{poolAt(45.00)}
	mon.Poll(context.Background())
	evts = drain()
	require.Len(t, evts, 1)
	assert.Equal(t, event.StatusResolved, evts[0].Status)
	assert.Equal(t, event.SeverityInfo, evts[0].Severity)
}

func TestPoolCrossingIntoCriticalSwapsConditions(t *testing.T) {
	// The reason near-full and full are separate types: a single type would
	// have fired once as a warning and never re-announced when it became
	// critical, because a level condition publishes its severity only when it
	// starts. Crossing must resolve the warning and raise the critical.
	lister := &mockPoolLister{list: []PoolStatusInfo{poolAt(88.00)}}
	mon, drain := poolHarness(t, lister)

	mon.Poll(context.Background())
	require.Equal(t, []event.Type{event.TypePoolDataNearFull}, typesOf(drain()))

	lister.list = []PoolStatusInfo{poolAt(97.00)}
	mon.Poll(context.Background())
	evts := drain()
	require.Len(t, evts, 2)

	byType := map[event.Type]event.Event{}
	for _, e := range evts {
		byType[e.Type] = e
	}
	full, ok := byType[event.TypePoolDataFull]
	require.True(t, ok, "critical must be raised")
	assert.Equal(t, event.StatusFiring, full.Status)
	assert.Equal(t, event.SeverityCritical, full.Severity)

	near, ok := byType[event.TypePoolDataNearFull]
	require.True(t, ok, "warning must be resolved, not left outstanding")
	assert.Equal(t, event.StatusResolved, near.Status)
}

func TestPoolMetadataIsItsOwnCondition(t *testing.T) {
	// Metadata is sized once at pool creation and does not grow with the pool,
	// so it can be critical while data is nearly empty. Reporting only data
	// would miss the failure entirely.
	mon, drain := poolHarness(t, &mockPoolLister{list: []PoolStatusInfo{{
		Name: "sds_sdspool", Node: "node-b", ThinPool: "sdsthin",
		DataPercent: 12.00, MetaPercent: 96.50,
	}}})

	mon.Poll(context.Background())
	evts := drain()
	require.Len(t, evts, 1)
	assert.Equal(t, event.TypePoolMetadataFull, evts[0].Type)
	assert.Equal(t, event.SeverityCritical, evts[0].Severity)
	assert.Equal(t, "metadata", evts[0].Details["dimension"])
}

func TestPoolOutOfSpaceFiresRegardlessOfThresholds(t *testing.T) {
	// LVM's own flag, not a threshold. This is the state node-a was in on
	// 2026-08-09 when DRBD dropped its disk.
	lister := &mockPoolLister{list: []PoolStatusInfo{{
		Name: "sds_sdspool", Node: "node-a", ThinPool: "sdsthin",
		DataPercent: 100.00, MetaPercent: 14.00, OutOfSpace: true,
	}}}
	mon, drain := poolHarness(t, lister)

	mon.Poll(context.Background())
	types := typesOf(drain())
	assert.Contains(t, types, event.TypePoolOutOfSpace)
	assert.Contains(t, types, event.TypePoolDataFull)

	// Freeing space clears it, which is what the recovery on 2026-08-09 did.
	lister.list = []PoolStatusInfo{{
		Name: "sds_sdspool", Node: "node-a", ThinPool: "sdsthin",
		DataPercent: 61.56, MetaPercent: 11.28,
	}}
	mon.Poll(context.Background())
	for _, e := range drain() {
		assert.Equal(t, event.StatusResolved, e.Status)
	}
}

func TestThickPoolRaisesNothing(t *testing.T) {
	// A group with no thin pool reports vg_free honestly, and Haify drives that
	// to zero on purpose. Alerting on it would fire permanently for every pool
	// in the cluster — the un-clearable wall the package comment warns about.
	mon, drain := poolHarness(t, &mockPoolLister{list: []PoolStatusInfo{
		{Name: "vg0", Node: "node-e"},
	}})
	mon.Poll(context.Background())

	assert.Empty(t, drain())
}

func TestPoolListFailureDoesNotResolveOutstandingAlerts(t *testing.T) {
	// The safety property the owners map exists for: silence from a source that
	// did not answer must not read as "the condition cleared".
	lister := &mockPoolLister{list: []PoolStatusInfo{poolAt(97.00)}}
	mon, drain := poolHarness(t, lister)

	mon.Poll(context.Background())
	require.Equal(t, []event.Type{event.TypePoolDataFull}, typesOf(drain()))

	lister.err = errors.New("ssh timeout")
	mon.Poll(context.Background())
	assert.Empty(t, drain(), "a failed pool listing must not clear the alert")
}

func TestNodePollDoesNotClearPoolAlerts(t *testing.T) {
	// Pool conditions carry both a resource name and a node, so the old
	// "empty resource means node-scoped" rule would have handed them to the
	// node lister — and a healthy node poll would have resolved them.
	pools := &mockPoolLister{list: []PoolStatusInfo{poolAt(97.00)}}
	nodes := &mockNodeLister{list: []NodeStatusInfo{{Name: "node-e", Reachable: true}}}
	mon, drain := newHarness(t, Options{Nodes: nodes, Pools: pools})

	mon.Poll(context.Background())
	require.Equal(t, []event.Type{event.TypePoolDataFull}, typesOf(drain()))

	pools.err = errors.New("unreachable")
	mon.Poll(context.Background())
	assert.Empty(t, drain())
}

func TestVanishedPoolClearsItsAlert(t *testing.T) {
	lister := &mockPoolLister{list: []PoolStatusInfo{poolAt(97.00)}}
	mon, drain := poolHarness(t, lister)

	mon.Poll(context.Background())
	require.Len(t, drain(), 1)

	lister.list = nil
	mon.Poll(context.Background())
	evts := drain()
	require.Len(t, evts, 1)
	assert.Equal(t, event.StatusResolved, evts[0].Status)
	assert.Contains(t, evts[0].Message, "no longer exists")
}

func TestPoolConvertedToThickSaysSoRatherThanDeleted(t *testing.T) {
	lister := &mockPoolLister{list: []PoolStatusInfo{poolAt(97.00)}}
	mon, drain := poolHarness(t, lister)

	mon.Poll(context.Background())
	require.Len(t, drain(), 1)

	// Still listed, no longer thin.
	lister.list = []PoolStatusInfo{{Name: "sds_sdspool", Node: "node-e"}}
	mon.Poll(context.Background())
	evts := drain()
	require.Len(t, evts, 1)
	assert.Equal(t, event.StatusResolved, evts[0].Status)
	assert.Contains(t, evts[0].Message, "no longer holds a thin pool")
}

func TestPoolsOnDifferentNodesAreDistinctConditions(t *testing.T) {
	// Every node in this cluster names its pool "sds_sdspool", so a key without
	// the node would collapse four pools into one alert.
	mon, drain := poolHarness(t, &mockPoolLister{list: []PoolStatusInfo{
		{Name: "sds_sdspool", Node: "node-b", ThinPool: "sdsthin", DataPercent: 97.0},
		{Name: "sds_sdspool", Node: "node-e", ThinPool: "sdsthin", DataPercent: 98.0},
	}})

	mon.Poll(context.Background())
	evts := drain()
	require.Len(t, evts, 2)
	nodes := map[string]bool{}
	for _, e := range evts {
		nodes[e.Node] = true
	}
	assert.Equal(t, map[string]bool{"node-b": true, "node-e": true}, nodes)
}

func TestNoPoolListerRaisesNothing(t *testing.T) {
	mon, drain := newHarness(t, Options{})
	mon.Poll(context.Background())
	assert.Empty(t, drain())
}

func TestNormalizeThresholds(t *testing.T) {
	near, full := normalizeThresholds(0, 0)
	assert.Equal(t, DefaultNearFullPercent, near)
	assert.Equal(t, DefaultFullPercent, full)

	near, full = normalizeThresholds(70, 90)
	assert.Equal(t, 70.0, near)
	assert.Equal(t, 90.0, full)

	// Out of range falls back rather than disabling the check.
	near, _ = normalizeThresholds(150, 90)
	assert.Equal(t, DefaultNearFullPercent, near)

	// A warning at or above the critical would be unreachable; it is pulled
	// below instead of silently dropping the warning condition.
	near, full = normalizeThresholds(95, 90)
	assert.Less(t, near, full)
}

func TestThresholdsAreConfigurable(t *testing.T) {
	mon, drain := newHarness(t, Options{
		Pools:           &mockPoolLister{list: []PoolStatusInfo{poolAt(72.00)}},
		NearFullPercent: 70,
		FullPercent:     80,
	})

	mon.Poll(context.Background())
	evts := drain()
	require.Len(t, evts, 1)
	assert.Equal(t, event.TypePoolDataNearFull, evts[0].Type)
}
