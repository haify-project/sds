package csi

import (
	"context"
	"os"
	"sync"

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

	// singleWriter records, for volumes published with the single-writer
	// access mode (ReadWriteOncePod), the one target path each is mounted at.
	// The spec makes the plugin refuse a second one. Kubelet enforces the same
	// rule, so losing this on a restart weakens the check but does not remove it.
	mu           sync.Mutex
	singleWriter map[string]string
}

// NewNodeServer returns the CSI Node service for this node.
func NewNodeServer(b SDSBackend, m Mounter, nodeName, nodeIP string, log *zap.Logger) csi.NodeServer {
	return &nodeServer{backend: b, mounter: m, nodeName: nodeName, nodeIP: nodeIP, log: log,
		singleWriter: map[string]string{}}
}

func (s *nodeServer) NodeGetInfo(context.Context, *csi.NodeGetInfoRequest) (*csi.NodeGetInfoResponse, error) {
	return &csi.NodeGetInfoResponse{
		NodeId:             s.nodeName,
		AccessibleTopology: &csi.Topology{Segments: map[string]string{TopologyKeyNode: s.nodeName}},
	}, nil
}

func (s *nodeServer) NodeGetCapabilities(context.Context, *csi.NodeGetCapabilitiesRequest) (*csi.NodeGetCapabilitiesResponse, error) {
	rpc := func(t csi.NodeServiceCapability_RPC_Type) *csi.NodeServiceCapability {
		return &csi.NodeServiceCapability{Type: &csi.NodeServiceCapability_Rpc{
			Rpc: &csi.NodeServiceCapability_RPC{Type: t}}}
	}
	return &csi.NodeGetCapabilitiesResponse{Capabilities: []*csi.NodeServiceCapability{
		rpc(csi.NodeServiceCapability_RPC_STAGE_UNSTAGE_VOLUME),
		rpc(csi.NodeServiceCapability_RPC_EXPAND_VOLUME),
		rpc(csi.NodeServiceCapability_RPC_GET_VOLUME_STATS),
		// ReadWriteOncePod. DRBD allows one Primary per resource, so a volume is
		// already single-node; this lets kubelet enforce single-Pod on top.
		rpc(csi.NodeServiceCapability_RPC_SINGLE_NODE_MULTI_WRITER),
	}}, nil
}

