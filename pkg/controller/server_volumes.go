package controller

import (
	"context"
	"fmt"

	haifypb "github.com/haify-project/haify/api/proto/v1"
)

func (s *Server) AddVolume(ctx context.Context, req *haifypb.AddVolumeRequest) (*haifypb.AddVolumeResponse, error) {
	err := s.resources.AddVolume(ctx, req.Resource, req.Volume, req.Pool, req.SizeGb)
	if err != nil {
		return &haifypb.AddVolumeResponse{
			Success: false,
			Message: err.Error(),
		}, nil
	}
	return &haifypb.AddVolumeResponse{
		Success: true,
		Message: "Volume added successfully",
	}, nil
}

func (s *Server) RemoveVolume(ctx context.Context, req *haifypb.RemoveVolumeRequest) (*haifypb.RemoveVolumeResponse, error) {
	err := s.resources.RemoveVolume(ctx, req.Resource, req.VolumeId)
	if err != nil {
		return &haifypb.RemoveVolumeResponse{
			Success: false,
			Message: err.Error(),
		}, nil
	}
	return &haifypb.RemoveVolumeResponse{
		Success: true,
		Message: "Volume removed successfully",
	}, nil
}

// RenameResource renames a resource (resource_rename.go).
func (s *Server) RenameResource(ctx context.Context, req *haifypb.RenameResourceRequest) (*haifypb.RenameResourceResponse, error) {
	if err := s.resources.RenameResource(ctx, req.Name, req.NewName); err != nil {
		return &haifypb.RenameResourceResponse{Success: false, Message: err.Error()}, nil
	}
	return &haifypb.RenameResourceResponse{Success: true,
		Message: fmt.Sprintf("resource %s renamed to %s", req.Name, req.NewName)}, nil
}

func (s *Server) ResizeVolume(ctx context.Context, req *haifypb.ResizeVolumeRequest) (*haifypb.ResizeVolumeResponse, error) {
	var err error
	if req.SizeBytes > 0 {
		err = s.resources.ResizeVolumeBytes(ctx, req.Resource, req.VolumeId, req.SizeBytes, req.IgnoreFreeSpace)
	} else {
		err = s.resources.ResizeVolumeOptions(ctx, req.Resource, req.VolumeId, uint64(req.SizeGb), req.IgnoreFreeSpace)
	}
	if err != nil {
		return &haifypb.ResizeVolumeResponse{
			Success: false,
			Message: err.Error(),
		}, nil
	}
	return &haifypb.ResizeVolumeResponse{
		Success: true,
		Message: "Volume resized successfully",
	}, nil
}

func (s *Server) CreateFilesystem(ctx context.Context, req *haifypb.CreateFilesystemRequest) (*haifypb.CreateFilesystemResponse, error) {
	// CreateFilesystem is implemented as part of Mount operation
	// This is a convenience wrapper that only creates filesystem
	err := s.resources.CreateFilesystemOnly(ctx, req.Resource, req.VolumeId, req.Fstype, req.Node)
	if err != nil {
		return &haifypb.CreateFilesystemResponse{
			Success: false,
			Message: err.Error(),
		}, nil
	}
	return &haifypb.CreateFilesystemResponse{
		Success: true,
		Message: "Filesystem created successfully",
	}, nil
}

func (s *Server) MountResource(ctx context.Context, req *haifypb.MountResourceRequest) (*haifypb.MountResourceResponse, error) {
	err := s.resources.Mount(ctx, req.Resource, req.Path, req.VolumeId, req.Node, req.Fstype)
	if err != nil {
		return &haifypb.MountResourceResponse{
			Success: false,
			Message: err.Error(),
		}, nil
	}
	return &haifypb.MountResourceResponse{
		Success: true,
		Message: "Resource mounted successfully",
	}, nil
}

func (s *Server) UnmountResource(ctx context.Context, req *haifypb.UnmountResourceRequest) (*haifypb.UnmountResourceResponse, error) {
	err := s.resources.Unmount(ctx, req.Resource, req.VolumeId, req.Node)
	if err != nil {
		return &haifypb.UnmountResourceResponse{
			Success: false,
			Message: err.Error(),
		}, nil
	}
	return &haifypb.UnmountResourceResponse{
		Success: true,
		Message: "Resource unmounted successfully",
	}, nil
}
