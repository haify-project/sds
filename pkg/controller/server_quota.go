package controller

import (
	"context"
	"fmt"

	haifypb "github.com/haify-project/haify/api/proto/v1"
)

// Quota RPCs (nfs_quota.go).

func (s *Server) SetNFSExportQuota(ctx context.Context, req *haifypb.SetNFSExportQuotaRequest) (*haifypb.SetNFSExportQuotaResponse, error) {
	if err := s.resources.SetNFSExportQuota(ctx, req.Resource, req.ExportPath, req.SizeBytes); err != nil {
		return &haifypb.SetNFSExportQuotaResponse{Success: false, Message: err.Error()}, nil
	}
	msg := fmt.Sprintf("quota of %d bytes set on %s", req.SizeBytes, req.ExportPath)
	if req.SizeBytes == 0 {
		msg = "quota removed from " + req.ExportPath
	}
	return &haifypb.SetNFSExportQuotaResponse{Success: true, Message: msg}, nil
}