func (s *nodeServer) NodeStageVolume(ctx context.Context, req *csi.NodeStageVolumeRequest) (*csi.NodeStageVolumeResponse, error) {
	res := req.GetVolumeId()
	staging := req.GetStagingTargetPath()
	if res == "" || staging == "" {
		return nil, status.Error(codes.InvalidArgument, "volume id and staging path are required")
	}
	if req.GetVolumeCapability() == nil {
		return nil, status.Error(codes.InvalidArgument, "volume capability is required")
	}

	// If this node holds no local replica, the volume cannot be promoted here
	// until the node joins the resource. When the StorageClass opted the volume
	// into remote access, attach a diskless client first (idempotent): the node
	// joins over DRBD with no local storage and becomes promotable, serving I/O
	// over the network. Without that opt-in a non-replica node is an error — the
	// scheduler should never have placed the Pod here.
	isReplica, err := s.nodeHoldsReplica(ctx, res)
	if err != nil {
		return nil, err
	}
	if !isReplica {
		if !allowsRemoteAccess(req.GetVolumeContext()) {
			return nil, status.Errorf(codes.FailedPrecondition,
				"node %q holds no replica of %q and the volume does not allow remote access (set allowRemoteVolumeAccess on the StorageClass)", s.nodeName, res)
		}
		if err := s.backend.AttachDisklessClient(ctx, res, s.nodeName); err != nil {
			return nil, status.Errorf(codes.Internal, "attach diskless client: %v", err)
		}
	}

	// Promote this node to DRBD Primary using a quorum-guarded promote.
	//
	// A graceful move (old node demoted first) succeeds via the normal
	// non-forced promote inside PromoteForNode. A HARD node failure leaves the
	// old Primary undemoted, so a plain promote would fail and the volume would
	// never come up here. PromoteForNode handles that by force-promoting ONLY if
	// this node currently holds DRBD quorum (majority) — a partitioned old
	// Primary that lost quorum is blocked from I/O, so forcing is safe. If this
	// node lacks quorum the controller refuses, and we surface that error rather
	// than risk a dual-Primary split-brain.
	if err := s.backend.PromoteForNode(ctx, res, s.nodeName); err != nil {
		return nil, status.Errorf(codes.Internal, "promote (quorum-guarded): %v", err)
	}

	device, err := s.deviceFor(ctx, res)
	if err != nil {
		return nil, err
	}

	// A raw block volume is handed to the Pod as the device itself, bind-mounted
	// at publish time. There is nothing to format and nothing to mount at the
	// staging path; promoting this node is the whole of staging.
	if req.GetVolumeCapability().GetBlock() != nil {
		return &csi.NodeStageVolumeResponse{}, nil
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
	// A node with no local replica only participates because a diskless client
	// was attached at stage time; detach it now so the resource sheds the stale
	// connection once the Pod leaves. A replica node is left in place. Detach is
	// idempotent and best-effort — a stale client is harmless and the next stage
	// re-attaches, so a detach error must not fail unstage.
	if isReplica, err := s.nodeHoldsReplica(ctx, res); err == nil && !isReplica {
		if err := s.backend.DetachDisklessClient(ctx, res, s.nodeName); err != nil {
			s.log.Warn("detach diskless client on unstage failed (leaving it in place)",
				zap.String("resource", res), zap.String("node", s.nodeName), zap.Error(err))
		}
	}
	return &csi.NodeUnstageVolumeResponse{}, nil
}

// nodeHoldsReplica reports whether this node is one of the resource's diskful
// replica nodes (as opposed to a diskless client or an unrelated node).
func (s *nodeServer) nodeHoldsReplica(ctx context.Context, resource string) (bool, error) {
	r, err := s.backend.GetResource(ctx, resource)
	if err != nil {
		return false, status.Errorf(codes.NotFound, "get resource %q: %v", resource, err)
	}
	for _, n := range r.GetNodes() {
		if n == s.nodeName {
			return true, nil
		}
	}
	return false, nil
}

// allowsRemoteAccess reads the remote-access flag propagated from the volume's
// StorageClass parameters into its VolumeContext.
func allowsRemoteAccess(volumeContext map[string]string) bool {
	return volumeContext[paramAllowRemoteVolumeAccess] == "true"
}

func (s *nodeServer) NodePublishVolume(ctx context.Context, req *csi.NodePublishVolumeRequest) (*csi.NodePublishVolumeResponse, error) {
	staging := req.GetStagingTargetPath()
	target := req.GetTargetPath()
	if staging == "" || target == "" {
		return nil, status.Error(codes.InvalidArgument, "staging and target paths are required")
	}
	if req.GetVolumeCapability() == nil {
		return nil, status.Error(codes.InvalidArgument, "volume capability is required")
	}
	if err := s.claimSingleWriter(req); err != nil {
		return nil, err
	}
	if req.GetVolumeCapability().GetBlock() != nil {
		return s.publishBlock(ctx, req)
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
	target := req.GetTargetPath()
	if err := s.mounter.Unmount(target); err != nil {
		return nil, status.Errorf(codes.Internal, "unmount target: %v", err)
	}
	s.mu.Lock()
	if s.singleWriter[req.GetVolumeId()] == target {
		delete(s.singleWriter, req.GetVolumeId())
	}
	s.mu.Unlock()
	// CSI spec requires the SP to delete the target_path after a successful unpublish.
	if err := os.Remove(target); err != nil && !os.IsNotExist(err) {
		return nil, status.Errorf(codes.Internal, "remove target: %v", err)
	}
	return &csi.NodeUnpublishVolumeResponse{}, nil
}

// NodeExpandVolume resizes the filesystem at the staging path to fill the
// expanded block device. Called by kubelet after ControllerExpandVolume
// succeeds and the volume is re-staged on this node.
func (s *nodeServer) NodeExpandVolume(ctx context.Context, req *csi.NodeExpandVolumeRequest) (*csi.NodeExpandVolumeResponse, error) {
	if req.GetVolumeId() == "" || req.GetVolumePath() == "" {
		return nil, status.Error(codes.InvalidArgument, "volume id and volume path are required")
	}

	// A raw block volume has no filesystem to grow: the device itself got
	// bigger in ControllerExpandVolume, and what the Pod sees is that device.
	if req.GetVolumeCapability().GetBlock() != nil {
		return &csi.NodeExpandVolumeResponse{CapacityBytes: req.GetCapacityRange().GetRequiredBytes()}, nil
	}

	// Resolve the block device for this resource so resize2fs / xfs_growfs
	// knows which device backs the mount.
	devicePath, err := s.deviceFor(ctx, req.GetVolumeId())
	if err != nil {
		return nil, err
	}

	if err := s.mounter.ResizeFS(devicePath, req.GetVolumePath()); err != nil {
		return nil, status.Errorf(codes.Internal, "resize filesystem: %v", err)
	}

	newSize := req.GetCapacityRange().GetRequiredBytes()
	return &csi.NodeExpandVolumeResponse{CapacityBytes: newSize}, nil
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

// publishBlock hands a raw block volume to a Pod: kubelet asks for it at a file
// path, and the DRBD device node is bind-mounted onto that file.
func (s *nodeServer) publishBlock(ctx context.Context, req *csi.NodePublishVolumeRequest) (*csi.NodePublishVolumeResponse, error) {
	target := req.GetTargetPath()
	device, err := s.deviceFor(ctx, req.GetVolumeId())
	if err != nil {
		return nil, err
	}
	if err := s.mounter.EnsureFile(target); err != nil {
		return nil, status.Errorf(codes.Internal, "create block target: %v", err)
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
	if err := s.mounter.Mount(device, target, "", opts); err != nil {
		return nil, status.Errorf(codes.Internal, "bind mount %s onto %s: %v", device, target, err)
	}
	return &csi.NodePublishVolumeResponse{}, nil
}

// NodeGetVolumeStats reports what a volume is using, which kubelet exports as
// the kubelet_volume_stats_* metrics. Without it a PVC's usage is invisible to
// Kubernetes: no dashboard shows it filling, and no alert can fire before it
// is full.
func (s *nodeServer) NodeGetVolumeStats(_ context.Context, req *csi.NodeGetVolumeStatsRequest) (*csi.NodeGetVolumeStatsResponse, error) {
	path := req.GetVolumePath()
	if req.GetVolumeId() == "" || path == "" {
		return nil, status.Error(codes.InvalidArgument, "volume id and volume path are required")
	}
	fi, err := os.Stat(path)
	if os.IsNotExist(err) {
		return nil, status.Errorf(codes.NotFound, "volume path %q does not exist", path)
	}
	if err != nil {
		return nil, status.Errorf(codes.Internal, "stat %q: %v", path, err)
	}

	// A raw block volume is published onto a file, a filesystem volume onto a
	// directory. For block there is only a size; what is "used" inside it is the
	// Pod's business and not something the node can see.
	if !fi.IsDir() {
		size, err := s.mounter.BlockSize(path)
		if err != nil {
			return nil, status.Errorf(codes.Internal, "block size of %q: %v", path, err)
		}
		return &csi.NodeGetVolumeStatsResponse{Usage: []*csi.VolumeUsage{
			{Unit: csi.VolumeUsage_BYTES, Total: size},
		}}, nil
	}

	st, err := s.mounter.FSStats(path)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "statfs %q: %v", path, err)
	}
	return &csi.NodeGetVolumeStatsResponse{Usage: []*csi.VolumeUsage{
		{Unit: csi.VolumeUsage_BYTES, Total: st.TotalBytes, Available: st.AvailableBytes, Used: st.UsedBytes},
		{Unit: csi.VolumeUsage_INODES, Total: st.TotalInodes, Available: st.AvailableInodes, Used: st.UsedInodes},
	}}, nil
}

// claimSingleWriter enforces ReadWriteOncePod on this node: a volume published
// with the single-writer mode may be mounted at one target path at a time.
// Publishing again at the same path is the idempotent retry the spec allows.
func (s *nodeServer) claimSingleWriter(req *csi.NodePublishVolumeRequest) error {
	if req.GetVolumeCapability().GetAccessMode().GetMode() != csi.VolumeCapability_AccessMode_SINGLE_NODE_SINGLE_WRITER {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	vol, target := req.GetVolumeId(), req.GetTargetPath()
	if held, ok := s.singleWriter[vol]; ok && held != target {
		return status.Errorf(codes.FailedPrecondition,
			"volume %q is ReadWriteOncePod and already published at %q", vol, held)
	}
	s.singleWriter[vol] = target
	return nil
}
