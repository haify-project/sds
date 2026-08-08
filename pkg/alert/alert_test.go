package alert

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/liliang-cn/sds/pkg/event"
)

type mockLister struct {
	list []ResourceStatusInfo
	err  error
}

func (m *mockLister) GetResourceStatusList(context.Context) ([]ResourceStatusInfo, error) {
	return m.list, m.err
}

type mockNodeLister struct {
	list []NodeStatusInfo
	err  error
}

func (m *mockNodeLister) GetNodeStatusList(context.Context) ([]NodeStatusInfo, error) {
	return m.list, m.err
}

// newHarness wires a monitor to a bus and drains everything published so far.
func newHarness(t *testing.T, opts Options) (*Monitor, func() []event.Event) {
	t.Helper()
	bus := event.NewBus(100)
	mon := NewMonitor(bus, opts)

	var seen uint64
	drain := func() []event.Event {
		evts := bus.Recent(event.Filter{}, seen, 0)
		if n := len(evts); n > 0 {
			seen = evts[n-1].ID
		}
		return evts
	}
	return mon, drain
}

func healthy(role string) NodeStateInfo {
	return NodeStateInfo{DiskState: "UpToDate", ReplicationState: "Established", Role: role}
}

func TestDegradedReplicaFiresOnceAndResolves(t *testing.T) {
	lister := &mockLister{list: []ResourceStatusInfo{{
		Name: "res1",
		NodeStates: map[string]NodeStateInfo{
			"n1": healthy("Primary"),
			"n2": {DiskState: "Diskless", ReplicationState: "StandAlone", Role: "Secondary"},
		},
	}}}
	mon, drain := newHarness(t, Options{Resources: lister})
	ctx := context.Background()

	mon.Poll(ctx)
	evts := drain()
	require.Len(t, evts, 1)
	assert.Equal(t, event.TypeResourceDegraded, evts[0].Type)
	assert.Equal(t, event.StatusFiring, evts[0].Status)
	assert.Equal(t, event.SeverityWarning, evts[0].Severity)
	assert.Equal(t, "res1", evts[0].Resource)
	assert.Equal(t, "n2", evts[0].Node)
	assert.Equal(t, "Diskless", evts[0].Details["disk_state"])

	// Still degraded: a level condition must not re-fire every poll, or a
	// receiver gets one page per interval for a single fault.
	mon.Poll(ctx)
	assert.Empty(t, drain(), "an unchanged condition must not re-fire")

	lister.list[0].NodeStates["n2"] = healthy("Secondary")
	mon.Poll(ctx)
	evts = drain()
	require.Len(t, evts, 1)
	assert.Equal(t, event.StatusResolved, evts[0].Status)
	assert.Equal(t, event.SeverityInfo, evts[0].Severity, "a recovery is not itself a problem")
}

// A controller restarting into an already-broken cluster must report the
// breakage, not stay quiet because it never saw the transition.
func TestDegradedFiresOnFirstPollAfterRestart(t *testing.T) {
	mon, drain := newHarness(t, Options{Resources: &mockLister{list: []ResourceStatusInfo{{
		Name:       "res1",
		NodeStates: map[string]NodeStateInfo{"n1": {DiskState: "Failed", Role: "Secondary"}},
	}}}})

	mon.Poll(context.Background())
	evts := drain()
	require.Len(t, evts, 1)
	assert.Equal(t, event.TypeResourceDegraded, evts[0].Type)
}

