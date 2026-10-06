package controller

import (
	"context"
	"fmt"
	"time"

	"github.com/haify-project/sds/pkg/deployment"
	"github.com/haify-project/sds/pkg/event"
	"github.com/haify-project/sds/pkg/inspect"
)

// The disks under the pools and their health (inspect.DiskProbeScript), for
// `sds pool disks` and for the daily inspection.

const diskProbeTimeout = 2 * time.Minute

// DiskHealth lists the disks of pool on node; empty pool or node means all.
func (sm *StorageManager) DiskHealth(ctx context.Context, pool, node string) ([]inspect.Disk, error) {
	c := sm.controller
	nodes, err := c.nodes.ListNodes(ctx)
	if err != nil {
		return nil, err
	}
	var hosts []string
	nameOf := map[string]string{}
	for _, n := range nodes {
		if (node != "" && n.Name != node && n.Address != node) || (node == "" && n.State != NodeStateOnline) {
			continue
		}
		hosts = append(hosts, n.Address)
		nameOf[n.Address] = n.Name
	}
	if len(hosts) == 0 {
		if node != "" {
			return nil, fmt.Errorf("node %q is not registered", node)
		}
		return nil, nil
	}
	cmd := "echo " + base64Std(inspect.DiskProbeScript) + " | base64 -d | sudo /bin/bash"
	res, err := c.deployment.Exec(ctx, hosts, cmd, deployment.WithExecTimeout(diskProbeTimeout))
	if err != nil {
		return nil, err
	}
	want := normalizeManagedName(pool)
	var out []inspect.Disk
	for _, h := range hosts {
		hr := res.Hosts[h]
		if hr == nil {
			continue
		}
		for _, d := range inspect.ParseDiskProbe(nameOf[h], hr.Output) {
			if pool == "" || d.Pool == want || d.Pool == pool {
				out = append(out, d)
			}
		}
	}
	return out, nil
}

// gatherDisks adds disk health to an inspection, and raises disk.health for
// each disk in trouble so it reaches the notification channels by itself
// rather than inside the inspection summary.
func (im *InspectionManager) gatherDisks(ctx context.Context, in *inspect.Input) {
	c := im.controller
	disks, err := c.storage.DiskHealth(ctx, "", "")
	if err != nil {
		return
	}
	in.Disks, in.DisksProbed = disks, true
	if c.events == nil {
		return
	}
	for _, d := range disks {
		sev := event.SeverityWarning
		switch d.Status {
		case inspect.DiskFail:
			sev = event.SeverityCritical
		case inspect.DiskWarn:
		default:
			continue
		}
		c.events.Publish(event.Event{Type: event.TypeDiskHealth, Severity: sev, Status: event.StatusInfo, Node: d.Node,
			Message: fmt.Sprintf("disk %s under pool %s on %s: %s; replace it with `sds pool replace-disk --pool %s --node %s --disk %s --new-disk <device>`",
				d.Device, d.Pool, d.Node, d.Detail, d.Pool, d.Node, d.PV),
			Details: map[string]string{"device": d.Device, "pool": d.Pool, "model": d.Model, "serial": d.Serial}})
	}
}
