package controller

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"go.uber.org/zap"

	"github.com/haify-project/sds/pkg/alert"
	"github.com/haify-project/sds/pkg/deployment"
	"github.com/haify-project/sds/pkg/event"
)

// Growing a thin pool before it is full ([storage.thin] autoextend_*).
//
// A full thin pool is not a slow failure: writes to it error out, DRBD takes
// the disk away, and the resource carries on degraded or not at all. A pool
// created by Haify leaves reserve_percent of its volume group free, and this
// observer grows the pool into that room once data or metadata use reaches
// the threshold — the job LVM's own autoextend does, but done here so it is
// visible as an event, and does not depend on dmeventd running on the node.
// When the group has no room left it says so once, which is the cue to add
// a disk.

const (
	autoextendCooldown  = 3 * time.Minute
	thinMetadataCeiling = 16 << 30 // LVM's maximum thin pool metadata size
	minExtendBytes      = 64 << 20
)

type thinAutoextender struct {
	c *Controller

	mu       sync.Mutex
	busy     map[string]bool
	last     map[string]time.Time
	warnedVG map[string]bool
}

func newThinAutoextender(c *Controller) *thinAutoextender {
	return &thinAutoextender{c: c, busy: map[string]bool{}, last: map[string]time.Time{}, warnedVG: map[string]bool{}}
}

// Observed implements alert.Observer. It must not block the poll, so the
// growing itself runs in its own goroutine.
func (a *thinAutoextender) Observed(obs alert.Observation) {
	cfg := a.c.config.Storage.Thin
	if cfg.AutoextendThreshold <= 0 || !obs.Pools.Enabled || !obs.Pools.OK {
		return
	}
	for _, p := range obs.Pools.Items {
		if p.ThinPool == "" || p.VDO {
			continue
		}
		data := p.DataPercent >= float64(cfg.AutoextendThreshold)
		meta := p.MetaPercent >= float64(cfg.AutoextendThreshold)
		key := p.Node + "/" + p.Name
		a.mu.Lock()
		if !data && !meta {
			delete(a.warnedVG, key)
			a.mu.Unlock()
			continue
		}
		if a.busy[key] || time.Since(a.last[key]) < autoextendCooldown {
			a.mu.Unlock()
			continue
		}
		a.busy[key] = true
		a.mu.Unlock()
		go func(p alert.PoolStatusInfo) {
			defer func() {
				a.mu.Lock()
				a.busy[key] = false
				a.last[key] = time.Now()
				a.mu.Unlock()
			}()
			a.extend(a.c.ctx, p, data, meta, key)
		}(p)
	}
}

// extendPlan is how much to grow a pool's data and metadata by, given its
// sizes and its group's free space. Metadata comes first: a pool whose
// metadata fills is lost to writes as surely as one whose data does.
func extendPlan(dataBytes, metaBytes, free uint64, growData, growMeta bool, percent int) (dataAdd, newMeta uint64) {
	if growMeta && metaBytes > 0 {
		target := min(metaBytes*2, thinMetadataCeiling)
		if target > metaBytes && target-metaBytes <= free {
			newMeta = target
			free -= target - metaBytes
		}
	}
	if growData && dataBytes > 0 {
		// Whole 4 MiB extents: lvextend refuses a size that is not.
		dataAdd = min(dataBytes*uint64(percent)/100, free) / (4 << 20) * (4 << 20)
		if dataAdd < minExtendBytes {
			dataAdd = 0
		}
	}
	return dataAdd, newMeta
}

func (a *thinAutoextender) extend(ctx context.Context, p alert.PoolStatusInfo, growData, growMeta bool, key string) {
	c := a.c
	host := c.ResolveHost(p.Node)
	dep := c.deployment
	data, err1 := dep.LVSizeBytes(ctx, host, p.Name, p.ThinPool)
	meta, err2 := dep.LVSizeBytes(ctx, host, p.Name, p.ThinPool+"_tmeta")
	free, err3 := dep.VGFreeBytes(ctx, host, p.Name)
	if err1 != nil || err2 != nil || err3 != nil {
		c.logger.Warn("Thin autoextend: could not size the pool", zap.String("pool", key))
		return
	}
	dataAdd, newMeta := extendPlan(data, meta, free, growData, growMeta, c.config.Storage.Thin.AutoextendPercent)
	if dataAdd == 0 && newMeta == 0 {
		a.mu.Lock()
		warned := a.warnedVG[key]
		a.warnedVG[key] = true
		a.mu.Unlock()
		if !warned {
			a.publish(p, event.SeverityWarning, fmt.Sprintf("thin pool %s/%s on %s is at %.0f%% data / %.0f%% metadata and its volume group "+
				"has %s left, too little to grow it: add a disk with `sds pool add --pool %s --nodes %s --devices <disk>`",
				p.Name, p.ThinPool, p.Node, p.DataPercent, p.MetaPercent, formatBytes(free), p.Name, p.Node))
		}
		return
	}
	var did []string
	if newMeta > 0 {
		if _, err := dep.LVExtendThinPoolMetadata(ctx, []string{host}, p.Name, p.ThinPool, newMeta); err != nil {
			c.logger.Warn("Thin autoextend: metadata", zap.String("pool", key), zap.Error(err))
		} else {
			did = append(did, fmt.Sprintf("metadata %s → %s (was %.0f%% used)", formatBytes(meta), formatBytes(newMeta), p.MetaPercent))
		}
	}
	if dataAdd > 0 {
		cmd := fmt.Sprintf("sudo lvextend -y -L +%dB %s/%s", dataAdd, p.Name, p.ThinPool)
		res, err := dep.Exec(ctx, []string{host}, cmd, deployment.WithExecTimeout(2*time.Minute))
		if err == nil && res.AllSuccess() {
			did = append(did, fmt.Sprintf("data %s → %s (was %.0f%% used)", formatBytes(data), formatBytes(data+dataAdd), p.DataPercent))
		} else {
			out := ""
			if res != nil && res.Hosts[host] != nil {
				out = strings.TrimSpace(res.Hosts[host].Output)
			}
			c.logger.Warn("Thin autoextend: data", zap.String("pool", key), zap.Error(err), zap.String("output", out))
			a.mu.Lock()
			warned := a.warnedVG[key]
			a.warnedVG[key] = true
			a.mu.Unlock()
			if !warned {
				a.publish(p, event.SeverityWarning, fmt.Sprintf("thin pool %s/%s on %s is at %.0f%% and growing it failed: %s",
					p.Name, p.ThinPool, p.Node, p.DataPercent, out))
			}
		}
	}
	if len(did) == 0 {
		return
	}
	left, _ := dep.VGFreeBytes(ctx, host, p.Name)
	a.publish(p, event.SeverityInfo, fmt.Sprintf("grew thin pool %s/%s on %s before it filled: %s; %s of its volume group left",
		p.Name, p.ThinPool, p.Node, strings.Join(did, ", "), formatBytes(left)))
}

func (a *thinAutoextender) publish(p alert.PoolStatusInfo, sev event.Severity, msg string) {
	a.c.logger.Info(msg)
	if a.c.events != nil {
		a.c.events.Publish(event.Event{Type: event.TypePoolExtended, Severity: sev, Status: event.StatusInfo,
			Node: p.Node, Message: msg, Details: map[string]string{"pool": p.Name, "thin_pool": p.ThinPool}})
	}
}
