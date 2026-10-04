package client

import (
	"context"
	"fmt"

	sdspb "github.com/haify-project/sds/api/proto/v1"
)

// CreateSMBGateway creates an SMB gateway.
func (c *SDSClient) CreateSMBGateway(ctx context.Context, req *sdspb.CreateSMBGatewayRequest) (*sdspb.CreateSMBGatewayResponse, error) {
	return c.client.CreateSMBGateway(ctx, req)
}

// AddSMBShare adds a share to a running SMB gateway.
func (c *SDSClient) AddSMBShare(ctx context.Context, resource string, share *sdspb.SMBShareInfo) error {
	resp, err := c.client.AddSMBShare(ctx, &sdspb.AddSMBShareRequest{Resource: resource, Share: share})
	return smbResult(resp, err)
}

// RemoveSMBShare removes a share; its data stays.
func (c *SDSClient) RemoveSMBShare(ctx context.Context, resource, name string) error {
	resp, err := c.client.RemoveSMBShare(ctx, &sdspb.RemoveSMBShareRequest{Resource: resource, Name: name})
	return smbResult(resp, err)
}

// ListSMBShares lists the shares of a running SMB gateway.
func (c *SDSClient) ListSMBShares(ctx context.Context, resource string) ([]*sdspb.SMBShareInfo, error) {
	resp, err := c.client.ListSMBShares(ctx, &sdspb.ListSMBSharesRequest{Resource: resource})
	if err := smbResult(resp, err); err != nil {
		return nil, err
	}
	return resp.Shares, nil
}

// SetSMBUser adds an SMB user or changes its password.
func (c *SDSClient) SetSMBUser(ctx context.Context, resource, user, password string) error {
	resp, err := c.client.SetSMBUser(ctx, &sdspb.SetSMBUserRequest{Resource: resource, User: user, Password: password})
	return smbResult(resp, err)
}

// RemoveSMBUser removes an SMB user.
func (c *SDSClient) RemoveSMBUser(ctx context.Context, resource, user string) error {
	resp, err := c.client.RemoveSMBUser(ctx, &sdspb.RemoveSMBUserRequest{Resource: resource, User: user})
	return smbResult(resp, err)
}

// ListSMBUsers lists the SMB users of a running gateway.
func (c *SDSClient) ListSMBUsers(ctx context.Context, resource string) ([]string, error) {
	resp, err := c.client.ListSMBUsers(ctx, &sdspb.ListSMBUsersRequest{Resource: resource})
	if err := smbResult(resp, err); err != nil {
		return nil, err
	}
	return resp.Users, nil
}

// smbResult folds a response's success flag into its error.
func smbResult(resp interface {
	GetSuccess() bool
	GetMessage() string
}, err error) error {
	if err != nil {
		return err
	}
	if !resp.GetSuccess() {
		return fmt.Errorf("%s", resp.GetMessage())
	}
	return nil
}
