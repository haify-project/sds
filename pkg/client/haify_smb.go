package client

import (
	"context"
	"fmt"

	haifypb "github.com/haify-project/haify/api/proto/v1"
)

// CreateSMBGateway creates an SMB gateway.
func (c *HaifyClient) CreateSMBGateway(ctx context.Context, req *haifypb.CreateSMBGatewayRequest) (*haifypb.CreateSMBGatewayResponse, error) {
	return c.client.CreateSMBGateway(ctx, req)
}

// AddSMBShare adds a share to a running SMB gateway.
func (c *HaifyClient) AddSMBShare(ctx context.Context, resource string, share *haifypb.SMBShareInfo) error {
	resp, err := c.client.AddSMBShare(ctx, &haifypb.AddSMBShareRequest{Resource: resource, Share: share})
	return smbResult(resp, err)
}

// RemoveSMBShare removes a share; its data stays.
func (c *HaifyClient) RemoveSMBShare(ctx context.Context, resource, name string) error {
	resp, err := c.client.RemoveSMBShare(ctx, &haifypb.RemoveSMBShareRequest{Resource: resource, Name: name})
	return smbResult(resp, err)
}

// ListSMBShares lists the shares of a running SMB gateway.
func (c *HaifyClient) ListSMBShares(ctx context.Context, resource string) ([]*haifypb.SMBShareInfo, error) {
	resp, err := c.client.ListSMBShares(ctx, &haifypb.ListSMBSharesRequest{Resource: resource})
	if err := smbResult(resp, err); err != nil {
		return nil, err
	}
	return resp.Shares, nil
}

// SetSMBUser adds an SMB user or changes its password.
func (c *HaifyClient) SetSMBUser(ctx context.Context, resource, user, password string) error {
	resp, err := c.client.SetSMBUser(ctx, &haifypb.SetSMBUserRequest{Resource: resource, User: user, Password: password})
	return smbResult(resp, err)
}

// RemoveSMBUser removes an SMB user.
func (c *HaifyClient) RemoveSMBUser(ctx context.Context, resource, user string) error {
	resp, err := c.client.RemoveSMBUser(ctx, &haifypb.RemoveSMBUserRequest{Resource: resource, User: user})
	return smbResult(resp, err)
}

// ListSMBUsers lists the SMB users of a running gateway.
func (c *HaifyClient) ListSMBUsers(ctx context.Context, resource string) ([]string, error) {
	resp, err := c.client.ListSMBUsers(ctx, &haifypb.ListSMBUsersRequest{Resource: resource})
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
