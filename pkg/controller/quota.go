package controller

import (
	"context"
	"fmt"

	"github.com/haify-project/sds/pkg/database"
)

// Quotas ([quota]).
//
// A thin pool admits volumes as long as it has room for the data already
// written, which is the point of thin provisioning and also how a pool ends up
// promising ten times what it holds and filling on a Tuesday. max_overcommit_ratio
// caps what may be promised: a replica, a new volume or a resize that would
// take a node's thin pool past that many times its real size is refused there,
// and auto-placement passes such a node over. Snapshots do not count; they
// share their origin's blocks until it is rewritten.
//
// Projects are resources sharing a label (project_label, "project" by
// default). A project's quota caps the total size of its resources — counted
// once, as the size a user asked for, not once per replica — and their
// number.

func (rm *ResourceManager) maxOvercommit() float64 {
	if rm.controller == nil || rm.controller.config == nil {
		return 0
	}
	return rm.controller.config.Quota.MaxOvercommitRatio
}

// overcommitted reports whether adding sizeGB to a thin pool takes it past
// its ratio.
func (c poolCapacity) overcommitted(sizeGB uint64) bool {
	if c.maxOvercommit <= 0 || c.sizeBytes == 0 {
		return false
	}
	return float64(c.virtualBytes+sizeGB<<30) > c.maxOvercommit*float64(c.sizeBytes)
}

// assertOvercommit refuses adding addGB of thin volume to pool on nodes when
// a node's pool would pass the ratio.
func (rm *ResourceManager) assertOvercommit(ctx context.Context, pool string, nodes []string, addGB uint64) error {
	ratio := rm.maxOvercommit()
	if ratio <= 0 || addGB == 0 || rm.controller.storage == nil {
		return nil
	}
	pools, err := rm.controller.storage.ListPools(ctx)
	if err != nil {
		return nil // capacity unknown is not capacity exhausted; placement says the same
	}
	pool = normalizeManagedName(pool)
	for _, n := range nodes {
		addr := rm.controller.ResolveHost(n)
		for _, p := range pools {
			if normalizeManagedName(p.Name) != pool || (p.Node != n && p.Node != addr && rm.controller.ResolveHost(p.Node) != addr) {
				continue
			}
			c := poolPlacementCapacity(p, false)
			c.maxOvercommit = ratio
			if c.thin && c.overcommitted(addGB) {
				return fmt.Errorf("%d GB more would promise %.1f times the size of thin pool %s on %s, past "+
					"[quota] max_overcommit_ratio %.1f; grow the pool or place it elsewhere",
					addGB, float64(c.virtualBytes+addGB<<30)/float64(c.sizeBytes), p.Name, n, ratio)
			}
		}
	}
	return nil
}

// assertProjectQuota refuses growing the project labels name by addGB (and,
// for a new resource, by one resource) past its quota.
func (rm *ResourceManager) assertProjectQuota(ctx context.Context, labels map[string]string, addGB uint64, newResource bool) error {
	if rm.controller.config == nil || rm.controller.db == nil {
		return nil
	}
	q := rm.controller.config.Quota
	project := labels[q.ProjectLabel]
	quota := q.Project(project)
	if project == "" || quota == nil {
		return nil
	}
	resources, err := rm.controller.db.ListResources(ctx)
	if err != nil {
		return err
	}
	var used uint64
	count := 0
	for _, r := range resources {
		if r.Labels[q.ProjectLabel] != project {
			continue
		}
		count++
		used += rm.resourceSizeGB(ctx, r)
	}
	if newResource && quota.MaxResources > 0 && count+1 > quota.MaxResources {
		return fmt.Errorf("project %s has %d of its %d resources; its quota allows no more", project, count, quota.MaxResources)
	}
	if quota.MaxGB > 0 && used+addGB > quota.MaxGB {
		return fmt.Errorf("project %s uses %d GB of its %d GB quota; %d GB more does not fit", project, used, quota.MaxGB, addGB)
	}
	return nil
}

func (rm *ResourceManager) resourceSizeGB(ctx context.Context, r *database.Resource) uint64 {
	vols, err := rm.controller.db.ListVolumes(ctx, r.Name)
	if err != nil {
		return 0
	}
	var gb uint64
	for _, v := range vols {
		gb += uint64(max(v.SizeGB, 0))
	}
	return gb
}

// assertQuotaForResource checks both for growing resource by addGB on nodes.
func (rm *ResourceManager) assertQuotaForResource(ctx context.Context, resource, pool string, nodes []string, addGB uint64) error {
	if rm.controller.db != nil {
		if r, err := rm.controller.db.GetResource(ctx, resource); err == nil && r != nil {
			if err := rm.assertProjectQuota(ctx, r.Labels, addGB, false); err != nil {
				return err
			}
		}
	}
	return rm.assertOvercommit(ctx, pool, nodes, addGB)
}

// assertCreateQuota checks a new resource against its project's quota and
// every target node's thin pools.
func (rm *ResourceManager) assertCreateQuota(ctx context.Context, volumes []resolvedVolume, nodes []string, labels map[string]string) error {
	var total uint64
	perPool := map[string]uint64{}
	for _, v := range volumes {
		total += uint64(v.sizeGB)
		perPool[v.pool] += uint64(v.sizeGB)
	}
	if err := rm.assertProjectQuota(ctx, labels, total, true); err != nil {
		return err
	}
	for pool, gb := range perPool {
		if err := rm.assertOvercommit(ctx, pool, nodes, gb); err != nil {
			return err
		}
	}
	return nil
}

// assertResizeQuota checks the growth of a resize.
func (rm *ResourceManager) assertResizeQuota(ctx context.Context, resource string, volumeID uint32, newSizeGB uint64, hosts []string) error {
	if rm.controller.db == nil {
		return nil
	}
	vols, err := rm.controller.db.ListVolumes(ctx, resource)
	if err != nil {
		return nil
	}
	v := findVolumeRecord(vols, volumeID)
	if v == nil || newSizeGB <= uint64(max(v.SizeGB, 0)) {
		return nil
	}
	return rm.assertQuotaForResource(ctx, resource, v.Pool, hosts, newSizeGB-uint64(v.SizeGB))
}
