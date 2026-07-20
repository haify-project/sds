package csi

import (
	"context"

	"github.com/container-storage-interface/spec/lib/go/csi"
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
	replicaNodes, err := selectReplicaNodes(candidates, requisiteNodes(req.GetAccessibilityRequirements()), params.Replicas)
	if err != nil {
		return nil, status.Errorf(codes.ResourceExhausted, "pool %q: %v", params.Pool, err)
	}

	if err := s.backend.CreateResourceWithPoolAndType(ctx, name, 0, replicaNodes, "C", sizeGB, params.Pool, params.StorageType, nil); err != nil {
		return nil, status.Errorf(codes.Internal, "create resource: %v", err)
	}

	return &csi.CreateVolumeResponse{Volume: &csi.Volume{
		VolumeId:           name,
		CapacityBytes:      int64(sizeGB) * giB,
		AccessibleTopology: topologyFor(replicaNodes, params.AllowRemoteVolumeAccess),
		VolumeContext:      volumeContextFor(params.AllowRemoteVolumeAccess),
	}}, nil
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
	}
	return &csi.ValidateVolumeCapabilitiesResponse{Confirmed: &csi.ValidateVolumeCapabilitiesResponse_Confirmed{
		VolumeCapabilities: req.GetVolumeCapabilities(),
	}}, nil
}

// bytesToGiB converts a byte count to whole GiB, rounding up; minimum 1.
func bytesToGiB(b int64) uint32 {
	if b <= 0 {
		return 1
	}
	g := (b + giB - 1) / giB
	return uint32(g)
}
