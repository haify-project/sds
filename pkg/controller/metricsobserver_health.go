package controller

import (
	"context"

	"github.com/liliang-cn/sds/pkg/alert"
	"github.com/liliang-cn/sds/pkg/database"
	"github.com/liliang-cn/sds/pkg/metrics"
)

type backupLister interface {
	ListBackups(ctx context.Context, resource, target string) ([]*database.Backup, error)
}

type resourceLister interface {
	ListResources(ctx context.Context) ([]*ResourceInfo, error)
}

// observeHealth exports what the alerting decides on: which nodes answered,
// how full each thin pool is, what is raised, how fresh the backups are, and
// which resources one machine's loss would take down.
func (o *metricsObserver) observeHealth(obs alert.Observation) {
	if obs.Nodes.Enabled && obs.Nodes.OK {
		reach := make([]metrics.NodeReach, 0, len(obs.Nodes.Items))
		for _, n := range obs.Nodes.Items {
			reach = append(reach, metrics.NodeReach{Node: n.Name, Reachable: n.Reachable})
		}
		o.metrics.SetNodeReachability(reach)
	}
	if obs.Pools.Enabled && obs.Pools.OK {
		var thin []metrics.ThinUsage
		for _, p := range obs.Pools.Items {
			if p.ThinPool != "" {
				thin = append(thin, metrics.ThinUsage{Pool: p.Name, Node: p.Node, DataPercent: p.DataPercent, MetaPercent: p.MetaPercent})
			}
		}
		o.metrics.SetThinPoolUsage(thin)
	}
	firing := make([]string, 0, len(obs.Firing))
	for _, f := range obs.Firing {
		firing = append(firing, f.Type+"/"+f.Severity)
	}
	o.metrics.SetFiringAlerts(firing)
	o.observeBackups()
	o.observeFaultDomains()
}

// observeBackups exports the newest completed backup per resource and target.
func (o *metricsObserver) observeBackups() {
	if o.backups == nil {
		return
	}
	all, err := o.backups.ListBackups(o.baseContext(), "", "")
	if err != nil {
		return
	}
	newest := map[string]*database.Backup{}
	for _, b := range all {
		if b.State != database.BackupStateCompleted {
			continue
		}
		key := b.Resource + "\x00" + b.Target
		if cur, ok := newest[key]; !ok || b.StartedAt.After(cur.StartedAt) {
			newest[key] = b
		}
	}
	out := make([]metrics.BackupState, 0, len(newest))
	for _, b := range newest {
		kind, shipped := database.BackupKindFull, b.TotalBytes
		if b.Kind == database.BackupKindIncremental {
			kind, shipped = b.Kind, 0
			for _, v := range b.Volumes {
				shipped += v.ChangedBytes
			}
		}
		out = append(out, metrics.BackupState{
			Resource: b.Resource, Target: b.Target, Kind: kind,
			StartedUnix: float64(b.StartedAt.Unix()), ShippedBytes: shipped,
		})
	}
	o.metrics.SetBackups(out)
}

// observeFaultDomains exports which resources sit in a single fault domain.
func (o *metricsObserver) observeFaultDomains() {
	if o.resources == nil {
		return
	}
	list, err := o.resources.ListResources(o.baseContext())
	if err != nil {
		return
	}
	risks := make(map[string]string, len(list))
	for _, r := range list {
		if r.FaultDomainRisk != "" {
			risks[r.Name] = r.FaultDomainRisk
		}
	}
	o.metrics.SetFaultDomainRisks(risks)
}

// peerTLS is nil for the node whose status was read, which has no connection
// to describe.
func peerTLS(state alert.NodeStateInfo) *bool {
	if state.Connection == "" {
		return nil
	}
	tls := state.TLS
	return &tls
}
