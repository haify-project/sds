package controller

import (
	"context"
	"fmt"

	"go.uber.org/zap"

	pb "github.com/liliang-cn/drbd-agent/api/proto/v1"
	"github.com/liliang-cn/sds/pkg/client"
)

// DrbdManager manages DRBD resources
type DrbdManager struct {
	controller *Controller
}

// NewDrbdManager creates a new DRBD manager
func NewDrbdManager(ctrl *Controller) *DrbdManager {
	return &DrbdManager{controller: ctrl}
}

// ListResources lists DRBD resources on all agents
func (dm *DrbdManager) ListResources(ctx context.Context) ([]*DrbdResourceInfo, error) {
	var resources []*DrbdResourceInfo

	dm.controller.agentsLock.RLock()
	agents := make(map[string]*client.AgentClient)
	for k, v := range dm.controller.agents {
		agents[k] = v
	}
	dm.controller.agentsLock.RUnlock()

	for endpoint, agent := range agents {
		req := &pb.StatusRequest{
			Resources: []string{},
		}
		resp, err := agent.Status(ctx, req)
		if err != nil {
			dm.controller.logger.Warn("Failed to get DRBD status",
				zap.String("endpoint", endpoint),
				zap.Error(err))
			continue
		}

		for _, res := range resp.Resources {
			info := &DrbdResourceInfo{
				Name:     res.Name,
				Role:     res.Role,
				Endpoint: endpoint,
			}

			for _, vol := range res.Volumes {
				info.Volumes = append(info.Volumes, &DrbdVolumeInfo{
					Number:    uint32(vol.VolumeNumber),
					Device:    vol.Device,
					DiskState: vol.DiskState,
				})
			}

			for _, peer := range res.Peers {
				info.Peers = append(info.Peers, &DrbdPeerInfo{
					NodeID:           uint32(peer.PeerNodeId),
					ConnectionName:   peer.ConnectionName,
					ReplicationState: peer.ReplicationState,
					Role:             peer.Role,
				})
			}

			resources = append(resources, info)
		}
	}

	return resources, nil
}

// Up brings DRBD resources up
func (dm *DrbdManager) Up(ctx context.Context, endpoint string, resources []string, force bool) error {
	dm.controller.agentsLock.RLock()
	agent := dm.controller.agents[endpoint]
	dm.controller.agentsLock.RUnlock()

	if agent == nil {
		return fmt.Errorf("agent not found: %s", endpoint)
	}

	req := &pb.UpRequest{
		Resources: resources,
		Force:     force,
	}

	resp, err := agent.Up(ctx, req)
	if err != nil {
		return fmt.Errorf("failed to bring resources up: %w", err)
	}

	if !resp.Success {
		return fmt.Errorf("failed to bring resources up: %s", resp.Message)
	}

	dm.controller.logger.Info("DRBD resources brought up",
		zap.Strings("resources", resources),
		zap.String("endpoint", endpoint))

	return nil
}

// Down takes DRBD resources down
func (dm *DrbdManager) Down(ctx context.Context, endpoint string, resources []string, force bool) error {
	dm.controller.agentsLock.RLock()
	agent := dm.controller.agents[endpoint]
	dm.controller.agentsLock.RUnlock()

	if agent == nil {
		return fmt.Errorf("agent not found: %s", endpoint)
	}

	req := &pb.DownRequest{
		Resources: resources,
		Force:     force,
	}

	resp, err := agent.Down(ctx, req)
	if err != nil {
		return fmt.Errorf("failed to take resources down: %w", err)
	}

	if !resp.Success {
		return fmt.Errorf("failed to take resources down: %s", resp.Message)
	}

	dm.controller.logger.Info("DRBD resources taken down",
		zap.Strings("resources", resources),
		zap.String("endpoint", endpoint))

	return nil
}

// Primary promotes resources to Primary
func (dm *DrbdManager) Primary(ctx context.Context, endpoint string, resources []string, force bool) error {
	dm.controller.agentsLock.RLock()
	agent := dm.controller.agents[endpoint]
	dm.controller.agentsLock.RUnlock()

	if agent == nil {
		return fmt.Errorf("agent not found: %s", endpoint)
	}

	req := &pb.PrimaryRequest{
		Resources: resources,
		Force:     force,
	}

	resp, err := agent.Primary(ctx, req)
	if err != nil {
		return fmt.Errorf("failed to promote resources: %w", err)
	}

	if !resp.Success {
		return fmt.Errorf("failed to promote resources: %s", resp.Message)
	}

	dm.controller.logger.Info("DRBD resources promoted to Primary",
		zap.Strings("resources", resources),
		zap.String("endpoint", endpoint))

	return nil
}

// Secondary demotes resources to Secondary
func (dm *DrbdManager) Secondary(ctx context.Context, endpoint string, resources []string) error {
	dm.controller.agentsLock.RLock()
	agent := dm.controller.agents[endpoint]
	dm.controller.agentsLock.RUnlock()

	if agent == nil {
		return fmt.Errorf("agent not found: %s", endpoint)
	}

	req := &pb.SecondaryRequest{
		Resources: resources,
	}

	resp, err := agent.Secondary(ctx, req)
	if err != nil {
		return fmt.Errorf("failed to demote resources: %w", err)
	}

	if !resp.Success {
		return fmt.Errorf("failed to demote resources: %s", resp.Message)
	}

	dm.controller.logger.Info("DRBD resources demoted to Secondary",
		zap.Strings("resources", resources),
		zap.String("endpoint", endpoint))

	return nil
}

// DrbdResourceInfo represents DRBD resource information
type DrbdResourceInfo struct {
	Name     string
	Role     string
	Endpoint string
	Volumes  []*DrbdVolumeInfo
	Peers    []*DrbdPeerInfo
}

// DrbdVolumeInfo represents DRBD volume information
type DrbdVolumeInfo struct {
	Number    uint32
	Device    string
	DiskState string
}

// DrbdPeerInfo represents DRBD peer information
type DrbdPeerInfo struct {
	NodeID           uint32
	ConnectionName   string
	ReplicationState string
	Role             string
}
