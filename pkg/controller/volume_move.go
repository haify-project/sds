package controller

import (
	"context"
	"fmt"
	"strings"
	"time"

	"go.uber.org/zap"

	"github.com/haify-project/sds/pkg/database"
	"github.com/haify-project/sds/pkg/deployment"
	"github.com/haify-project/sds/pkg/event"
)

// Moving a volume to another pool while the resource stays in service
// (`sds resource move-volume`): from an HDD pool to an SSD pool, off a pool
// that is being retired.
//
// One node at a time: the node's copy is detached, a fresh volume is made in
// the new pool and attached, and DRBD copies the data back from a peer. The
// resource keeps serving throughout — a Primary whose own copy is detached
// reads and writes through its peers — and is never more than one copy short.
// A thin target stays thin: the resync sends zeros as discards. The move ends
// by writing the new pool into the resource's config and database record.
//
// The old volume's LVM snapshots cannot come along and are deleted with it,
// so a resource whose snapshots are locked is refused.

const (
	volumeMovePoll    = 10 * time.Second
	volumeMoveTimeout = 72 * time.Hour
)

// MoveVolume starts moving volume volID of resource to pool. A failed move
// is resumed by asking again with the same pool.
func (rm *ResourceManager) MoveVolume(ctx context.Context, resource string, volID int, pool string) (string, error) {
	c := rm.controller
	pool = normalizeManagedName(pool)
	r, err := c.db.GetResource(ctx, resource)
	if err != nil || r == nil {
		return "", fmt.Errorf("resource %q not found", resource)
	}
	vol, err := rm.dbVolume(ctx, resource, volID)
	if err != nil {
		return "", err
	}
	nodes := splitCSV(r.Nodes)
	switch {
	case vol.Pool == pool:
		return "", fmt.Errorf("volume %d of %s is already in %s", volID, resource, pool)
	case r.Encrypted:
		return "", fmt.Errorf("%s is encrypted; moving an encrypted volume is not supported", resource)
	case r.MoveFrom != "":
		return "", fmt.Errorf("%s is moving a replica (%s → %s); wait for it to finish", resource, r.MoveFrom, r.MoveTo)
	case len(nodes) < 2:
		return "", fmt.Errorf("%s has one copy; its data would have nowhere to come back from", resource)
	}
	if err := c.assertResourceUnlocked(ctx, resource, "moving the volume to another pool"); err != nil {
		return "", err
	}
	for _, n := range nodes {
		res, err := rm.deployment.Exec(ctx, []string{c.ResolveHost(n)}, "sudo vgs "+pool)
		if err != nil || !res.AllSuccess() {
			return "", fmt.Errorf("pool %s does not exist on %s", pool, n)
		}
	}
	jobs, _ := c.db.ListStorageJobs(ctx)
	var done []string
	for _, j := range jobs {
		if j.Kind != database.JobMoveVolume || j.Resource != resource {
			continue
		}
		if j.State == database.JobRunning {
			return "", fmt.Errorf("job %s is already moving a volume of %s", j.ID, resource)
		}
		if j.State == database.JobFailed && j.VolumeID == volID && j.TargetPool == pool && len(done) == 0 {
			done = j.Done // pick up where the last attempt stopped
		}
	}
	j := &database.StorageJob{ID: newJobID(), Kind: database.JobMoveVolume, State: database.JobRunning,
		Resource: resource, VolumeID: volID, FromPool: vol.Pool, TargetPool: pool,
		Nodes: rm.secondariesFirst(ctx, resource, nodes), Done: done, Progress: "starting", StartedAt: time.Now()}
	if err := c.db.SaveStorageJob(ctx, j); err != nil {
		return "", err
	}
	go rm.runVolumeMove(c.ctx, j)
	return j.ID, nil
}

func (rm *ResourceManager) dbVolume(ctx context.Context, resource string, volID int) (*database.Volume, error) {
	vols, err := rm.controller.db.ListVolumes(ctx, resource)
	if err != nil {
		return nil, err
	}
	for _, v := range vols {
		if v.VolumeID == volID {
			return v, nil
		}
	}
	return nil, fmt.Errorf("%s has no volume %d", resource, volID)
}

// secondariesFirst orders nodes so the Primary's copy moves last: until then
// it keeps serving from its own disk.
func (rm *ResourceManager) secondariesFirst(ctx context.Context, resource string, nodes []string) []string {
	var primary string
	if info, err := rm.GetResource(ctx, resource); err == nil {
		for n, st := range info.NodeStates {
			if strings.EqualFold(st.Role, "Primary") {
				primary = rm.controller.NodeName(n)
			}
		}
	}
	var out []string
	for _, n := range nodes {
		if n != primary {
			out = append(out, n)
		}
	}
	if contains(nodes, primary) {
		out = append(out, primary)
	}
	return out
}

