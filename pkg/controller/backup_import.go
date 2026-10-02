package controller

import (
	"context"
	"encoding/json"
	"fmt"
	"path"
	"sort"
	"strings"
	"time"

	"go.uber.org/zap"

	"github.com/haify-project/sds/pkg/database"
)

// Importing backups from a target. The controller's database is replicated with
// the cluster, so the disaster backups exist for takes the backup records with
// it. Every backup also leaves a manifest.json beside its images, written only
// once the images are verified; this rebuilds the records, chains included,
// from those manifests alone. It is also how one cluster restores another's
// backups: add the same bucket as a target, import, restore.

// ImportResult says what an import found on the target.
type ImportResult struct {
	// Imported are the backup ids now known to this controller.
	Imported []string
	// Skipped maps a manifest's backup id (or its path when unreadable) to why
	// it was left out.
	Skipped map[string]string
}

// ImportBackups records every complete backup on target that this controller
// does not already know. node names the node that reads the target; empty
// tries the registered nodes in turn until one has rclone.
func (bm *BackupManager) ImportBackups(ctx context.Context, targetName, node string) (*ImportResult, error) {
	db := bm.controller.db
	if db == nil {
		return nil, fmt.Errorf("database not available")
	}
	dbTarget, err := db.GetBackupTarget(ctx, targetName)
	if err != nil {
		return nil, err
	}
	host, err := bm.importHost(ctx, node)
	if err != nil {
		return nil, err
	}
	dep := newBackupDeploymentClient(bm.controller.deployment)
	sess, err := bm.backend.Prepare(ctx, dep, host, targetSpecFromDB(dbTarget))
	if err != nil {
		return nil, err
	}
	defer func() {
		if err := sess.Close(context.WithoutCancel(ctx)); err != nil {
			bm.controller.logger.Warn("Failed to remove staged backup credentials", zap.String("host", host), zap.Error(err))
		}
	}()

	objects, err := sess.List(ctx)
	if err != nil {
		return nil, err
	}
	sizes := make(map[string]uint64, len(objects))
	var manifests []string
	for _, o := range objects {
		sizes[o.Path] = o.Bytes
		if path.Base(o.Path) == manifestObject {
			manifests = append(manifests, o.Path)
		}
	}
	sort.Strings(manifests)

	res := &ImportResult{Skipped: map[string]string{}}
	var found []*database.Backup
	for _, mp := range manifests {
		text, err := sess.GetText(ctx, mp)
		if err != nil {
			res.Skipped[mp] = err.Error()
			continue
		}
		rec, err := backupFromManifest(text, path.Dir(mp), targetName, sizes)
		if err != nil {
			res.Skipped[mp] = err.Error()
			continue
		}
		if _, err := db.GetBackup(ctx, rec.ID); err == nil {
			res.Skipped[rec.ID] = "already recorded"
			continue
		}
		found = append(found, rec)
	}

	bm.adoptBases(ctx, found)
	for _, rec := range found {
		if err := db.SaveBackup(ctx, rec); err != nil {
			return res, fmt.Errorf("record imported backup %s: %w", rec.ID, err)
		}
		res.Imported = append(res.Imported, rec.ID)
	}
	bm.controller.logger.Info("Imported backups",
		zap.String("target", targetName), zap.Int("imported", len(res.Imported)), zap.Int("skipped", len(res.Skipped)))
	return res, nil
}

// importHost picks the node that reads the target.
func (bm *BackupManager) importHost(ctx context.Context, node string) (string, error) {
	dep := newBackupDeploymentClient(bm.controller.deployment)
	if node != "" {
		host := bm.controller.ResolveHost(node)
		return host, bm.backend.Preflight(ctx, dep, host)
	}
	nodes, err := bm.controller.db.ListNodes(ctx)
	if err != nil {
		return "", err
	}
	var last error
	for _, n := range nodes {
		host := bm.controller.ResolveHost(n.Name)
		if last = bm.backend.Preflight(ctx, dep, host); last == nil {
			return host, nil
		}
	}
	if last == nil {
		return "", fmt.Errorf("no node is registered to read the target from")
	}
	return "", fmt.Errorf("no registered node can read the target: %w", last)
}

