package controller

import (
	"context"

	haifypb "github.com/haify-project/haify/api/proto/v1"
	"github.com/haify-project/haify/pkg/inspect"
)

// RunInspection inspects the cluster now and returns the report.
func (s *Server) RunInspection(ctx context.Context, req *haifypb.RunInspectionRequest) (*haifypb.RunInspectionResponse, error) {
	areas := make([]inspect.Area, 0, len(req.Areas))
	for _, a := range req.Areas {
		areas = append(areas, inspect.Area(a))
	}
	r, err := s.ctrl.inspections.Run(ctx, inspect.TriggerManual, areas)
	if err != nil {
		return &haifypb.RunInspectionResponse{Success: false, Message: err.Error(), Report: reportToPB(r, true)}, nil
	}
	return &haifypb.RunInspectionResponse{Success: true, Message: inspect.Headline(r, headlineItems), Report: reportToPB(r, true)}, nil
}

// ListInspections lists stored reports, newest first, without their checks.
func (s *Server) ListInspections(ctx context.Context, req *haifypb.ListInspectionsRequest) (*haifypb.ListInspectionsResponse, error) {
	reports, err := s.ctrl.inspections.List(ctx, int(req.Limit))
	if err != nil {
		return &haifypb.ListInspectionsResponse{Success: false, Message: err.Error()}, nil
	}
	out := make([]*haifypb.InspectionReport, 0, len(reports))
	for _, r := range reports {
		out = append(out, reportToPB(r, false))
	}
	return &haifypb.ListInspectionsResponse{Success: true, Reports: out}, nil
}

// GetInspection returns one stored report, "latest" for the newest.
func (s *Server) GetInspection(ctx context.Context, req *haifypb.GetInspectionRequest) (*haifypb.GetInspectionResponse, error) {
	r, err := s.ctrl.inspections.Get(ctx, req.Id)
	if err != nil {
		return &haifypb.GetInspectionResponse{Success: false, Message: err.Error()}, nil
	}
	return &haifypb.GetInspectionResponse{Success: true, Report: reportToPB(r, true)}, nil
}

func reportToPB(r *inspect.Report, withChecks bool) *haifypb.InspectionReport {
	if r == nil {
		return nil
	}
	out := &haifypb.InspectionReport{
		Id:               r.ID,
		Trigger:          string(r.Trigger),
		StartedAtUnixMs:  r.StartedAt.UnixMilli(),
		FinishedAtUnixMs: r.FinishedAt.UnixMilli(),
		Summary: &haifypb.InspectionSummary{
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
		out.Checks = append(out.Checks, &haifypb.InspectionCheck{
			Id: c.ID, Area: string(c.Area), Subject: c.Subject, Status: string(c.Status),
			Message: c.Message, Evidence: c.Evidence, Fix: c.Fix, Runbook: c.Runbook,
		})
	}
	return out
}
