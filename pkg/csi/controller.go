package csi

import (
	"context"

	"github.com/container-storage-interface/spec/lib/go/csi"
	sdspb "github.com/liliang-cn/sds/api/proto/v1"
	"go.uber.org/zap"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

const giB = 1 << 30

type controllerServer struct {
	csi.UnimplementedControllerServer
	backend SDSBackend
	log     *zap.Logger
}

// NewControllerServer returns the CSI Controller service.
func NewControllerServer(b SDSBackend, log *zap.Logger) csi.ControllerServer {
	return &controllerServer{backend: b, log: log}
}

func (s *controllerServer) CreateVolume(ctx context.Context, req *csi.CreateVolumeRequest) (*csi.CreateVolumeResponse, error) {
	if req.GetName() == "" {
		return nil, status.Error(codes.InvalidArgument, "volume name is required")
	}
	if len(req.GetVolumeCapabilities()) == 0 {
		return nil, status.Error(codes.InvalidArgument, "volume capabilities are required")
	}
	// Raw block volumes (volumeMode: Block) are not implemented: the node
	// plugin only ever formats and mounts a filesystem. Rejecting the request
	// here, where the message reaches the PVC's events, is far kinder than
	// letting provisioning succeed and having kubelet fail much later with an
	// opaque "MapVolume.MapBlockVolume ... bind mount ... exit status 32".
	for _, c := range req.GetVolumeCapabilities() {
		if c.GetBlock() != nil {
			return nil, status.Error(codes.InvalidArgument,
				"volumeMode: Block is not supported by this driver; use volumeMode: Filesystem")
		}
	}
	name := sanitizeResourceName(req.GetName())
	sizeGB := bytesToGiB(req.GetCapacityRange().GetRequiredBytes())

	// Idempotency: if the resource already exists, check capacity range compatibility.
	if existing, err := s.backend.GetResource(ctx, name); err == nil && existing != nil {
		// If the caller specified an exact capacity range (RequiredBytes == LimitBytes),
		// and it differs from what was provisioned, return AlreadyExists per CSI spec.
		cr := req.GetCapacityRange()
		if cr != nil && cr.GetLimitBytes() > 0 && cr.GetRequiredBytes() == cr.GetLimitBytes() {
			existingSizeGB := bytesToGiB(cr.GetRequiredBytes())
			if len(existing.GetVolumes()) > 0 && uint64(existingSizeGB) != existing.GetVolumes()[0].GetSizeGb() {
				return nil, status.Errorf(codes.AlreadyExists, "volume %q already exists with different capacity", name)
			}
		}
		return &csi.CreateVolumeResponse{Volume: &csi.Volume{
			VolumeId:           existing.GetName(),
			CapacityBytes:      int64(sizeGB) * giB,
			AccessibleTopology: accessibleTopology(existing.GetNodes()),
		}}, nil
	}

	params, err := ParseVolumeParams(req.GetParameters())
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}
	if params.ResourceProfile != "" {
		profileClient, ok := s.backend.(interface {
			GetResourceProfile(context.Context, string) (*sdspb.ResourceProfile, error)
		})
		if !ok {
			return nil, status.Error(codes.Internal, "SDS backend does not support resource profiles")
		}
		profile, profileErr := profileClient.GetResourceProfile(ctx, params.ResourceProfile)
		if profileErr != nil {
			return nil, status.Errorf(codes.InvalidArgument, "resource profile %q: %v", params.ResourceProfile, profileErr)
		}
		if _, explicit := req.GetParameters()["pool"]; !explicit {
			params.Pool = profile.Pool
		}
		if _, explicit := req.GetParameters()["replicas"]; !explicit && profile.Replicas > 0 {
			params.Replicas = int(profile.Replicas)
		}
		if _, explicit := req.GetParameters()["storageType"]; !explicit && profile.StorageType != "" {
			params.StorageType = profile.StorageType
		}
		if params.Pool == "" {
			return nil, status.Errorf(codes.InvalidArgument, "resource profile %q does not define a pool", params.ResourceProfile)
		}
	}

	// A volume restored from a snapshot, or cloned from another volume, must be
	// filled before first use. Resolve where the data comes from first: the copy
	// runs on the node holding the source, so that node has to be among the new
	// volume's replicas.
	var source *volumeSource
	if cs := req.GetVolumeContentSource(); cs != nil {
		source, err = s.resolveVolumeSource(ctx, cs)
		if err != nil {
			return nil, err
		}
		if source.cleanup != nil {
			defer source.cleanup()
		}
	}

	nodes, err := s.backend.ListNodes(ctx)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "list nodes: %v", err)
	}
	pools, err := s.backend.ListPools(ctx)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "list pools: %v", err)
	}
	// Only consider nodes that actually host the requested pool: replicas
	// placed on a node without the backing pool fail at LV-creation time.
	candidates := nodesWithPool(nodes, pools, params.Pool)
	requisite := requisiteNodes(req.GetAccessibilityRequirements())
	if source != nil {
		// Put the source's node first so a replica lands there and the copy is
		// local; without this the new volume could be placed entirely elsewhere.
		requisite = append([]string{source.node}, requisite...)
	}
	replicaNodes, err := selectReplicaNodes(candidates, requisite, params.Replicas)
	if err != nil {
		return nil, status.Errorf(codes.ResourceExhausted, "pool %q: %v", params.Pool, err)
	}
	if source != nil && !containsNode(replicaNodes, source.node) {
		return nil, status.Errorf(codes.ResourceExhausted,
			"node %q holds the source data but cannot host a replica of the new volume (pool %q)", source.node, params.Pool)
	}

	labels := make(map[string]string, len(params.ResourceLabels)+1)
	for key, value := range params.ResourceLabels {
		labels[key] = value
	}
	labels["sds.csi/managed-by"] = "csi"
	if backend, ok := s.backend.(interface {
		CreateResourceRequest(context.Context, *sdspb.CreateResourceRequest) error
	}); ok {
		err = backend.CreateResourceRequest(ctx, &sdspb.CreateResourceRequest{
			Name:        name,
			Nodes:       replicaNodes,
			Protocol:    "C",
			SizeGb:      sizeGB,
			Pool:        params.Pool,
			StorageType: params.StorageType,
			Profile:     params.ResourceProfile,
			Labels:      labels,
		})
	} else if params.ResourceProfile != "" || len(params.ResourceLabels) > 0 {
		return nil, status.Error(codes.Internal, "SDS backend does not support resource profiles or labels")
	} else {
		err = s.backend.CreateResourceWithPoolAndType(ctx, name, 0, replicaNodes, "C", sizeGB, params.Pool, params.StorageType, nil)
	}
	if err != nil {
		return nil, status.Errorf(codes.Internal, "create resource: %v", err)
	}

	// Fill the new volume from its source. This must happen before the volume is
	// ever handed out, so on failure the half-written resource is destroyed
	// rather than returned: a retry then starts from a clean, empty volume.
	//
	// (If the controller itself dies mid-copy the resource survives empty, and a
	// retry would take the "already exists" path above and return it as ready.
	// That window is not closed here; it needs persisted provisioning state.)
	if source != nil {
		if _, perr := s.backend.PopulateVolume(ctx, name, 0, source.device, source.node); perr != nil {
			if derr := s.backend.DeleteResource(context.WithoutCancel(ctx), name); derr != nil {
				s.log.Error("failed to roll back a volume whose restore failed; it may need manual cleanup",
					zap.String("volume", name), zap.Error(derr))
			}
			return nil, status.Errorf(codes.Internal, "populate %q from %q: %v", name, source.device, perr)
		}
	}

	return &csi.CreateVolumeResponse{Volume: &csi.Volume{
		VolumeId:           name,
		CapacityBytes:      int64(sizeGB) * giB,
		AccessibleTopology: topologyFor(replicaNodes, params.AllowRemoteVolumeAccess),
		VolumeContext:      volumeContextFor(params.AllowRemoteVolumeAccess),
		ContentSource:      contentSourceOf(req.GetVolumeContentSource()),
	}}, nil
}

