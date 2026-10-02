package inspect

import (
	"strings"
	"testing"
	"time"
)

func acceptsWarning(e AlertEvent) bool { return e.Severity == "warning" || e.Severity == "critical" }

func TestAlertsDisabledAndNoChannel(t *testing.T) {
	in := cluster()
	in.Alerts.Enabled = false
	if c := only(t, checkAlerts(in), "alerts.disabled"); c.Status != StatusWarn {
		t.Errorf("got %+v", c)
	}
	in.Alerts.Enabled = true
	if c := only(t, checkAlerts(in), "alerts.no_channel"); c.Status != StatusFail {
		t.Errorf("got %+v", c)
	}
}

// The only channel pointed at a dead webhook, and the CRITICAL that fired
// every 30 minutes for two hours went nowhere.
func TestDeadWebhookAndUndeliveredCritical(t *testing.T) {
	in := cluster()
	in.Alerts.Since = t0.Add(-24 * time.Hour)
	in.Alerts.Targets = []DeliveryTarget{{
		Name: "ops", Enabled: true, Accepts: acceptsWarning, Known: true,
		LastSuccess: t0.Add(-72 * time.Hour), LastFailure: t0.Add(-time.Minute),
		LastError: "dial tcp 192.168.1.5:8080: connect: no route to host", ConsecutiveFailures: 4,
		Failed: map[uint64]bool{11: true, 12: true},
	}}
	in.Alerts.Events = []AlertEvent{
		{ID: 11, Type: "resource.no_primary", Severity: "critical", Resource: "blk", At: t0.Add(-2 * time.Hour)},
		{ID: 12, Type: "resource.no_primary", Severity: "critical", Resource: "blk", At: t0.Add(-90 * time.Minute)},
	}
	checks := checkAlerts(in)
	if c := only(t, checks, "alerts.channel_failing"); c.Subject != "ops" || c.Fix != "sds channel test ops" {
		t.Errorf("got %+v", c)
	}
	c := only(t, checks, "alerts.undelivered")
	if c.Status != StatusFail || len(c.Evidence) != 2 {
		t.Errorf("got %+v", c)
	}
}

func TestEventNoChannelAccepts(t *testing.T) {
	in := cluster()
	in.Alerts.Targets = []DeliveryTarget{{Name: "pager", Enabled: true,
		Accepts: func(e AlertEvent) bool { return e.Severity == "critical" }}}
	in.Alerts.Events = []AlertEvent{{ID: 3, Type: "pool.data_near_full", Severity: "warning", At: t0}}
	c := only(t, checkAlerts(in), "alerts.undelivered")
	if c.Status != StatusWarn {
		t.Errorf("only warnings were lost: %+v", c)
	}
}

func TestAlertsDeliveringIsAPass(t *testing.T) {
	in := cluster()
	in.Alerts.Targets = []DeliveryTarget{
		{Name: "ops", Enabled: true, Accepts: acceptsWarning, Known: true, LastSuccess: t0, Failed: map[uint64]bool{}},
		{Name: "config:hooks.example", Enabled: true, Accepts: acceptsWarning},
		{Name: "muted", Enabled: false},
	}
	in.Alerts.Events = []AlertEvent{{ID: 5, Type: "resource.degraded", Severity: "warning", At: t0}}
	checks := checkAlerts(in)
	if len(checks) != 1 || checks[0].ID != "alerts.channel_muted" {
		t.Errorf("want only the muted channel noted:\n%s", dump(checks))
	}
	// One channel failing one event while another delivered it is not lost.
	in.Alerts.Targets[1].Known = true
	in.Alerts.Targets[1].Failed = map[uint64]bool{5: true}
	if len(find(checkAlerts(in), "alerts.undelivered")) != 0 {
		t.Errorf("event 5 reached ops")
	}
}

// A channel added today did not deliver yesterday's alerts: on orange the
// first channel was created after 20 alerts had already gone nowhere, and the
// first run after it reported them all as accepted.
func TestAlertsRaisedBeforeTheChannelExistedReachedNoOne(t *testing.T) {
	in := cluster()
	in.Alerts.Targets = []DeliveryTarget{
		{Name: "ops", Enabled: true, Accepts: acceptsWarning, Known: true, LastSuccess: t0, Since: t0},
	}
	in.Alerts.Events = []AlertEvent{
		{ID: 1, Type: "resource.no_primary", Severity: "critical", At: t0.Add(-time.Hour)},
		{ID: 2, Type: "resource.degraded", Severity: "warning", At: t0.Add(time.Minute)},
	}
	lost := find(checkAlerts(in), "alerts.undelivered")
	if len(lost) != 1 || lost[0].Status != StatusFail || len(lost[0].Evidence) != 1 ||
		!strings.Contains(lost[0].Evidence[0], "before a channel") {
		t.Fatalf("want only the earlier critical reported:\n%s", dump(lost))
	}
}
