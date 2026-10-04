package client

import (
	"context"
	"errors"
	"fmt"

	sdspb "github.com/haify-project/sds/api/proto/v1"
)

// ErrApprovalDisabled is returned when the controller runs without
// [rbac.approval].
var ErrApprovalDisabled = errors.New("two-person approval is not enabled on the controller ([rbac.approval])")

// ListApprovals returns the open approval requests, or every one with all.
func (c *SDSClient) ListApprovals(ctx context.Context, all bool) ([]*sdspb.ApprovalInfo, error) {
	resp, err := c.client.ListApprovals(ctx, &sdspb.ListApprovalsRequest{IncludeClosed: all})
	if err != nil {
		return nil, err
	}
	if !resp.Enabled {
		return nil, ErrApprovalDisabled
	}
	return resp.Approvals, nil
}

// ApproveRequest approves another user's pending request.
func (c *SDSClient) ApproveRequest(ctx context.Context, id string) (*sdspb.ApprovalInfo, string, error) {
	resp, err := c.client.ApproveRequest(ctx, &sdspb.ApproveRequestRequest{Id: id})
	if err != nil {
		return nil, "", err
	}
	if !resp.Success {
		return resp.Approval, "", fmt.Errorf("%s", resp.Message)
	}
	return resp.Approval, resp.Message, nil
}

// RejectRequest closes a pending request without running it.
func (c *SDSClient) RejectRequest(ctx context.Context, id string) (*sdspb.ApprovalInfo, error) {
	resp, err := c.client.RejectRequest(ctx, &sdspb.RejectRequestRequest{Id: id})
	if err != nil {
		return nil, err
	}
	if !resp.Success {
		return resp.Approval, fmt.Errorf("%s", resp.Message)
	}
	return resp.Approval, nil
}
