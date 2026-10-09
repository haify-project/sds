package inspect

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/robfig/cron/v3"
)

// Schedule lateness, in cron intervals since the last success.
const (
	lateWarn = 1.5
	lateFail = 3.0
)

// checkBackups judges scheduled protection: each backup schedule's last
// success against its own cadence, the targets they ship to, snapshot
// schedules still firing, and backup base snapshots nothing refers to.
func checkBackups(in *Input) []Check {
	b := in.Backups
	var out []Check
	enabled := 0
	for _, s := range b.Schedules {
		if s.Enabled {
			enabled++
		}
	}
	for _, s := range b.SnapSchedules {
		if s.Enabled {
			enabled++
		}
	}
	if !b.SchedulerEnabled && enabled > 0 {
		out = append(out, Check{ID: "backups.scheduler_off", Area: AreaBackups, Status: StatusFail,
			Message: fmt.Sprintf("%s enabled but [schedule] enabled = false, so none of them runs", plural(enabled, "schedule is", "schedules are")),
			Fix:     "set [schedule] enabled = true in /etc/haify/controller.toml and restart haify-controller"})
	}
	scheds := append([]BackupSchedule(nil), b.Schedules...)
	sort.Slice(scheds, func(i, j int) bool { return scheds[i].Name < scheds[j].Name })
	for _, s := range scheds {
		if !s.Enabled {
			continue
		}
		out = append(out, backupScheduleChecks(in, s)...)
	}
	snaps := append([]SnapSchedule(nil), b.SnapSchedules...)
	sort.Slice(snaps, func(i, j int) bool { return snaps[i].Name < snaps[j].Name })
	for _, s := range snaps {
		if !s.Enabled || !b.SchedulerEnabled {
			continue
		}
		base, what := s.LastRun, "last run"
		if base.IsZero() {
			base, what = s.CreatedAt, "created, never run,"
		}
		if c, ok := lateness(in.Now, s.Cron, base); ok && c.Status != StatusPass {
			c.ID, c.Subject = "backups.snapshot_schedule_late", s.Name
			c.Message = fmt.Sprintf("snapshot schedule %q (%s) %s %s; %s", s.Name, s.Cron, what, base.UTC().Format(time.RFC3339), c.Message)
			c.Fix = "journalctl -u haify-controller | grep -i 'snapshot schedule'"
			out = append(out, c)
		}
	}
	out = append(out, leftoverBaseSnapshots(in)...)
	if len(out) == 0 && enabled == 0 {
		out = append(out, pass("backups.schedules", AreaBackups, "no backup or snapshot schedules"))
	}
	if len(out) == 0 {
		out = append(out, pass("backups.schedules", AreaBackups,
			"%s on time, no unreferenced backup snapshots", plural(enabled, "schedule", "schedules")))
	}
	return out
}

func backupScheduleChecks(in *Input, s BackupSchedule) []Check {
	var out []Check
	if !in.Backups.Targets[s.Target] {
		out = append(out, Check{ID: "backups.target_missing", Area: AreaBackups, Subject: s.Name, Status: StatusFail,
			Message: fmt.Sprintf("schedule %s ships to target %q, which does not exist; every run fails", s.Name, s.Target),
			Fix:     fmt.Sprintf("haify backup target add --name %s --kind <s3|smb|webdav> <target flags>", s.Target)})
	}
	if s.LastError != "" {
		out = append(out, Check{ID: "backups.schedule_failing", Area: AreaBackups, Subject: s.Name, Status: StatusFail,
			Message:  "the last scheduled backup failed: " + s.LastError,
			Evidence: []string{"last run " + stamp(s.LastRun), "last success " + stamp(s.LastSuccess)},
			Fix:      "haify backup schedule run " + s.Name})
		return out
	}
	base, what := s.LastSuccess, "last success"
	if base.IsZero() {
		base, what = s.CreatedAt, "no successful run since it was created"
	}
	if c, ok := lateness(in.Now, s.Cron, base); ok && c.Status != StatusPass {
		c.ID, c.Subject, c.Fix = "backups.schedule_late", s.Name, "haify backup schedule run "+s.Name
		c.Message = fmt.Sprintf("backup schedule %s (%s): %s %s; %s", s.Name, s.Cron, what, stamp(base), c.Message)
		out = append(out, c)
	}
	return out
}

// lateness compares the time since base with the schedule's interval.
func lateness(now time.Time, spec string, base time.Time) (Check, bool) {
	sched, err := cron.ParseStandard(spec)
	if err != nil || base.IsZero() {
		return Check{}, false
	}
	next := sched.Next(now)
	interval := sched.Next(next).Sub(next)
	if interval <= 0 {
		return Check{}, false
	}
	age := now.Sub(base)
	ratio := float64(age) / float64(interval)
	c := Check{Area: AreaBackups, Status: StatusPass,
		Evidence: []string{fmt.Sprintf("interval %s, %s since, %.1f intervals", interval, age.Round(time.Minute), ratio)}}
	switch {
	case ratio > lateFail:
		c.Status = StatusFail
	case ratio > lateWarn:
		c.Status = StatusWarn
	}
	c.Message = fmt.Sprintf("that is %.1f intervals ago", ratio)
	return c, true
}

// leftoverBaseSnapshots lists `_bk_` snapshots that no backup record keeps
// as an incremental base. Each pins every block it shares with the origin.
func leftoverBaseSnapshots(in *Input) []Check {
	if in.Backups.Running {
		return nil
	}
	var out []Check
	for _, node := range sortedKeys(in.Probes) {
		var orphans []string
		for _, lv := range in.Probes[node].LVs {
			if !managedVG(lv.VG) || !strings.Contains(lv.Name, "_bk_") {
				continue
			}
			key := lv.VG + "/" + lv.Name
			if in.Backups.BaseSnapshots[node+"/"+key] || in.Backups.BaseSnapshots[key] {
				continue
			}
			orphans = append(orphans, key)
		}
		if len(orphans) == 0 {
			continue
		}
		out = append(out, Check{ID: "backups.leftover_base_snapshot", Area: AreaBackups, Subject: node, Status: StatusWarn,
			Message:  fmt.Sprintf("%s on %s no backup record refers to; each holds thin pool space", plural(len(orphans), "backup snapshot", "backup snapshots"), node),
			Evidence: orphans,
			Fix:      fmt.Sprintf("ssh %s sudo lvremove -y %s", sshTarget(in, node), strings.Join(orphans, " "))})
	}
	return out
}

func stamp(t time.Time) string {
	if t.IsZero() {
		return "never"
	}
	return t.UTC().Format(time.RFC3339)
}