func TestFailoverDetected(t *testing.T) {
	lister := &mockLister{list: []ResourceStatusInfo{{
		Name: "res1",
		NodeStates: map[string]NodeStateInfo{
			"n1": healthy("Primary"),
			"n2": healthy("Secondary"),
		},
	}}}
	mon, drain := newHarness(t, Options{Resources: lister})
	ctx := context.Background()

	// First poll only learns where the Primary is.
	mon.Poll(ctx)
	assert.Empty(t, drain(), "the first sighting of a Primary is not a failover")

	lister.list[0].NodeStates["n1"] = healthy("Secondary")
	lister.list[0].NodeStates["n2"] = healthy("Primary")
	mon.Poll(ctx)

	evts := drain()
	require.Len(t, evts, 1)
	assert.Equal(t, event.TypeResourceFailover, evts[0].Type)
	assert.Equal(t, event.SeverityWarning, evts[0].Severity)
	// A failover has already happened; there is no later state that clears it.
	// Firing would leave every historical failover outstanding forever.
	assert.Equal(t, event.StatusInfo, evts[0].Status)
	assert.Equal(t, "n1", evts[0].Details["from"])
	assert.Equal(t, "n2", evts[0].Details["to"])
	assert.Contains(t, evts[0].Message, "Primary moved from n1 to n2")

	// Steady state again.
	mon.Poll(ctx)
	assert.Empty(t, drain())
}

// Losing the Primary entirely is worse than moving it: nothing is serving I/O.
func TestNoPrimaryIsCritical(t *testing.T) {
	lister := &mockLister{list: []ResourceStatusInfo{{
		Name:       "res1",
		NodeStates: map[string]NodeStateInfo{"n1": healthy("Primary")},
	}}}
	mon, drain := newHarness(t, Options{Resources: lister})
	ctx := context.Background()

	mon.Poll(ctx)
	drain()

	lister.list[0].NodeStates["n1"] = healthy("Secondary")
	mon.Poll(ctx)
	evts := drain()
	require.Len(t, evts, 1)
	assert.Equal(t, event.TypeResourceNoPrimary, evts[0].Type)
	assert.Equal(t, event.SeverityCritical, evts[0].Severity)
	firingKey := evts[0].Key()

	// The clear must arrive under the SAME type. A receiver pairs firing with
	// resolved by (type, resource); a resolve published as resource.promoted
	// closes nothing, and the critical stays outstanding forever.
	lister.list[0].NodeStates["n1"] = healthy("Primary")
	mon.Poll(ctx)
	evts = drain()
	require.Len(t, evts, 1)
	assert.Equal(t, event.TypeResourceNoPrimary, evts[0].Type)
	assert.Equal(t, event.StatusResolved, evts[0].Status)
	assert.Equal(t, evts[0].Key(), firingKey, "the resolve must carry the firing event's key")
}

// A resource that has never had a Primary is Secondary by design, not in
// trouble. Raising a critical for each of those would bury the real ones.
func TestSecondaryEverywhereIsNotAnAlert(t *testing.T) {
	lister := &mockLister{list: []ResourceStatusInfo{{
		Name:       "res1",
		NodeStates: map[string]NodeStateInfo{"n1": healthy("Secondary")},
	}}}
	mon, drain := newHarness(t, Options{Resources: lister})
	ctx := context.Background()

	mon.Poll(ctx)
	mon.Poll(ctx)
	assert.Empty(t, drain())
}

// Deleting a resource while it has no Primary must clear the critical. Nothing
// evaluates the condition again, so without this it stays raised forever.
func TestNoPrimaryClearsWhenTheResourceIsDeleted(t *testing.T) {
	lister := &mockLister{list: []ResourceStatusInfo{{
		Name:       "res1",
		NodeStates: map[string]NodeStateInfo{"n1": healthy("Primary")},
	}}}
	mon, drain := newHarness(t, Options{Resources: lister})
	ctx := context.Background()

	mon.Poll(ctx)
	lister.list[0].NodeStates["n1"] = healthy("Secondary")
	mon.Poll(ctx)
	drain()

	lister.list = nil
	mon.Poll(ctx)
	evts := drain()
	require.Len(t, evts, 1)
	assert.Equal(t, event.TypeResourceNoPrimary, evts[0].Type)
	assert.Equal(t, event.StatusResolved, evts[0].Status)
}

// Map iteration order must not make a dual-primary resource look like it is
// flapping between its two Primaries on every poll.
func TestDualPrimaryDoesNotFlap(t *testing.T) {
	lister := &mockLister{list: []ResourceStatusInfo{{
		Name: "res1",
		NodeStates: map[string]NodeStateInfo{
			"n1": healthy("Primary"),
			"n2": healthy("Primary"),
		},
	}}}
	mon, drain := newHarness(t, Options{Resources: lister})

	for range 10 {
		mon.Poll(context.Background())
	}
	assert.Empty(t, drain(), "a stable dual-primary resource must be silent")
}

