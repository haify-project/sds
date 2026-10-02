package controller

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"time"

	"go.uber.org/zap"

	"github.com/haify-project/sds/pkg/database"
	"github.com/haify-project/sds/pkg/deployment"
	"github.com/haify-project/sds/pkg/event"
	"github.com/haify-project/sds/pkg/inspect"
)

// alertWindow is how far back the inspection looks for alerts that reached
// no channel: one daily schedule's worth.
const alertWindow = 24 * time.Hour

// gather builds the inspection input: the database's view of the cluster plus
// one probe round to every node. Nothing here fails the run; what could not
// be read is left empty and the checks report it.
func (im *InspectionManager) gather(ctx context.Context) *inspect.Input {
	c := im.controller
	in := &inspect.Input{
		Now:         im.now(),
		Probes:      map[string]*inspect.NodeProbe{},
		ProbeErrors: map[string]string{},
	}
	if c.config != nil {
		in.PoolNearFull = c.config.Alert.PoolNearFullPercent
		in.PoolFull = c.config.Alert.PoolFullPercent
	}
	if nodes, err := c.nodes.ListNodes(ctx); err == nil {
		for _, n := range nodes {
			in.Nodes = append(in.Nodes, inspect.Node{Name: n.Name, Address: n.Address,
				ReplicationAddress: n.ReplicationAddress, Hostname: n.Hostname})
		}
	}
	im.probe(ctx, in)
	im.gatherResources(ctx, in)
	im.gatherAlerts(ctx, in)
	im.gatherBackups(ctx, in)
	im.gatherTLS(in)
	if reports, err := im.List(ctx, 0); err == nil {
		for _, r := range reports {
			if len(r.Pools) > 0 {
				in.Previous = r
				break
			}
		}
	}
	return in
}

// probe runs inspect.ProbeScript on every node in one SSH round.
func (im *InspectionManager) probe(ctx context.Context, in *inspect.Input) {
	c := im.controller
	if len(in.Nodes) == 0 {
		return
	}
	addrs := make([]string, 0, len(in.Nodes))
	for _, n := range in.Nodes {
		addrs = append(addrs, n.Address)
	}
	cmd := "echo " + base64Std(inspect.ProbeScript) + " | base64 -d | sudo /bin/bash"
	in.ProbeStart = im.now()
	res, err := c.deployment.Exec(ctx, addrs, cmd, deployment.WithExecTimeout(inspectionProbeTimeout))
	in.ProbeEnd = im.now()
	for _, n := range in.Nodes {
		if err != nil {
			in.ProbeErrors[n.Name] = err.Error()
			continue
		}
		var hr *deployment.HostResult
		if res != nil {
			hr = res.Hosts[n.Address]
		}
		if hr == nil {
			in.ProbeErrors[n.Name] = "no result from " + n.Address
			continue
		}
		// A probe that ran but exited non-zero still printed what it found;
		// only output that is not the probe's at all is a failure.
		p, perr := inspect.ParseProbe(hr.Output)
		if perr != nil {
			msg := perr.Error()
			if hr.Error != nil {
				msg = hr.Error.Error() + ": " + msg
			}
			in.ProbeErrors[n.Name] = msg
			continue
		}
		in.Probes[n.Name] = p
	}
}

// nodeRef maps a stored node reference (name, hostname or address) to the
// registered node name.
func (im *InspectionManager) nodeRef(ref string) string {
	if name := im.controller.nodes.GetNodeNameByAddress(ref); name != "" {
		return name
	}
	return ref
}

func (im *InspectionManager) nodeRefs(refs []string) []string {
	out := make([]string, 0, len(refs))
	for _, r := range refs {
		if r = strings.TrimSpace(r); r != "" {
			out = append(out, im.nodeRef(r))
		}
	}
	return out
}