// containsNode reports whether nodes includes want.
func containsNode(nodes []string, want string) bool {
	for _, n := range nodes {
		if n == want {
			return true
		}
	}
	return false
}

// topologyFor decides where the CO may schedule Pods that use the volume. By
// default it pins them to the replica nodes (local I/O). With remote access
// enabled the volume is reachable from every node via a diskless client, so we
// impose no topology constraint and let the scheduler place the Pod anywhere.
func topologyFor(replicaNodes []string, allowRemote bool) []*csi.Topology {
	if allowRemote {
		return nil
	}
	return accessibleTopology(replicaNodes)
}

// volumeContextFor carries the remote-access flag into the volume's context so
// the node service can tell, at stage time, whether a Pod on a non-replica node
// is allowed to attach the volume diskless.
func volumeContextFor(allowRemote bool) map[string]string {
	if !allowRemote {
		return nil
	}
	return map[string]string{paramAllowRemoteVolumeAccess: "true"}
}

func (s *controllerServer) DeleteVolume(ctx context.Context, req *csi.DeleteVolumeRequest) (*csi.DeleteVolumeResponse, error) {
	if req.GetVolumeId() == "" {
		return nil, status.Error(codes.InvalidArgument, "volume id is required")
	}
	if _, err := s.backend.GetResource(ctx, req.GetVolumeId()); err != nil {
		// Not found -> already deleted; idempotent success.
		return &csi.DeleteVolumeResponse{}, nil
	}
	if err := s.backend.DeleteResource(ctx, req.GetVolumeId()); err != nil {
		return nil, status.Errorf(codes.Internal, "delete resource: %v", err)
	}
	return &csi.DeleteVolumeResponse{}, nil
}