func TestWANDegradedFiresAndResolves(t *testing.T) {
	lister := &mockLister{list: []ResourceStatusInfo{{
		Name:       "res1",
		NodeStates: map[string]NodeStateInfo{"n1": healthy("Primary")},
		WANEnabled: true,
		WANHealthy: false,
		WANMessage: "DR WAN endpoint unreachable",
	}}}
	mon, drain := newHarness(t, Options{Resources: lister})
	ctx := context.Background()

	mon.Poll(ctx)
	evts := drain()
	require.Len(t, evts, 1)
	assert.Equal(t, event.TypeWANDegraded, evts[0].Type)
	assert.Equal(t, event.SeverityCritical, evts[0].Severity)
	assert.Equal(t, "wan", evts[0].Node)
	assert.Contains(t, evts[0].Message, "unreachable")

	mon.Poll(ctx)
	assert.Empty(t, drain())

	lister.list[0].WANHealthy = true
	lister.list[0].WANMessage = ""
	mon.Poll(ctx)
	evts = drain()
	require.Len(t, evts, 1)
	assert.Equal(t, event.StatusResolved, evts[0].Status)
}

func TestNodeUnreachable(t *testing.T) {
	nodes := &mockNodeLister{list: []NodeStatusInfo{
		{Name: "n1", Reachable: true},
		{Name: "n2", Reachable: false, Message: "ssh: connect: no route to host"},
	}}
	mon, drain := newHarness(t, Options{Resources: &mockLister{}, Nodes: nodes})
	ctx := context.Background()

	mon.Poll(ctx)
	evts := drain()
	require.Len(t, evts, 1)
	assert.Equal(t, event.TypeNodeUnreachable, evts[0].Type)
	assert.Equal(t, event.SeverityCritical, evts[0].Severity)
	assert.Equal(t, "n2", evts[0].Node)

	nodes.list[1].Reachable = true
	mon.Poll(ctx)
	evts = drain()
	require.Len(t, evts, 1)
	assert.Equal(t, event.StatusResolved, evts[0].Status)
	assert.Contains(t, evts[0].Message, "reachable again")
}

// Without a NodeLister the monitor must simply not raise node events, rather
// than panicking or claiming every node is down.
func TestNodeChecksOptional(t *testing.T) {
	mon, drain := newHarness(t, Options{Resources: &mockLister{}})
	mon.Poll(context.Background())
	assert.Empty(t, drain())
}

// A deleted resource must not leave a firing alert that can never clear, and
// must not make its recreated namesake look like it failed over.
func TestDeletedResourceClearsAndForgets(t *testing.T) {
	lister := &mockLister{list: []ResourceStatusInfo{{
		Name: "res1",
		NodeStates: map[string]NodeStateInfo{
			"n1": healthy("Primary"),
			"n2": {DiskState: "Diskless", Role: "Secondary"},
		},
	}}}
	mon, drain := newHarness(t, Options{Resources: lister})
	ctx := context.Background()

	mon.Poll(ctx)
	require.Len(t, drain(), 1)

	lister.list = nil
	mon.Poll(ctx)
	evts := drain()
	require.Len(t, evts, 1)
	assert.Equal(t, event.StatusResolved, evts[0].Status)
	assert.Contains(t, evts[0].Message, "no longer exists")

	// Recreated on the other node: that is a new resource, not a failover.
	lister.list = []ResourceStatusInfo{{
		Name:       "res1",
		NodeStates: map[string]NodeStateInfo{"n2": healthy("Primary")},
	}}
	mon.Poll(ctx)
	assert.Empty(t, drain(), "a recreated resource must not report a failover")
}

