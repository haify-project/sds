package client

import (
	"context"
	"fmt"

	haifypb "github.com/haify-project/haify/api/proto/v1"
)

// RunInspection inspects the cluster now and returns the report. It blocks
// for the whole run: one SSH round to every node plus the database reads.
func (c *HaifyClient) RunInspection(ctx context.Context, areas []string) (*haifypb.InspectionReport, error) {
	resp, err := c.client.RunInspection(ctx, &haifypb.RunInspectionRequest{Areas: areas})
	if err != nil {
		return nil, err
	}
	if !resp.Success {
		return resp.Report, fmt.Errorf("%s", resp.Message)
	}
	return resp.Report, nil
}

// ListInspections returns stored reports, newest first, without checks.
func (c *HaifyClient) ListInspections(ctx context.Context, limit int32) ([]*haifypb.InspectionReport, error) {
	resp, err := c.client.ListInspections(ctx, &haifypb.ListInspectionsRequest{Limit: limit})
	if err != nil {
		return nil, err
	}
	if !resp.Success {
		return nil, fmt.Errorf("%s", resp.Message)
	}
	return resp.Reports, nil
}

// GetInspection returns one stored report; id "latest" is the newest.
func (c *HaifyClient) GetInspection(ctx context.Context, id string) (*haifypb.InspectionReport, error) {
	resp, err := c.client.GetInspection(ctx, &haifypb.GetInspectionRequest{Id: id})
	if err != nil {
		return nil, err
	}
	if !resp.Success {
		return nil, fmt.Errorf("%s", resp.Message)
	}
	return resp.Report, nil
}