func (rm *ResourceManager) runVolumeMove(ctx context.Context, j *database.StorageJob) {
	c := rm.controller
	for _, n := range j.Nodes {
		if contains(j.Done, n) {
			continue
		}
		j.Progress = fmt.Sprintf("moving %s (%d of %d)", n, len(j.Done)+1, len(j.Nodes))
		_ = c.db.SaveStorageJob(ctx, j)
		if err := rm.moveVolumeOnNode(ctx, j, n); err != nil {
			rm.finishVolumeMove(ctx, j, fmt.Errorf("on %s: %w", n, err))
			return
		}
		j.Done = append(j.Done, n)
		_ = c.db.SaveStorageJob(ctx, j)
	}
	rm.finishVolumeMove(ctx, j, rm.commitVolumeMove(ctx, j))
}

// moveVolumeOnNode replaces node's copy of the volume with one in the target
// pool and waits for DRBD to fill it.
func (rm *ResourceManager) moveVolumeOnNode(ctx context.Context, j *database.StorageJob, node string) error {
	c := rm.controller
	host := c.ResolveHost(node)
	vol, err := rm.dbVolume(ctx, j.Resource, j.VolumeID)
	if err != nil {
		return err
	}
	target := fmt.Sprintf("%s/%d", j.Resource, j.VolumeID)
	if err := rm.waitPeerUpToDate(ctx, j.Resource, j.VolumeID, node); err != nil {
		return err
	}
	size, err := rm.deployment.LVSizeBytes(ctx, host, j.FromPool, vol.VolumeName)
	if err != nil {
		return fmt.Errorf("read the size of %s/%s: %w", j.FromPool, vol.VolumeName, err)
	}
	// A little larger than the old volume: fresh metadata may be sized for
	// more peers than the old copy's, and a copy smaller than its peers is
	// refused when it connects.
	if err := rm.createBackingVolumeOn(ctx, host, j.TargetPool, vol.VolumeName, size+size/256+64<<20); err != nil {
		return fmt.Errorf("create %s/%s: %w", j.TargetPool, vol.VolumeName, err)
	}
	if err := execFailure(rm.deployment.DRBDDetach(ctx, host, target)); err != nil {
		return fmt.Errorf("detach: %w", err)
	}
	if err := rm.writeHostVolumeDisk(ctx, j, node, "/dev/"+j.TargetPool+"/"+vol.VolumeName); err != nil {
		return err
	}
	if err := execFailure(rm.deployment.Exec(ctx, []string{host},
		fmt.Sprintf("sudo drbdadm create-md --max-peers=%d --force %s", deployment.DefaultMaxPeers, target))); err != nil {
		return fmt.Errorf("create metadata: %w", err)
	}
	if err := execFailure(rm.deployment.DRBDAttach(ctx, host, target)); err != nil {
		return fmt.Errorf("attach: %w", err)
	}
	if err := rm.waitLocalUpToDate(ctx, host, target); err != nil {
		return err
	}
	// The old copy and its snapshots go.
	old := fmt.Sprintf("for s in $(sudo lvs --noheadings -o lv_name -S origin=%s %s 2>/dev/null); do sudo lvremove -f %s/$s; done; sudo lvremove -f %s/%s",
		vol.VolumeName, j.FromPool, j.FromPool, j.FromPool, vol.VolumeName)
	if err := execFailure(rm.deployment.Exec(ctx, []string{host}, old)); err != nil {
		c.logger.Warn("Volume moved, but the old copy could not be removed", zap.String("node", node), zap.Error(err))
	}
	return nil
}

// writeHostVolumeDisk points node's copy at disk in the config every member
// holds.
func (rm *ResourceManager) writeHostVolumeDisk(ctx context.Context, j *database.StorageJob, node, disk string) error {
	return rm.editResourceConfig(ctx, j.Resource, func(conf string) (string, error) {
		minor := -1
		for _, v := range parseResourceConfigVolumes(conf) {
			if v.VolumeID == j.VolumeID {
				minor = v.Minor
			}
		}
		if minor < 0 {
			return "", fmt.Errorf("volume %d is not in the config", j.VolumeID)
		}
		return setHostVolumeDisk(conf, rm.controller.nodes.GetDRBDNameByRef(node), j.VolumeID, minor, disk)
	})
}