func TestListErrorsAreSurvivable(t *testing.T) {
	mon, drain := newHarness(t, Options{
		Resources: &mockLister{err: errors.New("db closed")},
		Nodes:     &mockNodeLister{err: errors.New("unreachable")},
	})
	mon.Poll(context.Background())
	assert.Empty(t, drain())
	assert.Equal(t, 1, mon.Polls())
}

func TestStartStopIsIdempotent(t *testing.T) {
	mon, _ := newHarness(t, Options{Resources: &mockLister{}, Interval: 5 * time.Millisecond})
	mon.Start(context.Background())

	require.Eventually(t, func() bool { return mon.Polls() >= 2 }, time.Second, 5*time.Millisecond,
		"Start must poll immediately and then on the interval")

	// Stop used to close a channel directly, so a second call panicked.
	mon.Stop()
	mon.Stop()
}

func TestPrimarySetIsSorted(t *testing.T) {
	res := ResourceStatusInfo{NodeStates: map[string]NodeStateInfo{
		"n3": healthy("Primary"),
		"n1": healthy("Primary"),
		"n2": healthy("Secondary"),
	}}
	assert.Equal(t, "n1,n3", primarySet(res))
}

func TestIsDegradedStates(t *testing.T) {
	for _, tc := range []struct {
		state NodeStateInfo
		want  string
	}{
		{NodeStateInfo{DiskState: "Failed"}, "Failed"},
		{NodeStateInfo{DiskState: "Detached"}, "Detached"},
		{NodeStateInfo{DiskState: "Diskless"}, "Diskless"},
		{NodeStateInfo{DiskState: "UpToDate", ReplicationState: "Disconnecting"}, "Disconnecting"},
		{NodeStateInfo{DiskState: "UpToDate", ReplicationState: "Unconnected"}, "Unconnected"},
		{NodeStateInfo{DiskState: "UpToDate", ReplicationState: "StandAlone"}, "StandAlone"},
	} {
		deg, reason := isDegraded(tc.state)
		assert.True(t, deg, tc.want)
		assert.Contains(t, reason, tc.want)
	}

	deg, reason := isDegraded(healthy("Primary"))
	assert.False(t, deg)
	assert.Empty(t, reason)
}

// A quorum tiebreaker and a diskless client are Diskless by design. Alerting on
// them produces a warning that can never clear — and since auto_tiebreaker is
// on by default, one per two-replica resource.
func TestExpectedDisklessIsNotDegraded(t *testing.T) {
	mon, drain := newHarness(t, Options{Resources: &mockLister{list: []ResourceStatusInfo{{
		Name: "mysqlha",
		NodeStates: map[string]NodeStateInfo{
			"orange1": healthy("Primary"),
			"orange2": healthy("Secondary"),
			// The auto-added tiebreaker.
			"orange3": {DiskState: "Diskless", ReplicationState: "Established", Role: "Secondary", ExpectedDiskless: true},
		},
	}}}})

	mon.Poll(context.Background())
	assert.Empty(t, drain(), "a tiebreaker being Diskless is its healthy state")
}

// The exemption is narrow: it covers Diskless only, and only the disk state.
func TestExpectedDisklessStillReportsRealFaults(t *testing.T) {
	for name, st := range map[string]NodeStateInfo{
		"failed disk":  {DiskState: "Failed", ExpectedDiskless: true},
		"detached":     {DiskState: "Detached", ExpectedDiskless: true},
		"disconnected": {DiskState: "Diskless", ReplicationState: "StandAlone", ExpectedDiskless: true},
	} {
		deg, reason := isDegraded(st)
		assert.True(t, deg, "%s must still be reported", name)
		assert.NotEmpty(t, reason)
	}

	// And a node that is NOT diskless by design losing its disk is still a fault.
	deg, reason := isDegraded(NodeStateInfo{DiskState: "Diskless"})
	assert.True(t, deg)
	assert.Contains(t, reason, "Diskless")
}

