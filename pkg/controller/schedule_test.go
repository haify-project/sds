package controller

import (
	"context"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/haify-project/sds/pkg/config"
	"github.com/haify-project/sds/pkg/database"
	"github.com/haify-project/sds/pkg/deployment"
)

func TestParseSnapNameRoundTrip(t *testing.T) {
	ts := time.Date(2024, 3, 9, 2, 30, 0, 0, time.UTC)
	name := buildSnapName("data_data", ts)
	assert.Equal(t, "data_data_sched_20240309T023000Z", name)

	vol, got, ok := parseSnapName(name)
	require.True(t, ok)
	assert.Equal(t, "data_data", vol)
	assert.True(t, got.Equal(ts))
}

func TestParseSnapNameRejectsNonScheduled(t *testing.T) {
	for _, n := range []string{"data_data", "manual-backup", "_sched_notatimestamp", "data_data_sched_"} {
		_, _, ok := parseSnapName(n)
		assert.False(t, ok, "should reject %q", n)
	}
}

func snap(name string, ts time.Time) scheduledSnap { return scheduledSnap{Name: name, TS: ts} }

func expiredNames(in []scheduledSnap) []string {
	out := make([]string, 0, len(in))
	for _, s := range in {
		out = append(out, s.Name)
	}
	sort.Strings(out)
	return out
}

func TestSelectExpiredHourly(t *testing.T) {
	base := time.Date(2024, 1, 1, 10, 0, 0, 0, time.UTC)
	snaps := []scheduledSnap{
		snap("s10", base),
		snap("s11", base.Add(1*time.Hour)),
		snap("s12", base.Add(2*time.Hour)),
		snap("s13", base.Add(3*time.Hour)),
		snap("s14", base.Add(4*time.Hour)),
	}
	expired := selectExpiredSnapshots(snaps, database.GFSPolicy{Hourly: 3})
	// keep the 3 newest hours (12,13,14); expire 10,11
	assert.Equal(t, []string{"s10", "s11"}, expiredNames(expired))
}

func TestSelectExpiredDailyBucketsKeepNewestPerDay(t *testing.T) {
	snaps := []scheduledSnap{
		snap("d1a", time.Date(2024, 1, 1, 1, 0, 0, 0, time.UTC)),
		snap("d1b", time.Date(2024, 1, 1, 5, 0, 0, 0, time.UTC)),
		snap("d1c", time.Date(2024, 1, 1, 9, 0, 0, 0, time.UTC)),
		snap("d2a", time.Date(2024, 1, 2, 2, 0, 0, 0, time.UTC)),
		snap("d2b", time.Date(2024, 1, 2, 8, 0, 0, 0, time.UTC)),
	}
	expired := selectExpiredSnapshots(snaps, database.GFSPolicy{Daily: 2})
	// keep newest of each of the 2 newest days: d2b (Jan2), d1c (Jan1)
	assert.Equal(t, []string{"d1a", "d1b", "d2a"}, expiredNames(expired))
}

func TestSelectExpiredUnionAcrossBuckets(t *testing.T) {
	snaps := []scheduledSnap{
		snap("j1_09", time.Date(2024, 1, 1, 9, 0, 0, 0, time.UTC)),
		snap("j2_02", time.Date(2024, 1, 2, 2, 0, 0, 0, time.UTC)),
		snap("j2_08", time.Date(2024, 1, 2, 8, 0, 0, 0, time.UTC)),
	}
	// hourly:2 keeps the 2 newest hours (j2_08, j2_02); daily:2 keeps newest of
	// Jan2 (j2_08) and Jan1 (j1_09). Union covers all three -> nothing expired.
	expired := selectExpiredSnapshots(snaps, database.GFSPolicy{Hourly: 2, Daily: 2})
	assert.Empty(t, expired)
}

