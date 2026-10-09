package client

import (
	"context"
	"fmt"

	haifypb "github.com/haify-project/haify/api/proto/v1"
)

// CreateResourceProfile creates or updates a resource profile.
func (c *HaifyClient) CreateResourceProfile(ctx context.Context, profile *haifypb.ResourceProfile) (*haifypb.ResourceProfile, error) {
	resp, err := c.client.CreateResourceProfile(ctx, &haifypb.CreateResourceProfileRequest{Profile: profile})
	if err != nil {
		return nil, err
	}
	if !resp.Success {
		return nil, fmt.Errorf("%s", resp.Message)
	}
	return resp.Profile, nil
}

// GetResourceProfile gets a resource profile by name.
func (c *HaifyClient) GetResourceProfile(ctx context.Context, name string) (*haifypb.ResourceProfile, error) {
	resp, err := c.client.GetResourceProfile(ctx, &haifypb.GetResourceProfileRequest{Name: name})
	if err != nil {
		return nil, err
	}
	if !resp.Success {
		return nil, fmt.Errorf("%s", resp.Message)
	}
	return resp.Profile, nil
}

// ListResourceProfiles lists resource profiles.
func (c *HaifyClient) ListResourceProfiles(ctx context.Context) ([]*haifypb.ResourceProfile, error) {
	resp, err := c.client.ListResourceProfiles(ctx, &haifypb.ListResourceProfilesRequest{})
	if err != nil {
		return nil, err
	}
	if !resp.Success {
		return nil, fmt.Errorf("%s", resp.Message)
	}
	return resp.Profiles, nil
}

// DeleteResourceProfile deletes a profile that has no members.
func (c *HaifyClient) DeleteResourceProfile(ctx context.Context, name string) error {
	resp, err := c.client.DeleteResourceProfile(ctx, &haifypb.DeleteResourceProfileRequest{Name: name})
	if err != nil {
		return err
	}
	if !resp.Success {
		return fmt.Errorf("%s", resp.Message)
	}
	return nil
}

// ListProfileMembers lists the resources that belong to a profile.
func (c *HaifyClient) ListProfileMembers(ctx context.Context, profile string) ([]*haifypb.ResourceInfo, error) {
	resp, err := c.client.ListResources(ctx, &haifypb.ListResourcesRequest{Profile: profile})
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
func (c *HaifyClient) SetResourceProfileOptions(ctx context.Context, name string, options map[string]string) (*haifypb.SetResourceProfileOptionsResponse, error) {
	return c.client.SetResourceProfileOptions(ctx, &haifypb.SetResourceProfileOptionsRequest{Name: name, Options: options})
}

// AdjustResourceProfile brings every member into line with the profile, or
// with dryRun says what it would change.
func (c *HaifyClient) AdjustResourceProfile(ctx context.Context, name string, dryRun bool) (*haifypb.AdjustResourceProfileResponse, error) {
	return c.client.AdjustResourceProfile(ctx, &haifypb.AdjustResourceProfileRequest{Name: name, DryRun: dryRun})
}

// GetResourceProfileMaxSize is the largest volume a new member could get now.
func (c *HaifyClient) GetResourceProfileMaxSize(ctx context.Context, name string) (*haifypb.GetResourceProfileMaxSizeResponse, error) {
	resp, err := c.client.GetResourceProfileMaxSize(ctx, &haifypb.GetResourceProfileMaxSizeRequest{Name: name})
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
func (c *HaifyClient) SetResourceProfile(ctx context.Context, resource, profile string) error {
	resp, err := c.client.SetResourceProfile(ctx, &haifypb.SetResourceProfileRequest{Resource: resource, Profile: profile})
	if err != nil {
		return err
	}
	if !resp.Success {
		return fmt.Errorf("%s", resp.Message)
	}
	return nil
}
