package controller

import (
	"context"
	"fmt"
	"strings"

	"go.uber.org/zap"

	"github.com/haify-project/sds/pkg/deployment"
	"github.com/haify-project/sds/pkg/gateway"
	"github.com/haify-project/sds/pkg/wanproxy"
)

// GatewayResourceManager adapts ResourceManager to gateway.ResourceManager interface
type GatewayResourceManager struct {
	rm                *ResourceManager
	autoStateVolume   bool
	stateVolumeSizeGB uint32
}

// NewGatewayResourceManager creates a new gateway resource manager adapter.
// autoStateVolume/stateVolumeSizeGB control whether a missing cluster-private
// state volume is provisioned automatically during gateway creation.
func NewGatewayResourceManager(rm *ResourceManager, autoStateVolume bool, stateVolumeSizeGB uint32) gateway.ResourceManager {
	if stateVolumeSizeGB == 0 {
		stateVolumeSizeGB = 1
	}
	return &GatewayResourceManager{
		rm:                rm,
		autoStateVolume:   autoStateVolume,
		stateVolumeSizeGB: stateVolumeSizeGB,
	}
}

// EnsureGatewayVolumes provisions the small cluster-private state volume(s) a
// gateway needs, so a single-volume resource can be exported directly. It is a
// no-op when the resource already has enough volumes or when auto-provisioning
// is disabled (the gateway's own check then surfaces a clear error).
func (a *GatewayResourceManager) EnsureGatewayVolumes(ctx context.Context, resource string, minVolumes int) error {
	if !a.autoStateVolume {
		return nil
	}
	info, err := a.rm.GetResource(ctx, resource)
	if err != nil {
		return err
	}
	if len(info.Volumes) >= minVolumes {
		return nil
	}
	if len(info.Volumes) == 0 {
		return fmt.Errorf("resource %q has no volumes to derive a pool from", resource)
	}
	pool := info.Volumes[0].Pool
	if pool == "" {
		return fmt.Errorf("cannot determine storage pool for resource %q", resource)
	}
	for n := len(info.Volumes); n < minVolumes; n++ {
		volName := fmt.Sprintf("%s_state%d", resource, n)
		a.rm.controller.logger.Info("Auto-provisioning gateway state volume",
			zap.String("resource", resource),
			zap.String("volume", volName),
			zap.String("pool", pool),
			zap.Uint32("size_gb", a.stateVolumeSizeGB))
		if err := a.rm.AddVolume(ctx, resource, volName, pool, a.stateVolumeSizeGB); err != nil {
			return fmt.Errorf("auto-provision state volume %q: %w", volName, err)
		}
	}
	return nil
}

func (a *GatewayResourceManager) GetResource(ctx context.Context, name string) (*gateway.ResourceInfo, error) {
	info, err := a.rm.GetResource(ctx, name)
	if err != nil {
		return nil, err
	}

	// Convert controller.ResourceInfo to gateway.ResourceInfo
	gwVolumes := make([]*gateway.ResourceVolumeInfo, len(info.Volumes))
	for i, v := range info.Volumes {
		gwVolumes[i] = &gateway.ResourceVolumeInfo{
			VolumeID: v.VolumeID,
			Device:   v.Device,
			SizeGB:   v.SizeGB,
			// The gateway tells its own auto-provisioned "<res>_state<N>"
			// volume from the operator's "<res>_data" by this name. Without
			// it, it can only go by position — which is what used to make it
			// export the wrong one.
			BackingVolume: v.BackingVolume,
		}
	}

	gwNodeStates := make(map[string]*gateway.ResourceNodeState)
	for k, v := range info.NodeStates {
		gwNodeStates[k] = &gateway.ResourceNodeState{
			Role:        v.Role,
			DiskState:   v.DiskState,
			Replication: v.Replication,
		}
	}

	// Diskful replicas only: resourceHosts reads the resource's own node list,
	// which never includes its tiebreakers or diskless clients.
	hosts, _ := a.rm.resourceHosts(ctx, name)

	return &gateway.ResourceInfo{
		Name:       info.Name,
		Port:       info.Port,
		Protocol:   info.Protocol,
		Nodes:      info.Nodes,
		Hosts:      hosts,
		Role:       info.Role,
		Volumes:    gwVolumes,
		NodeStates: gwNodeStates,
	}, nil
}

func (a *GatewayResourceManager) SetPrimary(ctx context.Context, resource, node string, force bool) error {
	return a.rm.SetPrimary(ctx, resource, node, force)
}