func (s *controllerServer) ControllerGetCapabilities(context.Context, *csi.ControllerGetCapabilitiesRequest) (*csi.ControllerGetCapabilitiesResponse, error) {
	cap := func(t csi.ControllerServiceCapability_RPC_Type) *csi.ControllerServiceCapability {
		return &csi.ControllerServiceCapability{Type: &csi.ControllerServiceCapability_Rpc{
			Rpc: &csi.ControllerServiceCapability_RPC{Type: t}}}
	}
	return &csi.ControllerGetCapabilitiesResponse{Capabilities: []*csi.ControllerServiceCapability{
		cap(csi.ControllerServiceCapability_RPC_CREATE_DELETE_VOLUME),
		cap(csi.ControllerServiceCapability_RPC_EXPAND_VOLUME),
		// LIST_SNAPSHOTS is deliberately NOT advertised: snapshots live on
		// individual nodes with no cluster-wide index, so the driver cannot
		// enumerate them. Create/Delete are fully supported.
		cap(csi.ControllerServiceCapability_RPC_CREATE_DELETE_SNAPSHOT),
		cap(csi.ControllerServiceCapability_RPC_CLONE_VOLUME),
	}}, nil
}

func (s *controllerServer) ValidateVolumeCapabilities(ctx context.Context, req *csi.ValidateVolumeCapabilitiesRequest) (*csi.ValidateVolumeCapabilitiesResponse, error) {
	if req.GetVolumeId() == "" {
		return nil, status.Error(codes.InvalidArgument, "volume id is required")
	}
	if len(req.GetVolumeCapabilities()) == 0 {
		return nil, status.Error(codes.InvalidArgument, "volume capabilities are required")
	}
	if _, err := s.backend.GetResource(ctx, req.GetVolumeId()); err != nil {
		return nil, status.Errorf(codes.NotFound, "volume %q not found", req.GetVolumeId())
	}
	for _, c := range req.GetVolumeCapabilities() {
		if c.GetAccessMode().GetMode() != csi.VolumeCapability_AccessMode_SINGLE_NODE_WRITER {
			return &csi.ValidateVolumeCapabilitiesResponse{}, nil // unsupported -> empty Confirmed
		}
		// Same reason as in CreateVolume: the node plugin only mounts
		// filesystems, so a block capability must not be confirmed.
		if c.GetBlock() != nil {
			return &csi.ValidateVolumeCapabilitiesResponse{}, nil
		}
	}
	return &csi.ValidateVolumeCapabilitiesResponse{Confirmed: &csi.ValidateVolumeCapabilitiesResponse_Confirmed{
		VolumeCapabilities: req.GetVolumeCapabilities(),
	}}, nil
}

// ControllerExpandVolume resizes the backing DRBD+LVM/ZFS volume on all
// replica nodes. The node plugin follows up with NodeExpandVolume to grow
// the filesystem online.
func (s *controllerServer) ControllerExpandVolume(ctx context.Context, req *csi.ControllerExpandVolumeRequest) (*csi.ControllerExpandVolumeResponse, error) {
	if req.GetVolumeId() == "" {
		return nil, status.Error(codes.InvalidArgument, "volume id is required")
	}
	newSizeGB := bytesToGiB(req.GetCapacityRange().GetRequiredBytes())

	if err := s.backend.ResizeVolume(ctx, req.GetVolumeId(), 0, newSizeGB); err != nil {
		return nil, status.Errorf(codes.Internal, "resize volume: %v", err)
	}

	return &csi.ControllerExpandVolumeResponse{
		CapacityBytes:         int64(newSizeGB) * giB,
		NodeExpansionRequired: true, // filesystem resize needed on the node
	}, nil
}

// bytesToGiB converts a byte count to whole GiB, rounding up; minimum 1.
func bytesToGiB(b int64) uint32 {
	if b <= 0 {
		return 1
	}
	g := (b + giB - 1) / giB
	return uint32(g)
}
