package csi

import (
	"context"
	"sort"
	"strconv"
	"strings"

	"github.com/container-storage-interface/spec/lib/go/csi"
	sdspb "github.com/liliang-cn/sds/api/proto/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/wrapperspb"
)

// managedByLabel marks a resource this driver created. CreateVolume sets it;
// ListVolumes and ListSnapshots use it to leave alone every resource the
// cluster runs for other reasons, such as its own control plane.
const (
	managedByLabel = "sds.csi/managed-by"
	managedByValue = "csi"
)

func isCSIVolume(r *sdspb.ResourceInfo) bool {
	return r.GetLabels()[managedByLabel] == managedByValue
}

// GetCapacity reports how much room a pool has left on one node, which
// external-provisioner publishes as a CSIStorageCapacity object and the
// scheduler reads before it places a Pod.
//
// Without this the scheduler is blind to pool space. A PVC whose node's pool is
// full was placed there anyway and then failed with ResourceExhausted, and under
// WaitForFirstConsumer the Pod sat Pending while the provisioner retried. With
// it, the scheduler does not pick the node in the first place.
func (s *controllerServer) GetCapacity(ctx context.Context, req *csi.GetCapacityRequest) (*csi.GetCapacityResponse, error) {
	for _, c := range req.GetVolumeCapabilities() {
		if unsupportedCapability(c) != "" {
			return &csi.GetCapacityResponse{AvailableCapacity: 0}, nil
		}
	}
	// Parameters are optional in GetCapacity. Without a pool the question is how
	// much this driver could provision anywhere, which is every pool it knows.
	if req.GetParameters()["pool"] == "" {
		return s.capacityOfAllPools(ctx)
	}
	params, err := ParseVolumeParams(req.GetParameters())
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}
	nodes, err := s.backend.ListNodes(ctx)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "list nodes: %v", err)
	}
	pools, err := s.backend.ListPools(ctx)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "list pools: %v", err)
	}

	want := managedPoolName(params.Pool)
	capacity := map[string]uint64{} // node name -> free bytes, for nodes that report it
	var unknown []string
	for _, n := range nodes {
		for _, p := range pools {
			if p.GetName() != want || (p.GetNode() != n.GetAddress() && p.GetNode() != n.GetName()) {
				continue
			}
			if free, known := poolCapacity(p); known {
				capacity[n.GetName()] = free
			} else {
				unknown = append(unknown, n.GetName())
			}
			break
		}
	}

	// One node, the case external-provisioner asks about: one CSIStorageCapacity
	// object per node, because the topology key is per node.
	if node := req.GetAccessibleTopology().GetSegments()[TopologyKeyNode]; node != "" {
		free, ok := capacity[node]
		if !ok {
			for _, u := range unknown {
				if u == node {
					// Publishing 0 would read as "full" and stop the scheduler
					// placing anything here, over a figure nobody measured.
					// Unavailable makes the provisioner retry instead.
					return nil, status.Errorf(codes.Unavailable,
						"pool %q on node %q reports no capacity figure", params.Pool, node)
				}
			}
			// The node does not host the pool: it can take none of this class.
			return &csi.GetCapacityResponse{AvailableCapacity: 0, MaximumVolumeSize: wrapperspb.Int64(0)}, nil
		}
		return &csi.GetCapacityResponse{
			AvailableCapacity: int64(free),
			MaximumVolumeSize: wrapperspb.Int64(int64(free)),
		}, nil
	}

	// No topology: the room in the pool as a whole, and the biggest volume
	// that fits on the nodes it would need. A volume takes `replicas` nodes,
	// so the largest one that fits is bounded by the replicas-th roomiest.
	var total uint64
	frees := make([]uint64, 0, len(capacity))
	for _, f := range capacity {
		total += f
		frees = append(frees, f)
	}
	sort.Slice(frees, func(i, j int) bool { return frees[i] > frees[j] })
	var maxVol uint64
	if params.Replicas > 0 && len(frees) >= params.Replicas {
		maxVol = frees[params.Replicas-1]
	}
	return &csi.GetCapacityResponse{
		AvailableCapacity: int64(total),
		MaximumVolumeSize: wrapperspb.Int64(int64(maxVol)),
	}, nil
}

