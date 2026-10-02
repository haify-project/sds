package client

import (
	"context"
	"fmt"

	sdspb "github.com/haify-project/sds/api/proto/v1"
)

// MakeHa creates a drbd-reactor promoter config for HA failover. Optional
// ocfAgents are appended, in order, to the promoter start[] list; pass nil to
// keep the historical behavior.
func (c *SDSClient) MakeHa(ctx context.Context, resource string, services []string, mountPoint, fsType, vip string, ocfAgents []*sdspb.OcfAgent, startItems []*sdspb.HaStartItem) (string, error) {
	req := &sdspb.MakeHaRequest{
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
func (c *SDSClient) EnableSelfHa(ctx context.Context, vip, pool string, sizeGB, port uint32, nodes []string) (string, string, error) {
	resp, err := c.client.EnableSelfHa(ctx, &sdspb.EnableSelfHaRequest{
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
func (c *SDSClient) DisableSelfHa(ctx context.Context, node string) error {
	resp, err := c.client.DisableSelfHa(ctx, &sdspb.DisableSelfHaRequest{Node: node})
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
func (c *SDSClient) GetSelfHaStatus(ctx context.Context) (*SelfHaStatus, error) {
	resp, err := c.client.GetSelfHaStatus(ctx, &sdspb.GetSelfHaStatusRequest{})
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

func (c *SDSClient) EvictHa(ctx context.Context, resource string) error {
	req := &sdspb.EvictHaRequest{
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
func (c *SDSClient) DeleteHa(ctx context.Context, resource string) error {
	req := &sdspb.DeleteHaRequest{
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
func (c *SDSClient) GetHa(ctx context.Context, resource string) (*sdspb.HaConfigInfo, error) {
	req := &sdspb.GetHaRequest{
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
func (c *SDSClient) ListHa(ctx context.Context) ([]*sdspb.HaConfigInfo, error) {
	req := &sdspb.ListHaRequest{}

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
func (c *SDSClient) GetHaStatus(ctx context.Context, resource string) ([]*sdspb.HaPromoterStatus, error) {
	resp, err := c.client.GetHaStatus(ctx, &sdspb.GetHaStatusRequest{Resource: resource})
	if err != nil {
		return nil, err
	}
	if !resp.Success {
		return nil, fmt.Errorf("%s", resp.Message)
	}
	return resp.Promoters, nil
}

// ListResourceAgents lists the OCF resource agents available on the nodes.
func (c *SDSClient) ListResourceAgents(ctx context.Context) ([]*sdspb.ResourceAgentInfo, error) {
	resp, err := c.client.ListResourceAgents(ctx, &sdspb.ListResourceAgentsRequest{})
	if err != nil {
		return nil, err
	}
	return resp.Agents, nil
}

// GetResourceAgentMetadata returns an OCF agent's parsed meta-data schema.
func (c *SDSClient) GetResourceAgentMetadata(ctx context.Context, provider, name string) (*sdspb.GetResourceAgentMetadataResponse, error) {
	resp, err := c.client.GetResourceAgentMetadata(ctx, &sdspb.GetResourceAgentMetadataRequest{
		Provider: provider,
		Name:     name,
	})
	if err != nil {
		return nil, err
	}
	return resp, nil
}

// GetHaToml reads a resource's drbd-reactor promoter TOML.
func (c *SDSClient) GetHaToml(ctx context.Context, resource string) (*sdspb.GetHaTomlResponse, error) {
	resp, err := c.client.GetHaToml(ctx, &sdspb.GetHaTomlRequest{Resource: resource})
	if err != nil {
		return nil, err
	}
	return resp, nil
}

// SyncHaToml writes an edited promoter TOML to all resource nodes and reloads
// drbd-reactor.
func (c *SDSClient) SyncHaToml(ctx context.Context, resource, content string) (string, error) {
	resp, err := c.client.SyncHaToml(ctx, &sdspb.SyncHaTomlRequest{
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
