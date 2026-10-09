package controller

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"

	"github.com/haify-project/haify/pkg/alert"
	"github.com/haify-project/haify/pkg/config"
	"github.com/haify-project/haify/pkg/database"
	"github.com/haify-project/haify/pkg/event"
)

type fakeAnomalyActions struct {
	mu       sync.Mutex
	frozen   []string
	snapshot chan string
}

func (f *fakeAnomalyActions) FreezeSchedule(_ context.Context, resource string, d time.Duration, _ string) (time.Time, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.frozen = append(f.frozen, resource)
	return time.Now().Add(d), nil
}

func (f *fakeAnomalyActions) SnapshotNow(_ context.Context, resource string) error {
	f.snapshot <- resource
	return nil
}

func writtenObs(resource, node string, kib uint64, resync bool) alert.Observation {
	st := alert.NodeStateInfo{Role: "Primary", DiskState: "UpToDate", WrittenKiB: &kib}
	peer := alert.NodeStateInfo{Role: "Secondary", DiskState: "UpToDate", ReplicationState: "Established", Connection: "Connected"}
	if resync {
		peer.ReplicationState = "SyncSource"
	}
	return alert.Observation{Resources: alert.ResourceObservation{
		SourceStatus: alert.SourceStatus{Enabled: true, OK: true},
		Items: []alert.ResourceStatusInfo{{Name: resource,
			NodeStates: map[string]alert.NodeStateInfo{node: st, "peer": peer}}},
	}}
}

// A resource that suddenly writes many times its learned rate, for more than
// one poll, freezes its snapshots and takes one more; going back to normal
// resolves the event and leaves the freeze in place.
func TestWriteAnomalyFiresFreezesAndResolves(t *testing.T) {
	acts := &fakeAnomalyActions{snapshot: make(chan string, 1)}
	clock := time.Date(2026, 10, 4, 12, 0, 0, 0, time.UTC)
	d := &writeAnomalyDetector{
		cfg:     config.WriteAnomalyConfig{Enabled: true, Factor: 5, MinMBps: 20, FreezeHours: 168},
		actions: acts, events: event.NewBus(20), log: zap.NewNop(), ctx: context.Background(),
		now: func() time.Time { return clock }, tracks: map[string]*writeTrack{},
	}
	var kib uint64
	poll := func(mbPerSec uint64) {
		clock = clock.Add(30 * time.Second)
		kib += mbPerSec * 1024 * 30
		d.Observed(writtenObs("db", "n1", kib, false))
	}

	d.Observed(writtenObs("db", "n1", kib, false))
	for i := 0; i < writeLearnSamples; i++ {
		poll(10) // normal: 10 MB/s
	}
	assert.Empty(t, d.events.Recent(event.Filter{}, 0, 10))

	poll(200) // one burst is not a rewrite
	assert.Empty(t, d.events.Recent(event.Filter{}, 0, 10))
	poll(200)
	evs := d.events.Recent(event.Filter{}, 0, 10)
	require.Len(t, evs, 1)
	assert.Equal(t, event.TypeResourceWriteAnomaly, evs[0].Type)
	assert.Equal(t, event.StatusFiring, evs[0].Status)
	assert.Contains(t, evs[0].Message, "frozen until")
	assert.Equal(t, []string{"db"}, acts.frozen)
	select {
	case r := <-acts.snapshot:
		assert.Equal(t, "db", r)
	case <-time.After(2 * time.Second):
		t.Fatal("no snapshot taken")
	}

	before := d.tracks["db"].base.Global.Mean
	for i := 0; i < 5; i++ {
		poll(200)
	}
	assert.Equal(t, before, d.tracks["db"].base.Global.Mean, "the attack is not learned as normal")
	assert.Len(t, acts.frozen, 1, "fires once")

	for i := 0; i < writeCalmPolls; i++ {
		poll(10)
	}
	evs = d.events.Recent(event.Filter{}, 0, 10)
	assert.Equal(t, event.StatusResolved, evs[len(evs)-1].Status)
}

