package client

import (
	"context"
	"fmt"

	haifypb "github.com/haify-project/haify/api/proto/v1"
)

// AttachDisklessClient adds a node to a resource as a diskless data client: it
// carries no local replica but connects over DRBD and can be promoted Primary
// to serve the volume over the network. Idempotent.
func (c *HaifyClient) AttachDisklessClient(ctx context.Context, resource, node string) error {
	resp, err := c.client.AttachDisklessClient(ctx, &haifypb.AttachDisklessClientRequest{
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
func (c *HaifyClient) DetachDisklessClient(ctx context.Context, resource, node string) error {
	resp, err := c.client.DetachDisklessClient(ctx, &haifypb.DetachDisklessClientRequest{
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
func (c *HaifyClient) SetTiebreaker(ctx context.Context, resource, node string) (string, string, error) {
	resp, err := c.client.SetTiebreaker(ctx, &haifypb.SetTiebreakerRequest{
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
func (c *HaifyClient) AddDR(ctx context.Context, resource, drNode, drEndpoint string, wanPort uint32, egressAddress string) (uint32, error) {
	resp, err := c.client.AddDR(ctx, &haifypb.AddDRRequest{
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
func (c *HaifyClient) AddReplica(ctx context.Context, resource, node string) error {
	resp, err := c.client.AddReplica(ctx, &haifypb.AddReplicaRequest{Resource: resource, Node: node})
	if err != nil {
		return err
	}
	if !resp.Success {
		return fmt.Errorf("%s", resp.Message)
	}
	return nil
}

// AddReplicaWith adds a replica with AddReplicaRequest's options.
func (c *HaifyClient) AddReplicaWith(ctx context.Context, req *haifypb.AddReplicaRequest) error {
	resp, err := c.client.AddReplica(ctx, req)
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
func (c *HaifyClient) AddReplicaIgnoringFreeSpace(ctx context.Context, resource, node string) error {
	resp, err := c.client.AddReplica(ctx, &haifypb.AddReplicaRequest{Resource: resource, Node: node, IgnoreFreeSpace: true})
	if err != nil {
		return err
	}
	if !resp.Success {
		return fmt.Errorf("%s", resp.Message)
	}
	return nil
}

// RemoveReplica takes a diskful replica out of a running resource.
func (c *HaifyClient) RemoveReplica(ctx context.Context, resource, node string) error {
	_, err := c.RemoveReplicaOptions(ctx, resource, node, false)
	return err
}

// RemoveReplicaOptions is RemoveReplica; lost removes the replica of a node
// that is gone for good, without reaching it. It returns the controller's
// message, which for a lost node says what to clean up on it.
func (c *HaifyClient) RemoveReplicaOptions(ctx context.Context, resource, node string, lost bool) (string, error) {
	resp, err := c.client.RemoveReplica(ctx, &haifypb.RemoveReplicaRequest{Resource: resource, Node: node, Lost: lost})
	if err != nil {
		return "", err
	}
	if !resp.Success {
		return "", fmt.Errorf("%s", resp.Message)
	}
	return resp.Message, nil
}

// RepairWanProxy reconciles a WAN resource's replication tunnels with the
// controller's current node list.
func (c *HaifyClient) RepairWanProxy(ctx context.Context, name string, dryRun bool) (*haifypb.RepairWanProxyResponse, error) {
	resp, err := c.client.RepairWanProxy(ctx, &haifypb.RepairWanProxyRequest{Name: name, DryRun: dryRun})
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
func (c *HaifyClient) SetWanEndpoint(ctx context.Context, req *haifypb.SetWanEndpointRequest) (*haifypb.SetWanEndpointResponse, error) {
	resp, err := c.client.SetWanEndpoint(ctx, req)
	if err != nil {
		return nil, err
	}
	if !resp.Success {
		return nil, fmt.Errorf("%s", resp.Message)
	}
	return resp, nil
}

func (c *HaifyClient) DRFailback(ctx context.Context, name, node string, waitSeconds uint32) (*haifypb.DRFailbackResponse, error) {
	return c.client.DRFailback(ctx, &haifypb.DRFailbackRequest{Name: name, Node: node, WaitSeconds: waitSeconds})
}
