package controller

import (
	"context"
	"fmt"

	"go.uber.org/zap"
)

// Choosing the replica a backup reads.

// pickBackupNode chooses which replica to read.
//
// An explicitly named node is honoured as given — an operator asking for the DR
// node knows what they are asking for. Otherwise a Secondary is preferred so
// the snapshot's copy-on-write cost lands away from the node serving the
// workload, and the DR node is skipped: under protocol A it may be behind, and
// a backup whose point in time is "somewhere near then" is not one.
func (bm *BackupManager) pickBackupNode(info *ResourceInfo, requested string) (string, error) {
	if requested != "" {
		for _, n := range info.Nodes {
			if n == requested {
				return requested, nil
			}
		}
		return "", fmt.Errorf("node %q holds no replica of %q", requested, info.Name)
	}
	if c := backupCandidates(info); len(c) > 0 {
		return c[0], nil
	}
	return "", fmt.Errorf(
		"no node holds an UpToDate replica of %q; back up from a healthy node or name one explicitly", info.Name)
}

// backupCandidates lists the replicas a backup may read, in order of
// preference: UpToDate Secondaries, then the Primary, never the DR replica.
func backupCandidates(info *ResourceInfo) []string {
	var secondaries, primaries []string
	for _, n := range info.Nodes {
		if info.WANMode && n == info.DRNode {
			continue
		}
		st := info.NodeStates[n]
		if st == nil || st.DiskState != "UpToDate" {
			continue
		}
		if st.Role == "Primary" {
			primaries = append(primaries, n)
		} else {
			secondaries = append(secondaries, n)
		}
	}
	return append(secondaries, primaries...)
}

// nodeWithSnapshotRoom returns the first candidate whose volume groups can hold
// the backup's snapshots, or "" to leave the choice to pickBackupNode.
//
// A thin snapshot takes its space from the pool as blocks change, but a thick
// one reserves its copy-on-write area up front from the volume group, and a
// nearly full group refuses it outright. Picking such a node fails the backup
// when a replica next to it had room.
func (bm *BackupManager) nodeWithSnapshotRoom(ctx context.Context, info *ResourceInfo) string {
	candidates := backupCandidates(info)
	for _, n := range candidates {
		if bm.snapshotRoom(ctx, bm.controller.ResolveHost(n), info) {
			return n
		}
		bm.controller.logger.Info("Backup: skipping a replica without room for a thick snapshot",
			zap.String("resource", info.Name), zap.String("node", n))
	}
	return ""
}

// snapshotRoom reports whether host's volume groups can take one snapshot of
// every volume of info. A query that fails counts as room: the snapshot then
// fails with LVM's own message, which is more use than a guess.
func (bm *BackupManager) snapshotRoom(ctx context.Context, host string, info *ResourceInfo) bool {
	dep := bm.controller.deployment
	need := map[string]uint64{}
	for _, v := range info.Volumes {
		if thin, err := dep.LVIsThin(ctx, host, v.Pool, v.BackingVolume); err != nil || thin {
			continue
		}
		need[v.Pool] += cowBytes(v.SizeGB)
	}
	for vg, n := range need {
		free, err := dep.VGFreeBytes(ctx, host, vg)
		if err == nil && free < n {
			return false
		}
	}
	return true
}

// cowBytes is cowSize in bytes.
func cowBytes(originGB uint64) uint64 {
	mb := originGB * 1024 / 5
	if mb < 256 {
		mb = 256
	}
	return mb << 20
}