func (im *InspectionManager) gatherResources(ctx context.Context, in *inspect.Input) {
	c := im.controller
	served := map[string]string{}
	if has, err := c.db.ListHaConfigs(ctx); err == nil {
		for _, h := range has {
			served[h.Resource] = "ha"
		}
	}
	if _, err := c.db.GetHaConfig(ctx, SelfHaResource); err == nil {
		served[SelfHaResource] = "self-ha"
	}
	if gws, err := c.db.ListGateways(ctx); err == nil {
		for _, g := range gws {
			in.Gateways = append(in.Gateways, inspect.Gateway{Resource: g.Resource, Type: string(g.Type), Status: g.Status})
		}
	}
	infos, err := c.resources.ListResources(ctx)
	if err != nil {
		c.logger.Warn("Inspection: list resources", zap.Error(err))
		return
	}
	for _, r := range infos {
		res := inspect.Resource{
			Name:            r.Name,
			Diskful:         im.nodeRefs(r.Nodes),
			Tiebreakers:     im.nodeRefs(r.DisklessNodes),
			Clients:         im.nodeRefs(r.DisklessClients),
			ServedBy:        served[r.Name],
			QuorumRisk:      r.QuorumRisk,
			FaultDomainRisk: r.FaultDomainRisk,
		}
		in.Resources = append(in.Resources, res)
		if res.ServedBy == "self-ha" {
			in.SelfHA = &inspect.SelfHAInput{Resource: r.Name, Nodes: res.Diskful}
		}
	}
	if served[SelfHaResource] == "self-ha" && in.SelfHA == nil {
		in.SelfHA = &inspect.SelfHAInput{Resource: SelfHaResource}
	}
}

// gatherAlerts collects the channels, their delivery records and the alerts
// raised in the window.
func (im *InspectionManager) gatherAlerts(ctx context.Context, in *inspect.Input) {
	c := im.controller
	if c.config == nil || !c.config.Alert.Enabled {
		return
	}
	in.Alerts.Enabled = true
	in.Alerts.Since = in.Now.Add(-alertWindow)
	records, _ := c.db.ListNotifyDeliveries(ctx)
	target := func(name string, enabled bool, filter event.Filter) inspect.DeliveryTarget {
		t := inspect.DeliveryTarget{Name: name, Enabled: enabled, Accepts: func(e inspect.AlertEvent) bool {
			return filter.Match(event.Event{Type: event.Type(e.Type), Severity: event.Severity(e.Severity), Resource: e.Resource})
		}}
		if d := records[name]; d != nil {
			t.Known = true
			t.LastSuccess, t.LastFailure, t.LastError = d.LastSuccess, d.LastFailure, d.LastError
			t.ConsecutiveFailures = d.ConsecutiveFailures
			t.Failed = map[uint64]bool{}
			for _, id := range d.FailedEvents {
				t.Failed[id] = true
			}
		}
		return t
	}
	if channels, err := c.db.ListNotifyChannels(ctx); err == nil {
		for _, ch := range channels {
			cfg, err := webhookConfigFor(ch)
			if err != nil {
				in.Alerts.Targets = append(in.Alerts.Targets, inspect.DeliveryTarget{Name: ch.Name, Enabled: ch.Enabled,
					Known: true, ConsecutiveFailures: 1, LastFailure: in.Now,
					LastError: "the channel's configuration is unusable: " + err.Error(),
					Accepts:   func(inspect.AlertEvent) bool { return false }})
				continue
			}
			t := target(ch.Name, ch.Enabled, cfg.Filter)
			t.Since = ch.CreatedAt
			in.Alerts.Targets = append(in.Alerts.Targets, t)
		}
	}
	for _, wh := range c.config.Alert.Receivers() {
		in.Alerts.Targets = append(in.Alerts.Targets,
			target(receiverName(wh.URL), true, event.Filter{MinSeverity: event.ParseSeverity(wh.MinSeverity)}))
	}
	if c.events == nil {
		return
	}
	for _, e := range c.events.Recent(event.Filter{MinSeverity: event.SeverityWarning}, 0, 0) {
		if e.Status != event.StatusFiring || e.Timestamp.Before(in.Alerts.Since) {
			continue
		}
		in.Alerts.Events = append(in.Alerts.Events, inspect.AlertEvent{ID: e.ID, Type: string(e.Type),
			Severity: string(e.Severity), Resource: e.Resource, Node: e.Node, Message: e.Message, At: e.Timestamp})
	}
}

