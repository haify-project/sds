package client

import (
	"context"
	"errors"
	"fmt"

	haifypb "github.com/haify-project/haify/api/proto/v1"
)

// ErrRBACDisabled is returned by the RBAC user-management calls when the
// controller runs without [rbac]; there are no users to manage.
var ErrRBACDisabled = errors.New("RBAC is not enabled on the controller")

// RbacWhoami reports the identity and role of the token this client sends.
// Enabled is false when the controller runs without [rbac].
func (c *HaifyClient) RbacWhoami(ctx context.Context) (*haifypb.GetRbacWhoamiResponse, error) {
	return c.client.GetRbacWhoami(ctx, &haifypb.GetRbacWhoamiRequest{})
}

// ListRbacPolicies returns the effective policy and user assignments. Admin only.
func (c *HaifyClient) ListRbacPolicies(ctx context.Context) (*haifypb.ListRbacPoliciesResponse, error) {
	return c.client.ListRbacPolicies(ctx, &haifypb.ListRbacPoliciesRequest{})
}

// CreateRbacUser adds a user and returns its token. An empty token asks the
// controller to generate one.
func (c *HaifyClient) CreateRbacUser(ctx context.Context, name, role, token string) (string, error) {
	resp, err := c.client.CreateRbacUser(ctx, &haifypb.CreateRbacUserRequest{Name: name, Role: role, Token: token})
	if err != nil {
		return "", err
	}
	if err := rbacResult(resp.Enabled, resp.Success, resp.Message); err != nil {
		return "", err
	}
	return resp.Token, nil
}

// DeleteRbacUser removes a user. Users declared in controller.toml cannot be removed.
func (c *HaifyClient) DeleteRbacUser(ctx context.Context, name string) error {
	resp, err := c.client.DeleteRbacUser(ctx, &haifypb.DeleteRbacUserRequest{Name: name})
	if err != nil {
		return err
	}
	return rbacResult(resp.Enabled, resp.Success, resp.Message)
}

// SetRbacUserRole changes a user's role.
func (c *HaifyClient) SetRbacUserRole(ctx context.Context, name, role string) error {
	resp, err := c.client.SetRbacUserRole(ctx, &haifypb.SetRbacUserRoleRequest{Name: name, Role: role})
	if err != nil {
		return err
	}
	return rbacResult(resp.Enabled, resp.Success, resp.Message)
}

func rbacResult(enabled, success bool, message string) error {
	switch {
	case !enabled:
		return ErrRBACDisabled
	case !success:
		return fmt.Errorf("%s", message)
	}
	return nil
}
