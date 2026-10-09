package csi

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/container-storage-interface/spec/lib/go/csi"
	haifypb "github.com/haify-project/haify/api/proto/v1"
	"go.uber.org/zap"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// snapshotIDSep separates the three parts of a CSI snapshot ID. Neither a
// sanitized resource name (letters/digits/underscore) nor a node name (a
// hostname) can contain it, so the encoding round-trips unambiguously.
const snapshotIDSep = "/"

// makeSnapshotID encodes everything DeleteSnapshot needs to find the snapshot
// again: the source resource, the node whose replica actually holds it, and its
// backend name. A snapshot lives on exactly ONE node (it is an LVM/ZFS snapshot
// of that node's local backing volume, not a replicated DRBD object), so the
// node must be part of the ID — re-resolving it later could pick a different
// replica that has no such snapshot.
func makeSnapshotID(resource, node, snapshotName string) string {
	return strings.Join([]string{resource, node, snapshotName}, snapshotIDSep)
}

// parseSnapshotID splits an ID produced by makeSnapshotID.
func parseSnapshotID(id string) (resource, node, snapshotName string, err error) {
	parts := strings.Split(id, snapshotIDSep)
	if len(parts) != 3 || parts[0] == "" || parts[1] == "" || parts[2] == "" {
		return "", "", "", fmt.Errorf("malformed snapshot id %q (want <resource>/<node>/<snapshot>)", id)
	}
	return parts[0], parts[1], parts[2], nil
}

// snapshotSource locates the backing volume to snapshot for a resource: the
// `<pool>/<lv>` path of its first volume, and a node that holds a diskful
// replica (GetNodes reports diskful nodes only, so a diskless client is never
// chosen — it has no local storage to snapshot).
func snapshotSource(res *haifypb.ResourceInfo) (volumePath, node string, err error) {
	vols := res.GetVolumes()
	if len(vols) == 0 {
		return "", "", fmt.Errorf("resource %q has no volumes", res.GetName())
	}
	pool, backing := vols[0].GetPool(), vols[0].GetBackingVolume()
	if pool == "" || backing == "" {
		return "", "", fmt.Errorf("resource %q volume 0 has no backing volume recorded", res.GetName())
	}
	nodes := res.GetNodes()
	if len(nodes) == 0 {
		return "", "", fmt.Errorf("resource %q has no diskful replica to snapshot", res.GetName())
	}
	return pool + "/" + backing, nodes[0], nil
}

// snapshotSizeBytes reports the source volume's size, which is what the CO
// records as the snapshot's restore size.
func snapshotSizeBytes(res *haifypb.ResourceInfo) int64 {
	if vols := res.GetVolumes(); len(vols) > 0 {
		return int64(vols[0].GetSizeGb()) * giB
	}
	return 0
}