// backupFromManifest turns one manifest into a backup record, after checking
// that every object it names is on the target at a plausible size. dir is the
// manifest's directory relative to the target root.
func backupFromManifest(text, dir, target string, sizes map[string]uint64) (*database.Backup, error) {
	var m BackupManifest
	if err := json.Unmarshal([]byte(strings.TrimSpace(text)), &m); err != nil {
		return nil, fmt.Errorf("not a backup manifest: %w", err)
	}
	if m.ID == "" || m.Resource == "" || len(m.Volumes) == 0 {
		return nil, fmt.Errorf("manifest names no backup id, resource or volume")
	}
	if m.Version > manifestVersion {
		return nil, fmt.Errorf("manifest version %d is newer than this controller understands (%d)", m.Version, manifestVersion)
	}
	kind := m.Kind
	if kind == "" {
		kind = database.BackupKindFull
	}
	if kind == database.BackupKindIncremental && m.Parent == "" {
		return nil, fmt.Errorf("incremental backup %s names no parent", m.ID)
	}
	started, err := time.Parse(time.RFC3339, m.CreatedAt)
	if err != nil {
		return nil, fmt.Errorf("backup %s: bad created_at %q", m.ID, m.CreatedAt)
	}
	finished := started
	if t, err := time.Parse(time.RFC3339, m.FinishedAt); err == nil {
		finished = t
	}
	rec := &database.Backup{
		ID: m.ID, Resource: m.Resource, Target: target, Node: m.Node, Backend: m.Backend,
		State: database.BackupStateCompleted, Prefix: dir, Kind: kind, Parent: m.Parent, Schedule: m.Schedule,
		TotalBytes: m.TotalBytes, StartedAt: started, FinishedAt: finished,
	}
	snaps := map[uint32]string{}
	for _, s := range m.Snapshots {
		snaps[s.VolumeID] = s.Name
	}
	for _, v := range m.Volumes {
		if _, ok := sizes[v.Object]; !ok {
			return nil, fmt.Errorf("backup %s: image %s is missing from the target", m.ID, v.Object)
		}
		if v.Ranges != "" {
			if _, ok := sizes[v.Ranges]; !ok {
				return nil, fmt.Errorf("backup %s: range list %s is missing from the target", m.ID, v.Ranges)
			}
		}
		rec.Volumes = append(rec.Volumes, database.BackupVolume{
			VolumeID: v.VolumeID, BackingVolume: v.Backing, Pool: v.Pool, Object: v.Object,
			Bytes: v.Bytes, Ranges: v.Ranges, ChangedBytes: v.ChangedBytes, Snapshot: snaps[v.VolumeID],
		})
	}
	return rec, nil
}

// adoptBases decides which imported backups keep their base snapshot. Only the
// newest backup of each resource can be the next incremental's base, and only
// when its snapshot is still on a node of this cluster — which it is after a
// lost database, and is not when the backups came from another cluster. Every
// other snapshot name is dropped, so nothing here ever releases (removes) a
// snapshot this cluster did not make.
func (bm *BackupManager) adoptBases(ctx context.Context, found []*database.Backup) {
	newest := map[string]*database.Backup{}
	for _, b := range found {
		if cur := newest[b.Resource]; cur == nil || b.StartedAt.After(cur.StartedAt) {
			newest[b.Resource] = b
		}
	}
	for _, b := range found {
		keep := newest[b.Resource] == b && bm.basesPresent(ctx, b)
		for i := range b.Volumes {
			if !keep {
				b.Volumes[i].Snapshot = ""
			}
		}
	}
}

// basesPresent reports whether every base snapshot b names is a thin volume on
// b's node right now.
func (bm *BackupManager) basesPresent(ctx context.Context, b *database.Backup) bool {
	if !bm.registered(ctx, b.Node) {
		return false
	}
	host := bm.controller.ResolveHost(b.Node)
	for _, v := range b.Volumes {
		if v.Snapshot == "" {
			return false
		}
		if thin, err := bm.controller.deployment.LVIsThin(ctx, host, v.Pool, v.Snapshot); err != nil || !thin {
			return false
		}
	}
	return true
}

// registered reports whether node is a node of this cluster, by name or address.
func (bm *BackupManager) registered(ctx context.Context, node string) bool {
	nodes, err := bm.controller.db.ListNodes(ctx)
	if err != nil {
		return false
	}
	for _, n := range nodes {
		if n.Name == node || n.Address == node {
			return true
		}
	}
	return false
}
