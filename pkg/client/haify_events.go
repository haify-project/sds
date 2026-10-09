package client

import (
	"context"
	"fmt"

	haifypb "github.com/haify-project/haify/api/proto/v1"
)

// ListEvents returns retained notifications, oldest first.
func (c *HaifyClient) ListEvents(ctx context.Context, req *haifypb.ListEventsRequest) (*haifypb.ListEventsResponse, error) {
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
func (c *HaifyClient) WatchEvents(ctx context.Context, req *haifypb.WatchEventsRequest) (haifypb.HaifyController_WatchEventsClient, error) {
	return c.client.WatchEvents(ctx, req)
}

// ListAuditEvents returns the persisted audit trail, newest first.
func (c *HaifyClient) ListAuditEvents(ctx context.Context, req *haifypb.ListAuditEventsRequest) (*haifypb.ListAuditEventsResponse, error) {
	resp, err := c.client.ListAuditEvents(ctx, req)
	if err != nil {
		return nil, err
	}
	if !resp.Success {
		return nil, fmt.Errorf("%s", resp.Message)
	}
	return resp, nil
}

func (c *HaifyClient) ListControllerLogs(ctx context.Context, req *haifypb.ListControllerLogsRequest) (*haifypb.ListControllerLogsResponse, error) {
	resp, err := c.client.ListControllerLogs(ctx, req)
	if err != nil {
		return nil, err
	}
	if !resp.Success {
		return nil, fmt.Errorf("%s", resp.Message)
	}
	return resp, nil
}
