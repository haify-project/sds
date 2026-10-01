package client

import (
	"context"
	"fmt"

	sdspb "github.com/liliang-cn/sds/api/proto/v1"
)

// ListEvents returns retained notifications, oldest first.
func (c *SDSClient) ListEvents(ctx context.Context, req *sdspb.ListEventsRequest) (*sdspb.ListEventsResponse, error) {
	resp, err := c.client.ListEvents(ctx, req)
	if err != nil {
		return nil, err
	}
	if !resp.Success {
		return nil, fmt.Errorf("%s", resp.Message)
	}
	return resp, nil
}

// WatchEvents opens a notification stream. The caller reads until Recv returns
// an error; cancelling ctx closes the stream.
func (c *SDSClient) WatchEvents(ctx context.Context, req *sdspb.WatchEventsRequest) (sdspb.SDSController_WatchEventsClient, error) {
	return c.client.WatchEvents(ctx, req)
}

// ListAuditEvents returns the persisted audit trail, newest first.
func (c *SDSClient) ListAuditEvents(ctx context.Context, req *sdspb.ListAuditEventsRequest) (*sdspb.ListAuditEventsResponse, error) {
	resp, err := c.client.ListAuditEvents(ctx, req)
	if err != nil {
		return nil, err
	}
	if !resp.Success {
		return nil, fmt.Errorf("%s", resp.Message)
	}
	return resp, nil
}

func (c *SDSClient) ListControllerLogs(ctx context.Context, req *sdspb.ListControllerLogsRequest) (*sdspb.ListControllerLogsResponse, error) {
	resp, err := c.client.ListControllerLogs(ctx, req)
	if err != nil {
		return nil, err
	}
	if !resp.Success {
		return nil, fmt.Errorf("%s", resp.Message)
	}
	return resp, nil
}
