package alert

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/haify-project/sds/pkg/event"
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
		// Connected again but still catching up: not a complete copy yet.
		{NodeStateInfo{DiskState: "Inconsistent", ReplicationState: "SyncTarget"}, "Inconsistent"},
		{NodeStateInfo{DiskState: "Outdated"}, "Outdated"},
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

// A replica DRBD has disconnected is a replica that protects nothing, and it
// is invisible unless the connection state itself is checked: a peer that is
// not Connected reports no disk state to fall through to.
//
// Measured: a replica of the openclaw resource sat StandAlone from 2026-09-20
// 02:54, after DRBD refused to resolve a split brain, and no alert fired for a
// day. StandAlone is not transient — DRBD stays there until someone acts.
func TestDisconnectedReplicaIsDegraded(t *testing.T) {
	standalone := NodeStateInfo{Connection: "StandAlone"}
	degraded, reason := isDegraded(standalone)
	require.True(t, degraded, "a StandAlone replica must be reported as degraded")
	assert.Contains(t, reason, "StandAlone")

	connecting := NodeStateInfo{Connection: "Connecting"}
	degraded, reason = isDegraded(connecting)
	require.True(t, degraded, "a replica whose link is down must be reported as degraded")
	assert.Contains(t, reason, "Connecting")

	// The node that answered the query has no connection to describe, and its
	// own disk state is the thing to judge it on.
	local := NodeStateInfo{DiskState: "UpToDate", ReplicationState: "Established", Role: "Primary"}
	degraded, _ = isDegraded(local)
	assert.False(t, degraded, "the answering node carries no Connection and must not be called degraded for it")
}

// Two replicas that both say UpToDate but hold different data are only ever
// visible as out-of-sync on an Established connection. The same counter on a
// disconnected or resyncing peer is catch-up, which degrade already reports.
func TestOutOfSyncFiresOnlyOnEstablishedPeers(t *testing.T) {
	diff := healthy("Secondary")
	diff.Connection = "Connected"
	diff.OutOfSyncKiB = 12
	catchingUp := NodeStateInfo{DiskState: "Outdated", ReplicationState: "Off", Role: "", Connection: "Connecting", OutOfSyncKiB: 4096}
	lister := &mockLister{list: []ResourceStatusInfo{{
		Name:       "res1",
		NodeStates: map[string]NodeStateInfo{"n1": healthy("Primary"), "n2": diff, "n3": catchingUp},
	}}}
	mon, drain := newHarness(t, Options{Resources: lister})
	ctx := context.Background()

	mon.Poll(ctx)
	var oos []event.Event
	for _, e := range drain() {
		if e.Type == event.TypeResourceOutOfSync {
			oos = append(oos, e)
		}
	}
	require.Len(t, oos, 1)
	assert.Equal(t, "n2", oos[0].Node)
	assert.Equal(t, "12", oos[0].Details["out_of_sync_kib"])

	diff.OutOfSyncKiB = 0
	lister.list[0].NodeStates["n2"] = diff
	mon.Poll(ctx)
	var resolved bool
	for _, e := range drain() {
		if e.Type == event.TypeResourceOutOfSync && e.Status == event.StatusResolved {
			resolved = true
		}
	}
	assert.True(t, resolved)
}

// Event-driven polling may only relax while the cluster is quiet: a resync or
// a degraded replica changes without any event, and must keep being polled.
func TestIdleIntervalOnlyWhenEventDrivenAndSteady(t *testing.T) {
	lister := &mockLister{list: []ResourceStatusInfo{{
		Name: "res1", NodeStates: map[string]NodeStateInfo{"n1": healthy("Primary"), "n2": healthy("Secondary")},
	}}}
	mon, _ := newHarness(t, Options{Resources: lister, Interval: 30 * time.Second})
	ctx := context.Background()

	mon.Poll(ctx)
	assert.Equal(t, 30*time.Second, mon.nextInterval(), "not event-driven yet")
	mon.SetEventDriven(true)
	assert.Equal(t, DefaultIdleInterval, mon.nextInterval())

	half := 40.0
	syncing := healthy("Secondary")
	syncing.SyncPercent = &half
	lister.list[0].NodeStates["n2"] = syncing
	mon.Poll(ctx)
	assert.Equal(t, 30*time.Second, mon.nextInterval(), "a resync in progress is polled at the normal rate")

	stuck := NodeStateInfo{DiskState: "Outdated", ReplicationState: "Established", Role: "Secondary"}
	lister.list[0].NodeStates["n2"] = stuck
	mon.Poll(ctx)
	assert.Equal(t, DefaultIdleInterval, mon.nextInterval(),
		"a replica stuck degraded changes only by an event; it must not pin the short interval for good")

	lister.list[0].NodeStates["n2"] = healthy("Secondary")
	mon.Poll(ctx)
	mon.SetEventDriven(false)
	assert.Equal(t, 30*time.Second, mon.nextInterval(), "a node whose events stopped arriving is polled at the normal rate")
}

func TestKickPollsSoon(t *testing.T) {
	old := kickSettle
	kickSettle = 10 * time.Millisecond
	defer func() { kickSettle = old }()

	lister := &mockLister{list: []ResourceStatusInfo{{
		Name: "res1", NodeStates: map[string]NodeStateInfo{"n1": healthy("Primary")},
	}}}
	mon, _ := newHarness(t, Options{Resources: lister, Interval: time.Hour})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	mon.Start(ctx)
	require.Eventually(t, func() bool { return mon.Polls() == 1 }, time.Second, 5*time.Millisecond)
	for range 5 {
		mon.Kick()
	}
	require.Eventually(t, func() bool { return mon.Polls() >= 2 }, time.Second, 5*time.Millisecond)
	time.Sleep(50 * time.Millisecond)
	assert.LessOrEqual(t, mon.Polls(), 3, "a burst of kicks is one or two polls, not five")
}

// A warning that clears within the hold is never raised; one that outlasts it
// is raised once. A critical is raised at once regardless.
func TestWarningsWaitOutTheHold(t *testing.T) {
	down := func() NodeStateInfo {
		s := healthy("Secondary")
		s.Connection = "Connecting"
		s.ReplicationState = "StandAlone"
		return s
	}
	lister := &mockLister{list: []ResourceStatusInfo{{
		Name:       "r",
		NodeStates: map[string]NodeStateInfo{"n1": healthy("Primary"), "n2": healthy("Secondary")},
	}}}
	mon, drain := newHarness(t, Options{Resources: lister, WarningHold: 30 * time.Second})
	clock := time.Unix(1000, 0)
	mon.now = func() time.Time { return clock }
	ctx := context.Background()
	mon.Poll(ctx)
	drain()

	// A blip: down for 10s, then back. Nothing at all is published.
	lister.list[0].NodeStates["n2"] = down()
	mon.Poll(ctx)
	clock = clock.Add(10 * time.Second)
	mon.Poll(ctx)
	lister.list[0].NodeStates["n2"] = healthy("Secondary")
	clock = clock.Add(5 * time.Second)
	mon.Poll(ctx)
	assert.Empty(t, drain(), "a link back within the hold is not news")

	// Down for longer than the hold: raised exactly once.
	lister.list[0].NodeStates["n2"] = down()
	mon.Poll(ctx)
	assert.Empty(t, drain())
	assert.True(t, mon.holding(), "a held warning keeps the monitor on its short interval")
	clock = clock.Add(31 * time.Second)
	mon.Poll(ctx)
	evts := drain()
	require.Len(t, evts, 1)
	assert.Equal(t, event.SeverityWarning, evts[0].Severity)
	assert.Equal(t, event.StatusFiring, evts[0].Status)
}