// Nothing is judged before the resource's normal is known, and resync
// traffic — DRBD's, not the application's — is never a sample.
func TestWriteAnomalyLearnsFirstAndSkipsResync(t *testing.T) {
	clock := time.Date(2026, 10, 4, 12, 0, 0, 0, time.UTC)
	d := &writeAnomalyDetector{cfg: config.WriteAnomalyConfig{Enabled: true, Factor: 5, MinMBps: 20},
		events: event.NewBus(10), log: zap.NewNop(), ctx: context.Background(),
		now: func() time.Time { return clock }, tracks: map[string]*writeTrack{}}
	var kib uint64
	for i := 0; i < 10; i++ {
		clock = clock.Add(30 * time.Second)
		kib += 500 * 1024 * 30
		d.Observed(writtenObs("db", "n1", kib, i%2 == 0))
	}
	assert.Empty(t, d.events.Recent(event.Filter{}, 0, 10), "an untrained resource does not fire")
	assert.Less(t, d.tracks["db"].base.Global.N, 10, "resync polls were skipped")

	// A counter that went down — DRBD came up again — starts over.
	tr := d.tracks["db"]
	_, ok := tr.sample("n1", 0, clock.Add(time.Minute))
	assert.False(t, ok)
	_, ok = tr.sample("n2", 100, clock.Add(2*time.Minute))
	assert.False(t, ok, "another answering node is another counter")
}

// A frozen schedule keeps every scheduled snapshot of its resource, whatever
// its age, and cannot be deleted; replacing it keeps the freeze; lifting it
// lifts all of that.
func TestFrozenScheduleKeepsEverything(t *testing.T) {
	dep := &fakeDeploymentClient{}
	ctrl := newBasicTestController(dep)
	ctrl.db = newTestDB(t)
	ctx := context.Background()
	require.NoError(t, ctrl.db.SaveResource(ctx, &database.Resource{Name: "data", Port: 7000, Nodes: "n1,n2"}))
	require.NoError(t, ctrl.db.SaveVolume(ctx, &database.Volume{ResourceName: "data", VolumeName: "data_data", Pool: "vg0"}))
	require.NoError(t, ctrl.schedules.CreateSchedule(ctx, "data", "0 * * * *", database.GFSPolicy{Hourly: 2}, true, nil))

	old := snapAt(time.Now().AddDate(0, -3, 0))
	assert.NotContains(t, errString(ctrl.snapshots.DeleteSnapshot(ctx, "vg0/data_data", old, "n1")), "frozen")

	until, err := ctrl.schedules.FreezeSchedule(ctx, "data", 0, "write anomaly")
	require.NoError(t, err)
	assert.WithinDuration(t, time.Now().Add(defaultFreeze), until, time.Minute)
	assert.ErrorContains(t, ctrl.snapshots.DeleteSnapshot(ctx, "vg0/data_data", old, "n1"), "frozen")
	assert.ErrorContains(t, ctrl.schedules.DeleteSchedule(ctx, "data"), "locked until")
	assert.ErrorContains(t, ctrl.resources.DeleteResource(ctx, "data", true), "deleting the resource")

	shorter, err := ctrl.schedules.FreezeSchedule(ctx, "data", time.Hour, "again")
	require.NoError(t, err)
	assert.True(t, until.Equal(shorter), "a freeze is only ever extended")

	require.NoError(t, ctrl.schedules.CreateSchedule(ctx, "data", "30 * * * *", database.GFSPolicy{Hourly: 2}, true, nil))
	s, _ := ctrl.db.GetSnapshotSchedule(ctx, "data")
	assert.False(t, scheduleFrozenUntil(s, lockNow()).IsZero(), "replacing the schedule keeps the freeze")

	require.NoError(t, ctrl.schedules.UnfreezeSchedule(ctx, "data"))
	assert.NotContains(t, errString(ctrl.snapshots.DeleteSnapshot(ctx, "vg0/data_data", old, "n1")), "frozen")
}

func errString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

func TestParseWrittenFromStatusJSON(t *testing.T) {
	out := `[{"name":"data","role":"Primary","devices":[{"volume":0,"disk-state":"UpToDate","written":1000},` +
		`{"volume":1,"disk-state":"UpToDate","written":24}],"connections":[]}]`
	states, err := parseNodeStatesFromJSON(out, "n1")
	require.NoError(t, err)
	require.NotNil(t, states["n1"].WrittenKiB)
	assert.EqualValues(t, 1024, *states["n1"].WrittenKiB)

	partial := `[{"name":"data","role":"Primary","devices":[{"volume":0,"written":1000},{"volume":1}],"connections":[]}]`
	states, err = parseNodeStatesFromJSON(partial, "n1")
	require.NoError(t, err)
	assert.Nil(t, states["n1"].WrittenKiB, "a partial sum would read as a drop in writes")
}
