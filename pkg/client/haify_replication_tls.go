package client

import (
	"context"
	"fmt"

	haifypb "github.com/haify-project/haify/api/proto/v1"
)

// SetupReplicationTLS prepares nodes (all registered ones when empty) for
// encrypted DRBD replication and reports each node's readiness.
func (c *HaifyClient) SetupReplicationTLS(ctx context.Context, nodes []string) ([]*haifypb.NodeTLSInfo, error) {
	resp, err := c.client.SetupReplicationTLS(ctx, &haifypb.SetupReplicationTLSRequest{Nodes: nodes})
	if err != nil {
		return nil, err
	}
	if !resp.Success {
		return nil, fmt.Errorf("%s", resp.Message)
	}
	return resp.Nodes, nil
}

// ReplicationTLSStatus reports whether each node can carry an encrypted
// connection, and what is missing when it cannot.
func (c *HaifyClient) ReplicationTLSStatus(ctx context.Context, nodes []string) ([]*haifypb.NodeTLSInfo, error) {
	resp, err := c.client.GetReplicationTLSStatus(ctx, &haifypb.GetReplicationTLSStatusRequest{Nodes: nodes})
	if err != nil {
		return nil, err
	}
	if !resp.Success {
		return nil, fmt.Errorf("%s", resp.Message)
	}
	return resp.Nodes, nil
}

// SetResourceTLS switches a resource's connections to or from TLS.
func (c *HaifyClient) SetResourceTLS(ctx context.Context, resource string, enabled bool) (string, error) {
	resp, err := c.client.SetResourceTLS(ctx, &haifypb.SetResourceTLSRequest{Resource: resource, Enabled: enabled})
	if err != nil {
		return "", err
	}
	if !resp.Success {
		return "", fmt.Errorf("%s", resp.Message)
	}
	return resp.Message, nil
}