func TestSelectExpiredAllZeroKeepsEverything(t *testing.T) {
	snaps := []scheduledSnap{
		snap("a", time.Date(2024, 1, 1, 1, 0, 0, 0, time.UTC)),
		snap("b", time.Date(2024, 1, 2, 1, 0, 0, 0, time.UTC)),
	}
	assert.Empty(t, selectExpiredSnapshots(snaps, database.GFSPolicy{}))
	assert.Empty(t, selectExpiredSnapshots(nil, database.GFSPolicy{Daily: 7}))
}

func TestCowSize(t *testing.T) {
	assert.Equal(t, "256M", cowSize(0))   // floor
	assert.Equal(t, "256M", cowSize(1))   // 204M -> floor
	assert.Equal(t, "2048M", cowSize(10)) // 20% of 10G
	assert.Equal(t, "20480M", cowSize(100))
}

func TestValidateCronAndNextRun(t *testing.T) {
	require.NoError(t, validateCron("0 2 * * *"))
	require.Error(t, validateCron("not a cron"))
	require.Error(t, validateCron("0 2 * *")) // too few fields

	now := time.Date(2024, 1, 1, 1, 0, 0, 0, time.UTC)
	next := NextRun("0 2 * * *", now)
	assert.Equal(t, time.Date(2024, 1, 1, 2, 0, 0, 0, time.UTC), next)
}

// recordingDeploy captures LVCreateSnapshot calls for assertions.
type recordingDeploy struct {
	*fakeDeploymentClient
	mu    sync.Mutex
	snaps []struct{ host, vg, name string }
}

func (r *recordingDeploy) LVCreateSnapshot(ctx context.Context, hosts []string, vgName, lvName, snapshotName, size string) (*deployment.ExecResult, error) {
	r.mu.Lock()
	for _, h := range hosts {
		r.snaps = append(r.snaps, struct{ host, vg, name string }{h, vgName, snapshotName})
	}
	r.mu.Unlock()
	return successExecResult(hosts, ""), nil
}

func TestRunScheduleSnapshotsDiskfulNodesOnly(t *testing.T) {
	dep := &recordingDeploy{fakeDeploymentClient: &fakeDeploymentClient{}}
	ctrl := newBasicTestController(dep)
	ctrl.config = &config.Config{Schedule: config.ScheduleConfig{Enabled: true}}
	registerNodes(ctrl, map[string]string{
		"orange1": "10.0.0.1",
		"orange2": "10.0.0.2",
		"orange3": "10.0.0.3",
	})

	db := newTestDB(t)
	ctrl.db = db

	// 2 diskful nodes + 1 diskless tiebreaker, one volume.
	require.NoError(t, db.SaveResource(context.Background(), &database.Resource{
		Name: "r1", Port: 7000, Nodes: "orange1,orange2", DisklessNodes: "orange3", Replicas: 2,
	}))
	require.NoError(t, db.SaveVolume(context.Background(), &database.Volume{
		ResourceName: "r1", VolumeName: "r1_data", VolumeID: 0, Pool: "sds_vg0",
		Device: "/dev/sds_vg0/r1_data", SizeGB: 1,
	}))
	require.NoError(t, db.SaveSnapshotSchedule(context.Background(), &database.SnapshotSchedule{
		Name: "r1", Resource: "r1", Cron: "0 * * * *", Enabled: true,
		Keep: database.GFSPolicy{Hourly: 24},
	}))

	ctrl.schedules.runSchedule("r1")

	// Snapshot taken on the two diskful nodes, never on the diskless tiebreaker.
	hosts := map[string]bool{}
	for _, s := range dep.snaps {
		hosts[s.host] = true
		assert.Equal(t, "sds_vg0", s.vg)
		_, _, ok := parseSnapName(s.name)
		assert.True(t, ok, "snapshot name should be scheduler-managed: %s", s.name)
	}
	assert.True(t, hosts["10.0.0.1"], "expected snapshot on orange1")
	assert.True(t, hosts["10.0.0.2"], "expected snapshot on orange2")
	assert.False(t, hosts["10.0.0.3"], "tiebreaker must not be snapshotted")

	// LastRun recorded.
	got, err := db.GetSnapshotSchedule(context.Background(), "r1")
	require.NoError(t, err)
	assert.False(t, got.LastRun.IsZero())
}
