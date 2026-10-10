package alert

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/haify-project/haify/pkg/event"
)

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

// Every replica Outdated means nothing can be promoted: that is an outage, not
// a replica that fell behind, and the alert has to say so at critical.
func TestAllReplicasOutdatedIsCriticalAndNamesTheWayOut(t *testing.T) {
	out := func() NodeStateInfo { s := healthy("Secondary"); s.DiskState = "Outdated"; return s }
	lister := &mockLister{list: []ResourceStatusInfo{{
		Name:       "gw",
		NodeStates: map[string]NodeStateInfo{"n1": out(), "n2": out()},
	}}}
	mon, drain := newHarness(t, Options{Resources: lister})
	mon.Poll(context.Background())
	evts := drain()
	require.NotEmpty(t, evts)
	for _, e := range evts {
		assert.Equal(t, event.SeverityCritical, e.Severity)
		assert.Contains(t, e.Message, "resource primary gw")
	}

	// One replica behind while another is current stays a warning.
	lister.list[0].NodeStates["n2"] = healthy("Primary")
	mon2, drain2 := newHarness(t, Options{Resources: lister})
	mon2.Poll(context.Background())
	for _, e := range drain2() {
		if e.Node == "n1" {
			assert.Equal(t, event.SeverityWarning, e.Severity)
		}
	}
}

// A Kubernetes volume is demoted whenever its pod goes away. That is its
// normal idle state, so it must not raise the critical a control-plane
// resource would.
func TestIdleVolumeLosingItsPrimaryIsNotCritical(t *testing.T) {
	lister := &mockLister{list: []ResourceStatusInfo{{
		Name:               "pvc_x",
		IdleWithoutPrimary: true,
		NodeStates:         map[string]NodeStateInfo{"n1": healthy("Primary")},
	}}}
	mon, drain := newHarness(t, Options{Resources: lister})
	ctx := context.Background()
	mon.Poll(ctx)
	drain()

	lister.list[0].NodeStates["n1"] = healthy("Secondary")
	mon.Poll(ctx)
	for _, e := range drain() {
		assert.NotEqual(t, event.TypeResourceNoPrimary, e.Type, "an unmounted CSI volume is not a failure")
	}
}

// A guest's disk following a live migration moved; nothing failed over.
func TestManagedVolumeMovingIsNotAFailover(t *testing.T) {
	lister := &mockLister{list: []ResourceStatusInfo{{
		Name:               "cinder-v1",
		IdleWithoutPrimary: true,
		NodeStates:         map[string]NodeStateInfo{"n1": healthy("Primary"), "n2": healthy("Secondary")},
	}}}
	mon, drain := newHarness(t, Options{Resources: lister})
	ctx := context.Background()
	mon.Poll(ctx)
	drain()

	lister.list[0].NodeStates = map[string]NodeStateInfo{"n1": healthy("Secondary"), "n2": healthy("Primary")}
	mon.Poll(ctx)
	evs := drain()
	require.Len(t, evs, 1)
	assert.Equal(t, event.TypeResourcePromoted, evs[0].Type)
	assert.Equal(t, event.SeverityInfo, evs[0].Severity)
	assert.Equal(t, map[string]string{"from": "n1", "to": "n2"}, evs[0].Details)
}

// A message names a volume by its claim too: pvc-<uid> alone does not tell an
// operator which application it is.
func TestMessagesNameTheOwner(t *testing.T) {
	lister := &mockLister{list: []ResourceStatusInfo{{
		Name:               "pvc-1234",
		Owner:              "PVC db/data-postgres-0",
		IdleWithoutPrimary: true,
		NodeStates:         map[string]NodeStateInfo{"n1": healthy("Primary"), "n2": healthy("Secondary")},
	}}}
	mon, drain := newHarness(t, Options{Resources: lister})
	ctx := context.Background()
	mon.Poll(ctx)
	drain()

	lister.list[0].NodeStates = map[string]NodeStateInfo{"n1": healthy("Secondary"), "n2": healthy("Primary")}
	mon.Poll(ctx)
	evs := drain()
	require.Len(t, evs, 1)
	assert.Equal(t, "pvc-1234", evs[0].Resource)
	assert.Equal(t, "resource pvc-1234 (PVC db/data-postgres-0) moved: Primary on n2, was n1", evs[0].Message)
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

func TestPrimarySetIsSorted(t *testing.T) {
	res := ResourceStatusInfo{NodeStates: map[string]NodeStateInfo{
		"n3": healthy("Primary"),
		"n1": healthy("Primary"),
		"n2": healthy("Secondary"),
	}}
	assert.Equal(t, "n1,n3", primarySet(res))
}

// A node rejoining the cluster must not raise "no Primary".
//
// This is the 2026-09-21 incident, reproduced. The controller reads a resource
// from whichever node answers its status query first, and a node that has just
// booted describes every peer as one line — "<peer> connection:Connecting" —
// with no role on it. So for the seconds its links take to establish, the view
// it returns contains no Primary anywhere, while the real Primary has been
// serving uninterrupted. The monitor published a CRITICAL saying the control
// plane had lost its Primary; nothing had happened.
func TestRejoiningNodeDoesNotFakeALostPrimary(t *testing.T) {
	lister := &mockLister{list: []ResourceStatusInfo{{
		Name: "res1",
		NodeStates: map[string]NodeStateInfo{
			"n1": healthy("Primary"),
			"n2": healthy("Secondary"),
		},
	}}}
	mon, drain := newHarness(t, Options{Resources: lister})
	ctx := context.Background()

	mon.Poll(ctx)
	drain()

	// n2 reboots and answers the next status query itself. From where it sits,
	// it is Secondary and n1 is merely "Connecting" — which is all DRBD says
	// about a peer whose link is not up, so n1 carries no role at all.
	lister.list[0].NodeStates = map[string]NodeStateInfo{
		"n2": {DiskState: "Outdated", Role: "Secondary"},
		"n1": {Connection: "Connecting"},
	}
	mon.Poll(ctx)

	for _, e := range drain() {
		if e.Type == event.TypeResourceNoPrimary && e.Status == event.StatusFiring {
			t.Fatalf("raised a CRITICAL lost-Primary from a view that never asked n1: %s", e.Message)
		}
	}
}

// And the alert is not merely deferred: once every link is back and the
// Primary is genuinely gone, it still fires.
func TestLostPrimaryStillFiresOnceEveryReplicaAnswered(t *testing.T) {
	lister := &mockLister{list: []ResourceStatusInfo{{
		Name: "res1",
		NodeStates: map[string]NodeStateInfo{
			"n1": healthy("Primary"),
			"n2": healthy("Secondary"),
		},
	}}}
	mon, drain := newHarness(t, Options{Resources: lister})
	ctx := context.Background()

	mon.Poll(ctx)
	drain()

	lister.list[0].NodeStates = map[string]NodeStateInfo{
		"n1": {DiskState: "UpToDate", ReplicationState: "Established", Role: "Secondary", Connection: "Connected"},
		"n2": {DiskState: "UpToDate", ReplicationState: "Established", Role: "Secondary", Connection: "Connected"},
	}
	mon.Poll(ctx)

	var got *event.Event
	for _, e := range drain() {
		if e.Type == event.TypeResourceNoPrimary && e.Status == event.StatusFiring {
			got = &e
		}
	}
	require.NotNil(t, got, "a genuinely demoted Primary must still raise the critical")
	assert.Equal(t, event.SeverityCritical, got.Severity)
}
