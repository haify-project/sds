package client

import (
	"context"
	"fmt"

	sdspb "github.com/haify-project/sds/api/proto/v1"
)

// CreateResourceProfile creates or updates a resource profile.
func (c *SDSClient) CreateResourceProfile(ctx context.Context, profile *sdspb.ResourceProfile) (*sdspb.ResourceProfile, error) {
	resp, err := c.client.CreateResourceProfile(ctx, &sdspb.CreateResourceProfileRequest{Profile: profile})
	if err != nil {
		return nil, err
	}
	if !resp.Success {
		return nil, fmt.Errorf("%s", resp.Message)
	}
	return resp.Profile, nil
}

// GetResourceProfile gets a resource profile by name.
func (c *SDSClient) GetResourceProfile(ctx context.Context, name string) (*sdspb.ResourceProfile, error) {
	resp, err := c.client.GetResourceProfile(ctx, &sdspb.GetResourceProfileRequest{Name: name})
	if err != nil {
		return nil, err
	}
	if !resp.Success {
		return nil, fmt.Errorf("%s", resp.Message)
	}
	return resp.Profile, nil
}

// ListResourceProfiles lists resource profiles.
func (c *SDSClient) ListResourceProfiles(ctx context.Context) ([]*sdspb.ResourceProfile, error) {
	resp, err := c.client.ListResourceProfiles(ctx, &sdspb.ListResourceProfilesRequest{})
	if err != nil {
		return nil, err
	}
	if !resp.Success {
		return nil, fmt.Errorf("%s", resp.Message)
	}
	return resp.Profiles, nil
}

// DeleteResourceProfile deletes a profile that has no members.
func (c *SDSClient) DeleteResourceProfile(ctx context.Context, name string) error {
	resp, err := c.client.DeleteResourceProfile(ctx, &sdspb.DeleteResourceProfileRequest{Name: name})
	if err != nil {
		return err
	}
	if !resp.Success {
		return fmt.Errorf("%s", resp.Message)
	}
	return nil
}

// ListProfileMembers lists the resources that belong to a profile.
func (c *SDSClient) ListProfileMembers(ctx context.Context, profile string) ([]*sdspb.ResourceInfo, error) {
	resp, err := c.client.ListResources(ctx, &sdspb.ListResourcesRequest{Profile: profile})
	if err != nil {
		return nil, err
	}
	if !resp.Success {
		return nil, fmt.Errorf("%s", resp.Message)
	}
	return resp.Resources, nil
}

// SetResourceProfileOptions records DRBD options on a profile and applies
// them to every member; the response has each member's outcome.
func (c *SDSClient) SetResourceProfileOptions(ctx context.Context, name string, options map[string]string) (*sdspb.SetResourceProfileOptionsResponse, error) {
	return c.client.SetResourceProfileOptions(ctx, &sdspb.SetResourceProfileOptionsRequest{Name: name, Options: options})
}

// AdjustResourceProfile brings every member into line with the profile, or
// with dryRun says what it would change.
func (c *SDSClient) AdjustResourceProfile(ctx context.Context, name string, dryRun bool) (*sdspb.AdjustResourceProfileResponse, error) {
	return c.client.AdjustResourceProfile(ctx, &sdspb.AdjustResourceProfileRequest{Name: name, DryRun: dryRun})
}

// GetResourceProfileMaxSize is the largest volume a new member could get now.
func (c *SDSClient) GetResourceProfileMaxSize(ctx context.Context, name string) (*sdspb.GetResourceProfileMaxSizeResponse, error) {
	resp, err := c.client.GetResourceProfileMaxSize(ctx, &sdspb.GetResourceProfileMaxSizeRequest{Name: name})
	if err != nil {
		return nil, err
	}
	if !resp.Success {
		return nil, fmt.Errorf("%s", resp.Message)
	}
	return resp, nil
}

// SetResourceProfile makes a resource a member of a profile, or takes it out
// of the one it is in when profile is empty.
func (c *SDSClient) SetResourceProfile(ctx context.Context, resource, profile string) error {
	resp, err := c.client.SetResourceProfile(ctx, &sdspb.SetResourceProfileRequest{Resource: resource, Profile: profile})
	if err != nil {
		return err
	}
	if !resp.Success {
		return fmt.Errorf("%s", resp.Message)
	}
	return nil
}