// csiVolume renders a resource as the CSI Volume a list or get returns.
func csiVolume(r *sdspb.ResourceInfo) *csi.Volume {
	var capacity int64
	if vols := r.GetVolumes(); len(vols) > 0 {
		capacity = int64(vols[0].GetSizeGb()) * giB
	}
	return &csi.Volume{
		VolumeId:           r.GetName(),
		CapacityBytes:      capacity,
		AccessibleTopology: accessibleTopology(r.GetNodes()),
	}
}

// ListVolumes returns the volumes this driver created, in a stable order so the
// CO can page through them with the token it is handed back.
func (s *controllerServer) ListVolumes(ctx context.Context, req *csi.ListVolumesRequest) (*csi.ListVolumesResponse, error) {
	all, err := s.backend.ListResources(ctx)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "list resources: %v", err)
	}
	var vols []*sdspb.ResourceInfo
	for _, r := range all {
		if isCSIVolume(r) {
			vols = append(vols, r)
		}
	}
	sort.Slice(vols, func(i, j int) bool { return vols[i].GetName() < vols[j].GetName() })

	start, end, next, err := page(len(vols), req.GetStartingToken(), req.GetMaxEntries())
	if err != nil {
		return nil, err
	}
	resp := &csi.ListVolumesResponse{NextToken: next}
	for _, r := range vols[start:end] {
		resp.Entries = append(resp.Entries, &csi.ListVolumesResponse_Entry{Volume: csiVolume(r)})
	}
	return resp, nil
}

// ControllerGetVolume returns one volume.
func (s *controllerServer) ControllerGetVolume(ctx context.Context, req *csi.ControllerGetVolumeRequest) (*csi.ControllerGetVolumeResponse, error) {
	if req.GetVolumeId() == "" {
		return nil, status.Error(codes.InvalidArgument, "volume id is required")
	}
	r, err := s.backend.GetResource(ctx, req.GetVolumeId())
	if err != nil || r == nil {
		return nil, status.Errorf(codes.NotFound, "volume %q not found", req.GetVolumeId())
	}
	return &csi.ControllerGetVolumeResponse{
		Volume: csiVolume(r),
		// No PublishedNodeIds: the driver has no ControllerPublish step, so the
		// field has nothing true to say.
		Status: &csi.ControllerGetVolumeResponse_VolumeStatus{},
	}, nil
}

// ListSnapshots returns the snapshots this driver created.
//
// There is no snapshot index to read. A CSI snapshot is an LVM/ZFS snapshot of
// one node's backing volume, so listing them means asking each replica node of
// each volume. The two filters narrow that walk — to one volume, or to one
// snapshot, whose ID already names the node that holds it.
func (s *controllerServer) ListSnapshots(ctx context.Context, req *csi.ListSnapshotsRequest) (*csi.ListSnapshotsResponse, error) {
	var entries []*csi.Snapshot

	switch {
	case req.GetSnapshotId() != "":
		resource, node, name, err := parseSnapshotID(req.GetSnapshotId())
		if err != nil {
			return &csi.ListSnapshotsResponse{}, nil // an ID nobody could have issued matches nothing
		}
		r, err := s.backend.GetResource(ctx, resource)
		if err != nil || r == nil {
			return &csi.ListSnapshotsResponse{}, nil
		}
		for _, snap := range s.snapshotsOn(ctx, r, node) {
			if strings.HasSuffix(snap.GetSnapshotId(), snapshotIDSep+name) {
				entries = append(entries, snap)
			}
		}

	case req.GetSourceVolumeId() != "":
		r, err := s.backend.GetResource(ctx, req.GetSourceVolumeId())
		if err != nil || r == nil {
			return &csi.ListSnapshotsResponse{}, nil
		}
		entries = s.snapshotsOf(ctx, r)

	default:
		all, err := s.backend.ListResources(ctx)
		if err != nil {
			return nil, status.Errorf(codes.Internal, "list resources: %v", err)
		}
		for _, r := range all {
			if isCSIVolume(r) {
				entries = append(entries, s.snapshotsOf(ctx, r)...)
			}
		}
	}

	sort.Slice(entries, func(i, j int) bool { return entries[i].GetSnapshotId() < entries[j].GetSnapshotId() })
	start, end, next, err := page(len(entries), req.GetStartingToken(), req.GetMaxEntries())
	if err != nil {
		return nil, err
	}
	resp := &csi.ListSnapshotsResponse{NextToken: next}
	for _, snap := range entries[start:end] {
		resp.Entries = append(resp.Entries, &csi.ListSnapshotsResponse_Entry{Snapshot: snap})
	}
	return resp, nil
}