// editResourceConfig rewrites a resource's .res on every member.
func (rm *ResourceManager) editResourceConfig(ctx context.Context, resource string, edit func(string) (string, error)) error {
	r, err := rm.controller.db.GetResource(ctx, resource)
	if err != nil || r == nil {
		return fmt.Errorf("resource %q not found", resource)
	}
	var hosts []string
	for _, n := range splitCSV(r.Nodes) {
		hosts = append(hosts, rm.controller.ResolveHost(n))
	}
	path := "/etc/drbd.d/" + resource + ".res"
	conf, err := rm.readResourceConfig(ctx, hosts, path, resource)
	if err != nil {
		return err
	}
	conf, err = edit(conf)
	if err != nil {
		return err
	}
	targets := append(hosts, rm.disklessParticipantHosts(ctx, resource)...)
	if _, err := rm.deployment.DistributeConfig(ctx, targets, conf, path); err != nil {
		return fmt.Errorf("distribute the config: %w", err)
	}
	return nil
}

// waitPeerUpToDate waits until a member other than node has the volume
// UpToDate, the copy node's will be rebuilt from.
func (rm *ResourceManager) waitPeerUpToDate(ctx context.Context, resource string, vol int, node string) error {
	deadline := time.Now().Add(10 * time.Minute)
	for {
		if info, err := rm.GetResource(ctx, resource); err == nil {
			for n, st := range info.NodeStates {
				if rm.controller.NodeName(n) != node && st.DiskState == "UpToDate" {
					return nil
				}
			}
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("no other copy of %s is UpToDate to rebuild from", resource)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(volumeMovePoll):
		}
	}
}

// waitLocalUpToDate waits for host's copy of target (res/vol) to finish its
// resync.
func (rm *ResourceManager) waitLocalUpToDate(ctx context.Context, host, target string) error {
	deadline := time.Now().Add(volumeMoveTimeout)
	for {
		res, err := rm.deployment.Exec(ctx, []string{host}, "sudo drbdadm dstate "+target)
		if err == nil && res.Hosts[host] != nil {
			if strings.HasPrefix(strings.TrimSpace(res.Hosts[host].Output), "UpToDate/") {
				return nil
			}
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("the new copy did not finish syncing within %s", volumeMoveTimeout)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(volumeMovePoll):
		}
	}
}

// commitVolumeMove makes the new pool the volume's pool for every node.
func (rm *ResourceManager) commitVolumeMove(ctx context.Context, j *database.StorageJob) error {
	vol, err := rm.dbVolume(ctx, j.Resource, j.VolumeID)
	if err != nil {
		return err
	}
	disk := "/dev/" + j.TargetPool + "/" + vol.VolumeName
	err = rm.editResourceConfig(ctx, j.Resource, func(conf string) (string, error) {
		out, err := setResourceVolumeDisk(conf, j.VolumeID, disk)
		if err != nil {
			return "", err
		}
		for _, n := range j.Nodes {
			out = removeHostVolumeDisk(out, rm.controller.nodes.GetDRBDNameByRef(n), j.VolumeID)
		}
		return out, nil
	})
	if err != nil {
		return err
	}
	vol.Pool, vol.Device = j.TargetPool, disk
	return rm.controller.db.SaveVolume(ctx, vol)
}

func (rm *ResourceManager) finishVolumeMove(ctx context.Context, j *database.StorageJob, err error) {
	c := rm.controller
	sev := event.SeverityInfo
	if err == nil {
		j.State, j.Progress = database.JobDone, "done"
		j.Message = fmt.Sprintf("volume %d of %s moved from %s to %s on %s", j.VolumeID, j.Resource, j.FromPool, j.TargetPool, strings.Join(j.Nodes, ", "))
	} else {
		sev = event.SeverityWarning
		j.State = database.JobFailed
		j.Message = fmt.Sprintf("moving volume %d of %s to %s stopped %v; moved so far: %s. Ask again to carry on from there",
			j.VolumeID, j.Resource, j.TargetPool, err, strings.Join(j.Done, ", "))
	}
	_ = c.db.SaveStorageJob(context.WithoutCancel(ctx), j)
	c.logger.Info("Volume move finished", zap.String("job", j.ID), zap.String("state", j.State), zap.String("message", j.Message))
	if c.events != nil {
		c.events.Publish(event.Event{Type: event.TypeVolumeMoved, Severity: sev, Status: event.StatusInfo,
			Resource: j.Resource, Message: j.Message, Details: map[string]string{"job": j.ID, "pool": j.TargetPool}})
	}
}

// resumeVolumeMoves carries on with the moves a previous controller started.
func (rm *ResourceManager) resumeVolumeMoves(ctx context.Context) {
	jobs, err := rm.controller.db.ListStorageJobs(ctx)
	if err != nil {
		return
	}
	for _, j := range jobs {
		if j.Kind == database.JobMoveVolume && j.State == database.JobRunning {
			go rm.runVolumeMove(rm.controller.ctx, j)
		}
	}
}