func (im *InspectionManager) gatherBackups(ctx context.Context, in *inspect.Input) {
	c := im.controller
	b := &in.Backups
	b.SchedulerEnabled = c.config != nil && c.config.Schedule.Enabled
	b.Targets = map[string]bool{}
	b.BaseSnapshots = map[string]bool{}
	if targets, err := c.db.ListBackupTargets(ctx); err == nil {
		for _, t := range targets {
			b.Targets[t.Name] = true
		}
	}
	lastSuccess := map[string]time.Time{}
	if backups, err := c.db.ListBackups(ctx, "", ""); err == nil {
		for _, rec := range backups {
			if rec.State == database.BackupStateRunning {
				b.Running = true
			}
			if rec.State == database.BackupStateCompleted && rec.Schedule != "" && rec.FinishedAt.After(lastSuccess[rec.Schedule]) {
				lastSuccess[rec.Schedule] = rec.FinishedAt
			}
			for _, v := range rec.Volumes {
				if v.Snapshot != "" {
					b.BaseSnapshots[v.Pool+"/"+v.Snapshot] = true
				}
			}
		}
	}
	if scheds, err := c.db.ListBackupSchedules(ctx); err == nil {
		for _, s := range scheds {
			b.Schedules = append(b.Schedules, inspect.BackupSchedule{Name: s.Name, Resource: s.Resource, Target: s.Target,
				Cron: s.Cron, Enabled: s.Enabled, CreatedAt: s.CreatedAt, LastRun: s.LastRun, LastError: s.LastError,
				LastSuccess: lastSuccess[s.Name]})
		}
	}
	if snaps, err := c.db.ListSnapshotSchedules(ctx); err == nil {
		for _, s := range snaps {
			b.SnapSchedules = append(b.SnapSchedules, inspect.SnapSchedule{Name: s.Name, Resource: s.Resource,
				Cron: s.Cron, Enabled: s.Enabled, CreatedAt: s.CreatedAt, LastRun: s.LastRun})
		}
	}
}

// gatherTLS reads the certificates kept on the controller itself.
func (im *InspectionManager) gatherTLS(in *inspect.Input) {
	c := im.controller
	if c.config == nil {
		return
	}
	if c.config.TLS.Enabled && c.config.TLS.CertFile != "" {
		in.TLS.APICertPath = c.config.TLS.CertFile
		if raw, err := os.ReadFile(c.config.TLS.CertFile); err != nil {
			in.TLS.APICertErr = err.Error()
		} else if cert, err := inspect.ParseCertFile(raw); err != nil {
			in.TLS.APICertErr = err.Error()
		} else {
			in.TLS.APICert = cert
		}
	}
	dbPath := c.config.Database.Path
	if dbPath == "" {
		dbPath = database.DefaultDBPath
	}
	caPath := filepath.Join(filepath.Dir(dbPath), "drbd-tls", "ca.pem")
	raw, err := os.ReadFile(caPath)
	if err != nil {
		// No CA: replication TLS was never set up.
		return
	}
	if cert, err := inspect.ParseCertFile(raw); err == nil {
		in.TLS.ReplicationCA, in.TLS.ReplicationCAPath = cert, caPath
	}
}

// receiverName names a controller.toml webhook in delivery records, which
// have no channel name to use.
func receiverName(url string) string {
	host := url
	if i := strings.Index(host, "://"); i >= 0 {
		host = host[i+3:]
	}
	if i := strings.IndexAny(host, "/?"); i >= 0 {
		host = host[:i]
	}
	return "config:" + host
}

// recordDelivery returns the hook that stores a channel's delivery outcomes.
func (c *Controller) recordDelivery(channel string) func(event.Event, error) {
	return func(e event.Event, err error) {
		if c.db == nil || e.Type == event.TypeChannelTest {
			return
		}
		msg := ""
		if err != nil {
			msg = err.Error()
		}
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if werr := c.db.RecordNotifyDelivery(ctx, channel, e.ID, time.Now(), msg); werr != nil {
			c.logger.Warn("Could not record a delivery result", zap.String("channel", channel), zap.Error(werr))
		}
	}
}
