package csi

import (
	"context"

	"github.com/container-storage-interface/spec/lib/go/csi"
	"go.uber.org/zap"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type nodeServer struct {
	csi.UnimplementedNodeServer
	backend  SDSBackend
	mounter  Mounter
	nodeName string
	nodeIP   string
	log      *zap.Logger
}

// NewNodeServer returns the CSI Node service for this node.
func NewNodeServer(b SDSBackend, m Mounter, nodeName, nodeIP string, log *zap.Logger) csi.NodeServer {
	return &nodeServer{backend: b, mounter: m, nodeName: nodeName, nodeIP: nodeIP, log: log}
}

func (s *nodeServer) NodeGetInfo(context.Context, *csi.NodeGetInfoRequest) (*csi.NodeGetInfoResponse, error) {
	return &csi.NodeGetInfoResponse{
		NodeId:             s.nodeName,
		AccessibleTopology: &csi.Topology{Segments: map[string]string{TopologyKeyNode: s.nodeName}},
	}, nil
}

func (s *nodeServer) NodeGetCapabilities(context.Context, *csi.NodeGetCapabilitiesRequest) (*csi.NodeGetCapabilitiesResponse, error) {
	return &csi.NodeGetCapabilitiesResponse{Capabilities: []*csi.NodeServiceCapability{
		{Type: &csi.NodeServiceCapability_Rpc{Rpc: &csi.NodeServiceCapability_RPC{
			Type: csi.NodeServiceCapability_RPC_STAGE_UNSTAGE_VOLUME}}},
	}}, nil
}

func (s *nodeServer) NodeStageVolume(ctx context.Context, req *csi.NodeStageVolumeRequest) (*csi.NodeStageVolumeResponse, error) {
	res := req.GetVolumeId()
	staging := req.GetStagingTargetPath()
	if res == "" || staging == "" {
		return nil, status.Error(codes.InvalidArgument, "volume id and staging path are required")
	}

	// Promote this node to DRBD Primary.
	if err := s.backend.SetPrimary(ctx, res, s.nodeName, false); err != nil {
		return nil, status.Errorf(codes.Internal, "set primary: %v", err)
	}

	device, err := s.deviceFor(ctx, res)
	if err != nil {
		return nil, err
	}

	fsType := req.GetVolumeCapability().GetMount().GetFsType()
	if fsType == "" {
		fsType = "ext4"
	}
	if err := s.mounter.EnsureDir(staging); err != nil {
		return nil, status.Errorf(codes.Internal, "mkdir staging: %v", err)
	}
	mounted, err := s.mounter.IsMountPoint(staging)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "check mount: %v", err)
	}
	if mounted {
		return &csi.NodeStageVolumeResponse{}, nil // idempotent
	}
	if err := s.mounter.FormatAndMount(device, staging, fsType, nil); err != nil {
		// Roll the role back so another node can take over.
		_ = s.backend.SetSecondary(ctx, res, s.nodeName)
		return nil, status.Errorf(codes.Internal, "format+mount: %v", err)
	}
	return &csi.NodeStageVolumeResponse{}, nil
}

func (s *nodeServer) NodeUnstageVolume(ctx context.Context, req *csi.NodeUnstageVolumeRequest) (*csi.NodeUnstageVolumeResponse, error) {
	res := req.GetVolumeId()
	staging := req.GetStagingTargetPath()
	if res == "" || staging == "" {
		return nil, status.Error(codes.InvalidArgument, "volume id and staging path are required")
	}
	if err := s.mounter.Unmount(staging); err != nil {
		return nil, status.Errorf(codes.Internal, "unmount staging: %v", err)
	}
	if err := s.backend.SetSecondary(ctx, res, s.nodeName); err != nil {
		return nil, status.Errorf(codes.Internal, "set secondary: %v", err)
	}
	return &csi.NodeUnstageVolumeResponse{}, nil
}

func (s *nodeServer) NodePublishVolume(ctx context.Context, req *csi.NodePublishVolumeRequest) (*csi.NodePublishVolumeResponse, error) {
	staging := req.GetStagingTargetPath()
	target := req.GetTargetPath()
	if staging == "" || target == "" {
		return nil, status.Error(codes.InvalidArgument, "staging and target paths are required")
	}
	if err := s.mounter.EnsureDir(target); err != nil {
		return nil, status.Errorf(codes.Internal, "mkdir target: %v", err)
	}
	mounted, err := s.mounter.IsMountPoint(target)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "check mount: %v", err)
	}
	if mounted {
		return &csi.NodePublishVolumeResponse{}, nil
	}
	opts := []string{"bind"}
	if req.GetReadonly() {
		opts = append(opts, "ro")
	}
	if err := s.mounter.Mount(staging, target, "", opts); err != nil {
		return nil, status.Errorf(codes.Internal, "bind mount: %v", err)
	}
	return &csi.NodePublishVolumeResponse{}, nil
}

func (s *nodeServer) NodeUnpublishVolume(_ context.Context, req *csi.NodeUnpublishVolumeRequest) (*csi.NodeUnpublishVolumeResponse, error) {
	if req.GetTargetPath() == "" {
		return nil, status.Error(codes.InvalidArgument, "target path is required")
	}
	if err := s.mounter.Unmount(req.GetTargetPath()); err != nil {
		return nil, status.Errorf(codes.Internal, "unmount target: %v", err)
	}
	return &csi.NodeUnpublishVolumeResponse{}, nil
}

// deviceFor returns the /dev/drbdX path of a resource's first volume.
func (s *nodeServer) deviceFor(ctx context.Context, resource string) (string, error) {
	r, err := s.backend.GetResource(ctx, resource)
	if err != nil {
		return "", status.Errorf(codes.NotFound, "get resource %q: %v", resource, err)
	}
	if len(r.GetVolumes()) == 0 || r.GetVolumes()[0].GetDevice() == "" {
		return "", status.Errorf(codes.Internal, "resource %q has no device", resource)
	}
	return r.GetVolumes()[0].GetDevice(), nil
}
