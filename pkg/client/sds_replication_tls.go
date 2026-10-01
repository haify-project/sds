package client

import (
	"context"
	"fmt"

	sdspb "github.com/liliang-cn/sds/api/proto/v1"
)

// SetupReplicationTLS prepares nodes (all registered ones when empty) for
// encrypted DRBD replication and reports each node's readiness.
func (c *SDSClient) SetupReplicationTLS(ctx context.Context, nodes []string) ([]*sdspb.NodeTLSInfo, error) {
	resp, err := c.client.SetupReplicationTLS(ctx, &sdspb.SetupReplicationTLSRequest{Nodes: nodes})
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
func (c *SDSClient) ReplicationTLSStatus(ctx context.Context, nodes []string) ([]*sdspb.NodeTLSInfo, error) {
	resp, err := c.client.GetReplicationTLSStatus(ctx, &sdspb.GetReplicationTLSStatusRequest{Nodes: nodes})
	if err != nil {
		return nil, err
	}
	if !resp.Success {
		return nil, fmt.Errorf("%s", resp.Message)
	}
	return resp.Nodes, nil
}

// SetResourceTLS switches a resource's connections to or from TLS.
func (c *SDSClient) SetResourceTLS(ctx context.Context, resource string, enabled bool) (string, error) {
	resp, err := c.client.SetResourceTLS(ctx, &sdspb.SetResourceTLSRequest{Resource: resource, Enabled: enabled})
	if err != nil {
		return "", err
	}
	if !resp.Success {
		return "", fmt.Errorf("%s", resp.Message)
	}
	return resp.Message, nil
}
