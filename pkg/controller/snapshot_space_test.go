package controller

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/haify-project/sds/pkg/deployment"
)

// A pool past the near-full line gives up the volume's oldest scheduled
// snapshots, oldest first, until it is back under — and never its newest two.
func TestRelieveThinPoolRemovesOldestUntilUnderThreshold(t *testing.T) {
	snaps := []string{
		"data_data_sched_20260927T110000Z", "data_data_sched_20260920T230000Z",
		"data_data_sched_20260928T100000Z", "data_data_sched_20260925T230000Z",
	}
	percent := 100.0
	var removed []string
	dep := &fakeDeploymentClient{}
	dep.lvsThinReportFunc = func(_ context.Context, hosts []string, vg string) (*deployment.ExecResult, error) {
		return successExecResult(hosts, fmt.Sprintf("  %s|pool_thin|thin-pool|21474836480|%.2f|10.00|twi-aotz--", vg, percent)), nil
	}
	dep.lvListSnapshotsFunc = func(_ context.Context, hosts []string, _ string) (*deployment.ExecResult, error) {
		var lines []string
		for _, s := range snaps {
			if !contains(removed, s) {
				lines = append(lines, s+"|1|2026-09-28 10:00:00 +0000|data_data")
			}
		}
		return successExecResult(hosts, strings.Join(lines, "\n")), nil
	}
	dep.lvRemoveSnapshotFunc = func(_ context.Context, hosts []string, _, name string) (*deployment.ExecResult, error) {
		removed = append(removed, name)
		percent -= 10 // each old snapshot held a tenth of the pool
		return successExecResult(hosts, ""), nil
	}
	ctrl := newBasicTestController(dep)
	vol := &ResourceVolumeInfo{Pool: "sds_pool", BackingVolume: "data_data", Device: "/dev/sds_pool/data_data"}

	ctrl.schedules.relieveThinPool(context.Background(), "n1", "n1", "r", vol, 0)
	want := []string{"data_data_sched_20260920T230000Z", "data_data_sched_20260925T230000Z"}
	if strings.Join(removed, ",") != strings.Join(want, ",") {
		t.Fatalf("removed %v, want the two oldest %v and then stop once under the threshold", removed, want)
	}

	// A pool that stays full: everything but the newest two goes, no more.
	removed = nil
	dep.lvRemoveSnapshotFunc = func(_ context.Context, hosts []string, _, name string) (*deployment.ExecResult, error) {
		removed = append(removed, name)
		percent = 100
		return successExecResult(hosts, ""), nil
	}
	percent = 100
	ctrl.schedules.relieveThinPool(context.Background(), "n1", "n1", "r", vol, 0)
	if strings.Join(removed, ",") != strings.Join(want, ",") {
		t.Fatalf("removed %v; the newest two must survive a pool that stays full", removed)
	}
	dep.lvRemoveSnapshotFunc = func(_ context.Context, hosts []string, _, name string) (*deployment.ExecResult, error) {
		removed = append(removed, name)
		return successExecResult(hosts, ""), nil
	}

	removed, percent = nil, 70
	ctrl.schedules.relieveThinPool(context.Background(), "n1", "n1", "r", vol, 0)
	if len(removed) != 0 {
		t.Fatalf("a pool under the threshold loses nothing, removed %v", removed)
	}
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}
