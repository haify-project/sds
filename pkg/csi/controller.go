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
	var nodeNames []string
	for _, n := range nodes {
		nodeNames = append(nodeNames, n.GetName())
	}
	replicaNodes, err := selectReplicaNodes(nodeNames, requisiteNodes(req.GetAccessibilityRequirements()), params.Replicas)
	if err != nil {
		return nil, status.Error(codes.ResourceExhausted, err.Error())
	}

	if err := s.backend.CreateResourceWithPoolAndType(ctx, name, 0, replicaNodes, "C", sizeGB, params.Pool, params.StorageType, nil); err != nil {
		return nil, status.Errorf(codes.Internal, "create resource: %v", err)
	}

	return &csi.CreateVolumeResponse{Volume: &csi.Volume{
		VolumeId:           name,
		CapacityBytes:      int64(sizeGB) * giB,
		AccessibleTopology: accessibleTopology(replicaNodes),
	}}, nil
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