// snapshotsOf lists a volume's CSI snapshots across all of its replica nodes.
func (s *controllerServer) snapshotsOf(ctx context.Context, r *sdspb.ResourceInfo) []*csi.Snapshot {
	var out []*csi.Snapshot
	for _, node := range r.GetNodes() {
		out = append(out, s.snapshotsOn(ctx, r, node)...)
	}
	return out
}

// snapshotsOn lists the CSI snapshots of a volume held on one node. A node that
// cannot be asked is skipped rather than failing the whole listing: one
// unreachable replica should not hide every other snapshot in the cluster.
//
// Only names carrying snapshotNamePrefix are the driver's own. The same LV also
// carries the controller's scheduled snapshots, and reporting those would hand
// the CO objects it never created and must never delete.
func (s *controllerServer) snapshotsOn(ctx context.Context, r *sdspb.ResourceInfo, node string) []*csi.Snapshot {
	volumePath, _, err := snapshotSource(r)
	if err != nil {
		return nil
	}
	snaps, err := s.backend.ListSnapshots(ctx, volumePath, node)
	if err != nil {
		return nil
	}
	var out []*csi.Snapshot
	for _, snap := range snaps {
		if !strings.HasPrefix(snap.GetName(), snapshotNamePrefix) {
			continue
		}
		out = append(out, &csi.Snapshot{
			SnapshotId:     makeSnapshotID(r.GetName(), node, snap.GetName()),
			SourceVolumeId: r.GetName(),
			SizeBytes:      snapshotSizeBytes(r),
			CreationTime:   snapshotCreationTime(snap.GetCreatedAt()),
			ReadyToUse:     true,
		})
	}
	return out
}

// page turns a CSI starting token and max entries into slice bounds over n
// sorted items, and the token for the page after. The token is the index of the
// next item; one the driver did not hand out is Aborted, as the spec asks.
func page(n int, token string, maxEntries int32) (start, end int, next string, err error) {
	if token != "" {
		start, err = strconv.Atoi(token)
		if err != nil || start < 0 || start > n {
			return 0, 0, "", status.Errorf(codes.Aborted, "invalid starting token %q", token)
		}
	}
	if maxEntries < 0 {
		return 0, 0, "", status.Error(codes.InvalidArgument, "max_entries must not be negative")
	}
	end = n
	if maxEntries > 0 && start+int(maxEntries) < n {
		end = start + int(maxEntries)
		next = strconv.Itoa(end)
	}
	return start, end, next, nil
}

// capacityOfAllPools answers a GetCapacity that names no pool: the free space
// across every pool that reports a figure, and the largest single pool as the
// biggest volume that could fit on one node.
func (s *controllerServer) capacityOfAllPools(ctx context.Context) (*csi.GetCapacityResponse, error) {
	pools, err := s.backend.ListPools(ctx)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "list pools: %v", err)
	}
	var total, largest uint64
	for _, p := range pools {
		if free, known := poolCapacity(p); known {
			total += free
			if free > largest {
				largest = free
			}
		}
	}
	return &csi.GetCapacityResponse{
		AvailableCapacity: int64(total),
		MaximumVolumeSize: wrapperspb.Int64(int64(largest)),
	}, nil
}
