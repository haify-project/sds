package inspect

import (
	"strings"
	"testing"
	"time"
)

func TestBackupScheduleFailingLateAndTargetMissing(t *testing.T) {
	in := cluster()
	in.Backups.Targets["s3"] = true
	in.Backups.Schedules = []BackupSchedule{
		{Name: "a@s3", Target: "s3", Cron: "0 2 * * *", Enabled: true, LastError: "rclone: 403",
			LastRun: t0.Add(-23 * time.Hour), LastSuccess: t0.Add(-5 * 24 * time.Hour)},
		{Name: "b@s3", Target: "s3", Cron: "0 2 * * *", Enabled: true, LastSuccess: t0.Add(-40 * time.Hour)},
		{Name: "c@s3", Target: "s3", Cron: "0 2 * * *", Enabled: true, LastSuccess: t0.Add(-4 * 24 * time.Hour)},
		{Name: "d@gone", Target: "gone", Cron: "0 2 * * *", Enabled: true, LastSuccess: t0.Add(-time.Hour)},
		{Name: "e@s3", Target: "s3", Cron: "0 2 * * *", Enabled: true, LastSuccess: t0.Add(-time.Hour)},
		{Name: "f@s3", Target: "s3", Cron: "0 2 * * *", Enabled: false},
	}
	checks := checkBackups(in)
	if c := only(t, checks, "backups.schedule_failing"); c.Subject != "a@s3" || c.Fix != "sds backup schedule run a@s3" {
		t.Errorf("got %+v", c)
	}
	late := find(checks, "backups.schedule_late")
	if len(late) != 2 {
		t.Fatalf("want b and c late:\n%s", dump(checks))
	}
	for _, c := range late {
		want := map[string]Status{"b@s3": StatusWarn, "c@s3": StatusFail}[c.Subject]
		if c.Status != want {
			t.Errorf("%s: %s, want %s", c.Subject, c.Status, want)
		}
	}
	if c := only(t, checks, "backups.target_missing"); c.Subject != "d@gone" {
		t.Errorf("got %+v", c)
	}
}

func TestNeverSucceededScheduleCountsFromCreation(t *testing.T) {
	in := cluster()
	in.Backups.Targets["s3"] = true
	in.Backups.Schedules = []BackupSchedule{{Name: "a@s3", Target: "s3", Cron: "0 * * * *", Enabled: true,
		CreatedAt: t0.Add(-10 * time.Hour)}}
	c := only(t, checkBackups(in), "backups.schedule_late")
	if c.Status != StatusFail || !strings.Contains(c.Message, "no successful run") {
		t.Errorf("got %+v", c)
	}
}

func TestSchedulerOffAndLateSnapshots(t *testing.T) {
	in := cluster()
	in.Backups.SnapSchedules = []SnapSchedule{{Name: "vm", Cron: "0 * * * *", Enabled: true, LastRun: t0.Add(-2 * time.Hour)}}
	if c := only(t, checkBackups(in), "backups.snapshot_schedule_late"); c.Status != StatusWarn {
		t.Errorf("got %+v", c)
	}
	in.Backups.SchedulerEnabled = false
	checks := checkBackups(in)
	only(t, checks, "backups.scheduler_off")
	if len(find(checks, "backups.snapshot_schedule_late")) != 0 {
		t.Errorf("a stopped scheduler is reported once, not per schedule")
	}
}

func TestLeftoverBackupSnapshots(t *testing.T) {
	in := cluster()
	in.Probes["n1"].LVs = []LV{
		{VG: "sds_pool0", Name: "vm_data_bk_20260901T020000Z", Segtype: "thin"},
		{VG: "sds_pool0", Name: "vm_data_bk_20260902T020000Z", Segtype: "thin"},
	}
	in.Backups.BaseSnapshots["sds_pool0/vm_data_bk_20260902T020000Z"] = true
	c := only(t, checkBackups(in), "backups.leftover_base_snapshot")
	if c.Fix != "ssh 10.0.0.1 sudo lvremove -y sds_pool0/vm_data_bk_20260901T020000Z" {
		t.Errorf("got %+v", c)
	}
	in.Backups.Running = true
	if len(find(checkBackups(in), "backups.leftover_base_snapshot")) != 0 {
		t.Errorf("a running backup's snapshot may not be recorded yet")
	}
}
