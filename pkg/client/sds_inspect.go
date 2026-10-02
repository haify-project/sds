package client

import (
	"context"
	"fmt"

	sdspb "github.com/haify-project/sds/api/proto/v1"
)

// RunInspection inspects the cluster now and returns the report. It blocks
// for the whole run: one SSH round to every node plus the database reads.
func (c *SDSClient) RunInspection(ctx context.Context, areas []string) (*sdspb.InspectionReport, error) {
	resp, err := c.client.RunInspection(ctx, &sdspb.RunInspectionRequest{Areas: areas})
	if err != nil {
		return nil, err
	}
	if !resp.Success {
		return resp.Report, fmt.Errorf("%s", resp.Message)
	}
	return resp.Report, nil
}

// ListInspections returns stored reports, newest first, without checks.
func (c *SDSClient) ListInspections(ctx context.Context, limit int32) ([]*sdspb.InspectionReport, error) {
	resp, err := c.client.ListInspections(ctx, &sdspb.ListInspectionsRequest{Limit: limit})
	if err != nil {
		return nil, err
	}
	if !resp.Success {
		return nil, fmt.Errorf("%s", resp.Message)
	}
	return resp.Reports, nil
}

// GetInspection returns one stored report; id "latest" is the newest.
func (c *SDSClient) GetInspection(ctx context.Context, id string) (*sdspb.InspectionReport, error) {
	resp, err := c.client.GetInspection(ctx, &sdspb.GetInspectionRequest{Id: id})
	if err != nil {
		return nil, err
	}
	if !resp.Success {
		return nil, fmt.Errorf("%s", resp.Message)
	}
	return resp.Report, nil
}