// GatewayDeploymentClient adapts deployment.Client to gateway.DeploymentClient interface
type GatewayDeploymentClient struct {
	dc *deployment.Client
}

// NewGatewayDeploymentClient creates a new gateway deployment client adapter
func NewGatewayDeploymentClient(dc *deployment.Client) gateway.DeploymentClient {
	return &GatewayDeploymentClient{dc: dc}
}

func (a *GatewayDeploymentClient) DistributeConfig(ctx context.Context, hosts []string, content, remotePath string) error {
	_, err := a.dc.DistributeConfig(ctx, hosts, content, remotePath)
	return err
}

func (a *GatewayDeploymentClient) Exec(ctx context.Context, hosts []string, cmd string) error {
	result, err := a.dc.Exec(ctx, hosts, cmd)
	if err != nil {
		return err
	}
	// Per-host command failures must surface: swallowing them let gateway
	// setup steps (e.g. formatting the cluster-private volume) fail
	// silently while the gateway reported success.
	for host, hr := range result.Hosts {
		if !hr.Success {
			return fmt.Errorf("command failed on %s: %s", host, strings.TrimSpace(hr.Output))
		}
	}
	return nil
}

// ExecOutput implements gateway.HostOutputReader: it returns what cmd printed
// on each host where it succeeded, so the gateway manager can read state that
// exists only on the nodes.
func (a *GatewayDeploymentClient) ExecOutput(ctx context.Context, hosts []string, cmd string) (map[string]string, error) {
	result, err := a.dc.Exec(ctx, hosts, cmd)
	if err != nil {
		return nil, err
	}
	out := make(map[string]string, len(result.Hosts))
	for host, hr := range result.Hosts {
		if hr != nil && hr.Success {
			out[host] = hr.Output
		}
	}
	return out, nil
}

// WanproxyDeploymentClient adapts the controller's deploymentClient to the
// wanproxy.DeploymentClient interface, converting deployment result types into
// wanproxy.Result. It mirrors GatewayDeploymentClient (the gateway adapter) and
// wraps the same interface, so the WAN provisioner runs over the exact SSH
// transport the rest of the controller uses (and is trivially fakeable in tests).
type WanproxyDeploymentClient struct {
	dc deploymentClient
}

// NewWanproxyDeploymentClient creates a wanproxy deployment client adapter over
// the controller's deployment client.
func NewWanproxyDeploymentClient(dc deploymentClient) wanproxy.DeploymentClient {
	return &WanproxyDeploymentClient{dc: dc}
}

func (a *WanproxyDeploymentClient) DistributeConfig(ctx context.Context, hosts []string, content, remotePath string) (*wanproxy.Result, error) {
	res, err := a.dc.DistributeConfig(ctx, hosts, content, remotePath)
	if err != nil {
		return nil, err
	}
	return configResultToWanproxy(res), nil
}

func (a *WanproxyDeploymentClient) Exec(ctx context.Context, hosts []string, cmd string) (*wanproxy.Result, error) {
	res, err := a.dc.Exec(ctx, hosts, cmd)
	if err != nil {
		return nil, err
	}
	return execResultToWanproxy(res), nil
}

// execResultToWanproxy converts a deployment.ExecResult into a wanproxy.Result.
func execResultToWanproxy(res *deployment.ExecResult) *wanproxy.Result {
	out := &wanproxy.Result{Hosts: make(map[string]*wanproxy.HostResult)}
	if res == nil {
		return out
	}
	for host, hr := range res.Hosts {
		out.Hosts[host] = &wanproxy.HostResult{Host: hr.Host, Output: hr.Output, Success: hr.Success, Err: hr.Error}
	}
	return out
}

// configResultToWanproxy converts a deployment.ConfigResult into a
// wanproxy.Result. ConfigResult carries a top-level Success flag that may be set
// with an empty per-host map (a fully-successful distribute), so when the map is
// empty we reflect the aggregate flag to keep Result.AllSuccess() accurate.
func configResultToWanproxy(res *deployment.ConfigResult) *wanproxy.Result {
	out := &wanproxy.Result{Hosts: make(map[string]*wanproxy.HostResult)}
	if res == nil {
		return out
	}
	for host, hr := range res.Hosts {
		out.Hosts[host] = &wanproxy.HostResult{Host: hr.Host, Output: hr.Output, Success: hr.Success, Err: hr.Error}
	}
	if len(out.Hosts) == 0 {
		out.Hosts["_"] = &wanproxy.HostResult{Host: "_", Success: res.Success}
	}
	return out
}
