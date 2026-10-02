package controller

import (
	"context"

	sdspb "github.com/liliang-cn/sds/api/proto/v1"
	"github.com/liliang-cn/sds/pkg/inspect"
)

// RunInspection inspects the cluster now and returns the report.
func (s *Server) RunInspection(ctx context.Context, req *sdspb.RunInspectionRequest) (*sdspb.RunInspectionResponse, error) {
	areas := make([]inspect.Area, 0, len(req.Areas))
	for _, a := range req.Areas {
		areas = append(areas, inspect.Area(a))
	}
	r, err := s.ctrl.inspections.Run(ctx, inspect.TriggerManual, areas)
	if err != nil {
		return &sdspb.RunInspectionResponse{Success: false, Message: err.Error(), Report: reportToPB(r, true)}, nil
	}
	return &sdspb.RunInspectionResponse{Success: true, Message: inspect.Headline(r, headlineItems), Report: reportToPB(r, true)}, nil
}

// ListInspections lists stored reports, newest first, without their checks.
func (s *Server) ListInspections(ctx context.Context, req *sdspb.ListInspectionsRequest) (*sdspb.ListInspectionsResponse, error) {
	reports, err := s.ctrl.inspections.List(ctx, int(req.Limit))
	if err != nil {
		return &sdspb.ListInspectionsResponse{Success: false, Message: err.Error()}, nil
	}
	out := make([]*sdspb.InspectionReport, 0, len(reports))
	for _, r := range reports {
		out = append(out, reportToPB(r, false))
	}
	return &sdspb.ListInspectionsResponse{Success: true, Reports: out}, nil
}

// GetInspection returns one stored report, "latest" for the newest.
func (s *Server) GetInspection(ctx context.Context, req *sdspb.GetInspectionRequest) (*sdspb.GetInspectionResponse, error) {
	r, err := s.ctrl.inspections.Get(ctx, req.Id)
	if err != nil {
		return &sdspb.GetInspectionResponse{Success: false, Message: err.Error()}, nil
	}
	return &sdspb.GetInspectionResponse{Success: true, Report: reportToPB(r, true)}, nil
}

func reportToPB(r *inspect.Report, withChecks bool) *sdspb.InspectionReport {
	if r == nil {
		return nil
	}
	out := &sdspb.InspectionReport{
		Id:               r.ID,
		Trigger:          string(r.Trigger),
		StartedAtUnixMs:  r.StartedAt.UnixMilli(),
		FinishedAtUnixMs: r.FinishedAt.UnixMilli(),
		Summary: &sdspb.InspectionSummary{
			Pass: int32(r.Summary.Pass), Warn: int32(r.Summary.Warn),
			Fail: int32(r.Summary.Fail), Error: int32(r.Summary.Error),
		},
	}
	for _, a := range r.Areas {
		out.Areas = append(out.Areas, string(a))
	}
	if !withChecks {
		return out
	}
	for _, c := range r.Checks {
		out.Checks = append(out.Checks, &sdspb.InspectionCheck{
			Id: c.ID, Area: string(c.Area), Subject: c.Subject, Status: string(c.Status),
			Message: c.Message, Evidence: c.Evidence, Fix: c.Fix, Runbook: c.Runbook,
		})
	}
	return out
}
