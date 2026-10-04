package controller

import (
	"context"
	"fmt"

	sdspb "github.com/haify-project/sds/api/proto/v1"
)

// Quota RPCs (nfs_quota.go).

func (s *Server) SetNFSExportQuota(ctx context.Context, req *sdspb.SetNFSExportQuotaRequest) (*sdspb.SetNFSExportQuotaResponse, error) {
	if err := s.resources.SetNFSExportQuota(ctx, req.Resource, req.ExportPath, req.SizeBytes); err != nil {
		return &sdspb.SetNFSExportQuotaResponse{Success: false, Message: err.Error()}, nil
	}
	msg := fmt.Sprintf("quota of %d bytes set on %s", req.SizeBytes, req.ExportPath)
	if req.SizeBytes == 0 {
		msg = "quota removed from " + req.ExportPath
	}
	return &sdspb.SetNFSExportQuotaResponse{Success: true, Message: msg}, nil
}
