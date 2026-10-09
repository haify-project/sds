package client

import (
	"context"
	"fmt"

	haifypb "github.com/haify-project/haify/api/proto/v1"
)

// MakeHa creates a drbd-reactor promoter config for HA failover. Optional
// ocfAgents are appended, in order, to the promoter start[] list; pass nil to
// keep the historical behavior.
func (c *HaifyClient) MakeHa(ctx context.Context, resource string, services []string, mountPoint, fsType, vip string, ocfAgents []*haifypb.OcfAgent, startItems []*haifypb.HaStartItem) (string, error) {
	req := &haifypb.MakeHaRequest{
		Resource:   resource,
		Services:   services,
		MountPoint: mountPoint,
		Fstype:     fsType,
		Vip:        vip,
		OcfAgents:  ocfAgents,
		StartItems: startItems,
	}

	resp, err := c.client.MakeHa(ctx, req)
	if err != nil {
		return "", err
	}

	if !resp.Success {
		return "", fmt.Errorf("%s", resp.Message)
	}

	return resp.ConfigPath, nil
}

// EnableSelfHa makes the controller itself highly available. It returns the
// metadata resource name and the node-local handoff log path.
func (c *HaifyClient) EnableSelfHa(ctx context.Context, vip, pool string, sizeGB, port uint32, nodes []string) (string, string, error) {
	resp, err := c.client.EnableSelfHa(ctx, &haifypb.EnableSelfHaRequest{
		Vip:    vip,
		Pool:   pool,
		SizeGb: sizeGB,
		Port:   port,
		Nodes:  nodes,
	})
	if err != nil {
		return "", "", err
	}
	if !resp.Success {
		return "", "", fmt.Errorf("%s", resp.Message)
	}
	return resp.Resource, resp.HandoffLog, nil
}

// DisableSelfHa reverts the controller to standalone operation on node.
func (c *HaifyClient) DisableSelfHa(ctx context.Context, node string) error {
	resp, err := c.client.DisableSelfHa(ctx, &haifypb.DisableSelfHaRequest{Node: node})
	if err != nil {
		return err
	}
	if !resp.Success {
		return fmt.Errorf("%s", resp.Message)
	}
	return nil
}

// SelfHaStatus describes the controller self-HA state.
type SelfHaStatus struct {
	Enabled    bool
	Resource   string
	VIP        string
	Nodes      []string
	ActiveNode string
}

// GetSelfHaStatus reports the controller self-HA state.
func (c *HaifyClient) GetSelfHaStatus(ctx context.Context) (*SelfHaStatus, error) {
	resp, err := c.client.GetSelfHaStatus(ctx, &haifypb.GetSelfHaStatusRequest{})
	if err != nil {
		return nil, err
	}
	if !resp.Success {
		return nil, fmt.Errorf("%s", resp.Message)
	}
	return &SelfHaStatus{
		Enabled:    resp.Enabled,
		Resource:   resp.Resource,
		VIP:        resp.Vip,
		Nodes:      resp.Nodes,
		ActiveNode: resp.ActiveNode,
	}, nil
}

func (c *HaifyClient) EvictHa(ctx context.Context, resource string) error {
	req := &haifypb.EvictHaRequest{
		Resource: resource,
	}

	resp, err := c.client.EvictHa(ctx, req)
	if err != nil {
		return err
	}

	if !resp.Success {
		return fmt.Errorf("%s", resp.Message)
	}

	return nil
}

// DeleteHa deletes an HA configuration
func (c *HaifyClient) DeleteHa(ctx context.Context, resource string) error {
	req := &haifypb.DeleteHaRequest{
		Resource: resource,
	}

	resp, err := c.client.DeleteHa(ctx, req)
	if err != nil {
		return err
	}

	if !resp.Success {
		return fmt.Errorf("%s", resp.Message)
	}

	return nil
}

// GetHa gets an HA configuration
func (c *HaifyClient) GetHa(ctx context.Context, resource string) (*haifypb.HaConfigInfo, error) {
	req := &haifypb.GetHaRequest{
		Resource: resource,
	}

	resp, err := c.client.GetHa(ctx, req)
	if err != nil {
		return nil, err
	}

	if !resp.Success {
		return nil, fmt.Errorf("%s", resp.Message)
	}

	return resp.Config, nil
}

// ListHa lists all HA configurations
func (c *HaifyClient) ListHa(ctx context.Context) ([]*haifypb.HaConfigInfo, error) {
	req := &haifypb.ListHaRequest{}

	resp, err := c.client.ListHa(ctx, req)
	if err != nil {
		return nil, err
	}

	if !resp.Success {
		return nil, fmt.Errorf("%s", resp.Message)
	}

	return resp.Configs, nil
}

// GetHaStatus retrieves drbd-reactor promoter status for one or all HA
// resources by querying the primary node via the controller's SSH dispatch.
func (c *HaifyClient) GetHaStatus(ctx context.Context, resource string) ([]*haifypb.HaPromoterStatus, error) {
	resp, err := c.client.GetHaStatus(ctx, &haifypb.GetHaStatusRequest{Resource: resource})
	if err != nil {
		return nil, err
	}
	if !resp.Success {
		return nil, fmt.Errorf("%s", resp.Message)
	}
	return resp.Promoters, nil
}

// ListResourceAgents lists the OCF resource agents available on the nodes.
func (c *HaifyClient) ListResourceAgents(ctx context.Context) ([]*haifypb.ResourceAgentInfo, error) {
	resp, err := c.client.ListResourceAgents(ctx, &haifypb.ListResourceAgentsRequest{})
	if err != nil {
		return nil, err
	}
	return resp.Agents, nil
}

// GetResourceAgentMetadata returns an OCF agent's parsed meta-data schema.
func (c *HaifyClient) GetResourceAgentMetadata(ctx context.Context, provider, name string) (*haifypb.GetResourceAgentMetadataResponse, error) {
	resp, err := c.client.GetResourceAgentMetadata(ctx, &haifypb.GetResourceAgentMetadataRequest{
		Provider: provider,
		Name:     name,
	})
	if err != nil {
		return nil, err
	}
	return resp, nil
}

// GetHaToml reads a resource's drbd-reactor promoter TOML.
func (c *HaifyClient) GetHaToml(ctx context.Context, resource string) (*haifypb.GetHaTomlResponse, error) {
	resp, err := c.client.GetHaToml(ctx, &haifypb.GetHaTomlRequest{Resource: resource})
	if err != nil {
		return nil, err
	}
	return resp, nil
}

// SyncHaToml writes an edited promoter TOML to all resource nodes and reloads
// drbd-reactor.
func (c *HaifyClient) SyncHaToml(ctx context.Context, resource, content string) (string, error) {
	resp, err := c.client.SyncHaToml(ctx, &haifypb.SyncHaTomlRequest{
		Resource: resource,
		Content:  content,
	})
	if err != nil {
		return "", err
	}
	if !resp.Success {
		return "", fmt.Errorf("%s", resp.Message)
	}
	return resp.Message, nil
}
