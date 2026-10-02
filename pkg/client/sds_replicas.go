package client

import (
	"context"
	"fmt"

	sdspb "github.com/haify-project/sds/api/proto/v1"
)

// AttachDisklessClient adds a node to a resource as a diskless data client: it
// carries no local replica but connects over DRBD and can be promoted Primary
// to serve the volume over the network. Idempotent.
func (c *SDSClient) AttachDisklessClient(ctx context.Context, resource, node string) error {
	resp, err := c.client.AttachDisklessClient(ctx, &sdspb.AttachDisklessClientRequest{
		Resource: resource,
		Node:     node,
	})
	if err != nil {
		return err
	}
	if !resp.Success {
		return fmt.Errorf("%s", resp.Message)
	}
	return nil
}

// DetachDisklessClient removes a diskless client added via AttachDisklessClient.
// Idempotent.
func (c *SDSClient) DetachDisklessClient(ctx context.Context, resource, node string) error {
	resp, err := c.client.DetachDisklessClient(ctx, &sdspb.DetachDisklessClientRequest{
		Resource: resource,
		Node:     node,
	})
	if err != nil {
		return err
	}
	if !resp.Success {
		return fmt.Errorf("%s", resp.Message)
	}
	return nil
}

// EvictHa evicts an HA resource from the active node
// SetTiebreaker moves a resource's diskless quorum tiebreaker to node (empty
// removes it). It returns the node that previously held it and the controller's
// description of the result — which for a removal says whether the remaining
// replicas still make a quorum majority, something only the controller knows.
func (c *SDSClient) SetTiebreaker(ctx context.Context, resource, node string) (string, string, error) {
	resp, err := c.client.SetTiebreaker(ctx, &sdspb.SetTiebreakerRequest{
		Resource: resource,
		Node:     node,
	})
	if err != nil {
		return "", "", err
	}
	if !resp.Success {
		return "", "", fmt.Errorf("%s", resp.Message)
	}
	return resp.PreviousNode, resp.Message, nil
}

// AddDR attaches an off-site asynchronous replica to a running resource,
// returning the base WAN port in use (the controller allocates one when the
// caller passes 0).
func (c *SDSClient) AddDR(ctx context.Context, resource, drNode, drEndpoint string, wanPort uint32, egressAddress string) (uint32, error) {
	resp, err := c.client.AddDR(ctx, &sdspb.AddDRRequest{
		Resource:      resource,
		DrNode:        drNode,
		DrEndpoint:    drEndpoint,
		WanPort:       wanPort,
		EgressAddress: egressAddress,
	})
	if err != nil {
		return 0, err
	}
	if !resp.Success {
		return 0, fmt.Errorf("%s", resp.Message)
	}
	return resp.WanPort, nil
}

// AddReplica adds a diskful local replica to a running resource.
func (c *SDSClient) AddReplica(ctx context.Context, resource, node string) error {
	resp, err := c.client.AddReplica(ctx, &sdspb.AddReplicaRequest{Resource: resource, Node: node})
	if err != nil {
		return err
	}
	if !resp.Success {
		return fmt.Errorf("%s", resp.Message)
	}
	return nil
}

// AddReplicaIgnoringFreeSpace adds a replica although the node's pool has less
// free space than the volume; see AddReplicaRequest.ignore_free_space.
func (c *SDSClient) AddReplicaIgnoringFreeSpace(ctx context.Context, resource, node string) error {
	resp, err := c.client.AddReplica(ctx, &sdspb.AddReplicaRequest{Resource: resource, Node: node, IgnoreFreeSpace: true})
	if err != nil {
		return err
	}
	if !resp.Success {
		return fmt.Errorf("%s", resp.Message)
	}
	return nil
}

// RemoveReplica takes a diskful replica out of a running resource.
func (c *SDSClient) RemoveReplica(ctx context.Context, resource, node string) error {
	resp, err := c.client.RemoveReplica(ctx, &sdspb.RemoveReplicaRequest{Resource: resource, Node: node})
	if err != nil {
		return err
	}
	if !resp.Success {
		return fmt.Errorf("%s", resp.Message)
	}
	return nil
}

// RepairWanProxy reconciles a WAN resource's replication tunnels with the
// controller's current node list.
func (c *SDSClient) RepairWanProxy(ctx context.Context, name string, dryRun bool) (*sdspb.RepairWanProxyResponse, error) {
	resp, err := c.client.RepairWanProxy(ctx, &sdspb.RepairWanProxyRequest{Name: name, DryRun: dryRun})
	if err != nil {
		return nil, err
	}
	if !resp.Success {
		return nil, fmt.Errorf("%s", resp.Message)
	}
	return resp, nil
}

// SetWanEndpoint changes where a WAN resource's primary site reaches its DR
// site; see SetWanEndpointRequest for the fields.
func (c *SDSClient) SetWanEndpoint(ctx context.Context, req *sdspb.SetWanEndpointRequest) (*sdspb.SetWanEndpointResponse, error) {
	resp, err := c.client.SetWanEndpoint(ctx, req)
	if err != nil {
		return nil, err
	}
	if !resp.Success {
		return nil, fmt.Errorf("%s", resp.Message)
	}
	return resp, nil
}

func (c *SDSClient) DRFailback(ctx context.Context, name, node string, waitSeconds uint32) (*sdspb.DRFailbackResponse, error) {
	return c.client.DRFailback(ctx, &sdspb.DRFailbackRequest{Name: name, Node: node, WaitSeconds: waitSeconds})
}