// CreateSnapshot takes a point-in-time snapshot of a volume's backing LV/dataset
// on one replica node. It is idempotent per the CSI spec: calling it again with
// the same name and source returns the existing snapshot instead of failing.
//
// The snapshot is local to one node — it is not replicated by DRBD — so it
// protects against logical faults (bad writes, accidental deletion) rather than
// loss of that node. Shipping snapshots off-cluster is a separate concern.
func (s *controllerServer) CreateSnapshot(ctx context.Context, req *csi.CreateSnapshotRequest) (*csi.CreateSnapshotResponse, error) {
	if req.GetName() == "" {
		return nil, status.Error(codes.InvalidArgument, "snapshot name is required")
	}
	if req.GetSourceVolumeId() == "" {
		return nil, status.Error(codes.InvalidArgument, "source volume id is required")
	}

	source := req.GetSourceVolumeId()
	res, err := s.backend.GetResource(ctx, source)
	if err != nil || res == nil {
		return nil, status.Errorf(codes.NotFound, "source volume %q not found", source)
	}

	volumePath, node, err := snapshotSource(res)
	if err != nil {
		return nil, status.Error(codes.FailedPrecondition, err.Error())
	}

	// The CO's name (e.g. "snapshot-<uuid>") must survive as an LVM/ZFS object
	// name — including staying clear of LVM's reserved "snapshot*" namespace.
	snapName := sanitizeSnapshotName(req.GetName())

	snapshot := &csi.Snapshot{
		SnapshotId:     makeSnapshotID(source, node, snapName),
		SourceVolumeId: source,
		SizeBytes:      snapshotSizeBytes(res),
		// LVM/ZFS snapshots are usable the moment lvcreate/zfs snapshot
		// returns; there is no background copy to wait for.
		ReadyToUse: true,
	}

	// Idempotency: an existing snapshot of the same name on the same source is
	// the same snapshot, so report success rather than trying to create it twice
	// (which lvcreate would reject).
	if existing, lerr := s.backend.ListSnapshots(ctx, volumePath, node); lerr == nil {
		for _, snap := range existing {
			if snap.GetName() == snapName {
				snapshot.CreationTime = snapshotCreationTime(snap.GetCreatedAt())
				return &csi.CreateSnapshotResponse{Snapshot: snapshot}, nil
			}
		}
	}

	// A name the CO already used for a snapshot of another volume is a
	// conflict, not a second snapshot. Kubernetes names snapshots after their
	// UID so it never asks, but the spec requires the answer, and giving it
	// costs one walk of the driver's volumes on the uncommon path — the
	// idempotent retry above has already returned.
	if other := s.snapshotNamedElsewhere(ctx, snapName, source); other != "" {
		return nil, status.Errorf(codes.AlreadyExists,
			"snapshot name %q is already used by a snapshot of volume %q", req.GetName(), other)
	}

	if err := s.backend.CreateSnapshot(ctx, volumePath, snapName, node); err != nil {
		return nil, status.Errorf(codes.Internal, "create snapshot %q of %q: %v", snapName, volumePath, err)
	}
	snapshot.CreationTime = timestamppb.New(time.Now())

	s.log.Info("CSI snapshot created",
		zap.String("snapshot", snapName),
		zap.String("volume", volumePath),
		zap.String("node", node))

	return &csi.CreateSnapshotResponse{Snapshot: snapshot}, nil
}

// DeleteSnapshot removes a snapshot. It is idempotent: an unknown, malformed or
// already-deleted snapshot is reported as success, as the CSI spec requires.
func (s *controllerServer) DeleteSnapshot(ctx context.Context, req *csi.DeleteSnapshotRequest) (*csi.DeleteSnapshotResponse, error) {
	if req.GetSnapshotId() == "" {
		return nil, status.Error(codes.InvalidArgument, "snapshot id is required")
	}

	resource, node, snapName, err := parseSnapshotID(req.GetSnapshotId())
	if err != nil {
		// Not an ID this driver ever handed out, so there is nothing to delete.
		return &csi.DeleteSnapshotResponse{}, nil
	}

	res, gerr := s.backend.GetResource(ctx, resource)
	if gerr != nil || res == nil {
		// The source volume is gone; its backing LV — and with it any snapshot
		// of that LV — went away when the resource was deleted.
		return &csi.DeleteSnapshotResponse{}, nil
	}

	volumePath, _, serr := snapshotSource(res)
	if serr != nil {
		return &csi.DeleteSnapshotResponse{}, nil
	}

	if err := s.backend.DeleteSnapshot(ctx, volumePath, snapName, node); err != nil {
		return nil, status.Errorf(codes.Internal, "delete snapshot %q: %v", snapName, err)
	}
	return &csi.DeleteSnapshotResponse{}, nil
}

// snapshotCreationTime converts a backend timestamp into a protobuf timestamp.
// The backend does not always record one, so an empty or unparseable value
// yields nil, which the CSI spec permits.
func snapshotCreationTime(raw string) *timestamppb.Timestamp {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	for _, layout := range []string{time.RFC3339, "2006-01-02 15:04:05 -0700", "2006-01-02 15:04:05"} {
		if t, err := time.Parse(layout, raw); err == nil {
			return timestamppb.New(t)
		}
	}
	return nil
}

// snapshotNamedElsewhere returns the volume that already has a CSI snapshot
// called snapName, other than source, or "" when none does.
func (s *controllerServer) snapshotNamedElsewhere(ctx context.Context, snapName, source string) string {
	all, err := s.backend.ListResources(ctx)
	if err != nil {
		return ""
	}
	for _, r := range all {
		if !isCSIVolume(r) || r.GetName() == source {
			continue
		}
		for _, snap := range s.snapshotsOf(ctx, r) {
			if strings.HasSuffix(snap.GetSnapshotId(), snapshotIDSep+snapName) {
				return r.GetName()
			}
		}
	}
	return ""
}