// A degraded replica is a common reason to reach for remove-replica, so the
// stuck alert lands on exactly the workflow that provokes it: the resource
// still exists, so a "resource deleted" check never fires, and nothing
// evaluates the departed node's condition again.
func TestRemovedReplicaClearsItsAlert(t *testing.T) {
	lister := &mockLister{list: []ResourceStatusInfo{{
		Name: "data",
		NodeStates: map[string]NodeStateInfo{
			"n1": healthy("Primary"),
			"n2": healthy("Secondary"),
			"n3": {DiskState: "Failed", Role: "Secondary"},
		},
	}}}
	mon, drain := newHarness(t, Options{Resources: lister})
	ctx := context.Background()

	mon.Poll(ctx)
	require.Len(t, drain(), 1)

	delete(lister.list[0].NodeStates, "n3")
	mon.Poll(ctx)

	evts := drain()
	require.Len(t, evts, 1)
	assert.Equal(t, event.StatusResolved, evts[0].Status)
	assert.Equal(t, "n3", evts[0].Node)
	assert.Equal(t, "data", evts[0].Resource)
	assert.Contains(t, evts[0].Message, "no longer part of resource data")

	// And it stays quiet afterwards rather than re-resolving every poll.
	mon.Poll(ctx)
	assert.Empty(t, drain())
}

func TestUnregisteredNodeClearsItsAlert(t *testing.T) {
	nodes := &mockNodeLister{list: []NodeStatusInfo{
		{Name: "n1", Reachable: true},
		{Name: "n2", Reachable: false, Message: "no route to host"},
	}}
	mon, drain := newHarness(t, Options{Resources: &mockLister{}, Nodes: nodes})
	ctx := context.Background()

	mon.Poll(ctx)
	require.Len(t, drain(), 1)

	nodes.list = nodes.list[:1]
	mon.Poll(ctx)

	evts := drain()
	require.Len(t, evts, 1)
	assert.Equal(t, event.StatusResolved, evts[0].Status)
	assert.Contains(t, evts[0].Message, "no longer registered")
}

// The safety property: a source that could not be listed reports nothing, and
// nothing must never be read as "everything cleared". Otherwise one transient
// database error resolves every alert in the cluster.
func TestListFailureDoesNotResolveAnything(t *testing.T) {
	lister := &mockLister{list: []ResourceStatusInfo{{
		Name:       "data",
		NodeStates: map[string]NodeStateInfo{"n1": {DiskState: "Failed", Role: "Secondary"}},
	}}}
	nodes := &mockNodeLister{list: []NodeStatusInfo{{Name: "n9", Reachable: false}}}
	mon, drain := newHarness(t, Options{Resources: lister, Nodes: nodes})
	ctx := context.Background()

	mon.Poll(ctx)
	require.Len(t, drain(), 2, "one degraded replica, one unreachable node")

	lister.err = errors.New("db closed")
	nodes.err = errors.New("dispatch unavailable")
	mon.Poll(ctx)
	assert.Empty(t, drain(), "a failed list must not resolve outstanding alerts")

	// When the source comes back and the fault is genuinely gone, it resolves.
	lister.err = nil
	nodes.err = nil
	lister.list[0].NodeStates["n1"] = healthy("Secondary")
	nodes.list[0].Reachable = true
	mon.Poll(ctx)
	assert.Len(t, drain(), 2)
}

// One source failing must not block the other from resolving its own.
func TestOneFailedSourceDoesNotBlockTheOther(t *testing.T) {
	lister := &mockLister{list: []ResourceStatusInfo{{
		Name:       "data",
		NodeStates: map[string]NodeStateInfo{"n1": {DiskState: "Failed", Role: "Secondary"}},
	}}}
	nodes := &mockNodeLister{list: []NodeStatusInfo{{Name: "n9", Reachable: false}}}
	mon, drain := newHarness(t, Options{Resources: lister, Nodes: nodes})
	ctx := context.Background()

	mon.Poll(ctx)
	require.Len(t, drain(), 2)

	// Resources still answer and the resource is gone; nodes are unavailable.
	lister.list = nil
	nodes.err = errors.New("dispatch unavailable")
	mon.Poll(ctx)

	evts := drain()
	require.Len(t, evts, 1, "the resource alert clears; the node alert is left alone")
	assert.Equal(t, "data", evts[0].Resource)
}
