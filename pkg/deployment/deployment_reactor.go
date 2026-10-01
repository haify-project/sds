package deployment

import (
	"context"
	"encoding/json"
	"fmt"
)

// ============ Reactor Operations ============

// ReactorWriteConfig writes reactor plugin config
func (c *Client) ReactorWriteConfig(ctx context.Context, hosts []string, pluginID, content string) (*ConfigResult, error) {
	remotePath := fmt.Sprintf("/etc/drbd-reactor.d/%s.toml", pluginID)
	return c.DistributeConfig(ctx, hosts, content, remotePath,
		WithPostCommand("sudo systemctl reload drbd-reactor || sudo systemctl restart drbd-reactor"))
}

// ReactorEnablePlugin enables a promoter plugin
func (c *Client) ReactorEnablePlugin(ctx context.Context, hosts []string, pluginID string) (*ExecResult, error) {
	return c.Exec(ctx, hosts, fmt.Sprintf("sudo drbd-reactorctl prom enable %s", pluginID))
}

// ReactorDisablePlugin disables a promoter plugin
func (c *Client) ReactorDisablePlugin(ctx context.Context, hosts []string, pluginID string) (*ExecResult, error) {
	return c.Exec(ctx, hosts, fmt.Sprintf("sudo drbd-reactorctl prom disable %s", pluginID))
}

// ReactorEvict evicts a promoter from the current node
func (c *Client) ReactorEvict(ctx context.Context, config string) (*ExecResult, error) {
	return c.Exec(ctx, []string{"localhost"}, fmt.Sprintf("sudo drbd-reactorctl evict %s", config))
}

// ReactorReload reloads drbd-reactor
func (c *Client) ReactorReload(ctx context.Context, hosts []string) (*ExecResult, error) {
	return c.Exec(ctx, hosts, "sudo systemctl reload drbd-reactor || sudo systemctl restart drbd-reactor")
}

// ============ Reactor Status ============

// ReactorStatus represents the status output from drbd-reactorctl status --json
type ReactorStatus struct {
	Promoter   []ReactorPromoterStatus `json:"promoter"`
	Prometheus []ReactorPluginStatus   `json:"prometheus"`
	Debugger   []ReactorPluginStatus   `json:"debugger"`
	UMH        []ReactorPluginStatus   `json:"umh"`
	AgentX     []ReactorPluginStatus   `json:"agentx"`
}

// ReactorPromoterStatus represents status of a promoter plugin
type ReactorPromoterStatus struct {
	DRBDResource string                 `json:"drbd_resource"`
	Path         string                 `json:"path"`
	PrimaryOn    string                 `json:"primary_on"`
	Target       ReactorServiceStatus   `json:"target"`
	Dependencies []ReactorServiceStatus `json:"dependencies"`
	Status       string                 `json:"status"`
}

// ReactorServiceStatus represents status of a systemd service
type ReactorServiceStatus struct {
	Name    string `json:"name"`
	Status  string `json:"status"`
	Freezer string `json:"freezer"`
}

// ReactorPluginStatus represents status of a generic reactor plugin
type ReactorPluginStatus struct {
	Path    string `json:"path"`
	Address string `json:"address,omitempty"`
	Status  string `json:"status"`
}

// ReactorStatusJSON gets the reactor status using JSON output
func (c *Client) ReactorStatusJSON(ctx context.Context, host string) (*ReactorStatus, error) {
	result, err := c.Exec(ctx, []string{host}, "sudo drbd-reactorctl status --json")
	if err != nil {
		return nil, fmt.Errorf("failed to get reactor status: %w", err)
	}

	for _, r := range result.Hosts {
		if !r.Success {
			return nil, fmt.Errorf("reactor status command failed: %s", r.Output)
		}

		var status ReactorStatus
		if err := jsonUnmarshal([]byte(r.Output), &status); err != nil {
			return nil, fmt.Errorf("failed to parse reactor status JSON: %w", err)
		}
		return &status, nil
	}

	return nil, fmt.Errorf("no result from reactor status command")
}

// ReactorPromoterStatusByResource gets the promoter status for a specific DRBD resource
func (c *Client) ReactorPromoterStatusByResource(ctx context.Context, host, resource string) (*ReactorPromoterStatus, error) {
	status, err := c.ReactorStatusJSON(ctx, host)
	if err != nil {
		return nil, err
	}

	for _, promoter := range status.Promoter {
		if promoter.DRBDResource == resource {
			return &promoter, nil
		}
	}

	return nil, fmt.Errorf("promoter status for resource %s not found", resource)
}

// jsonUnmarshal is a local wrapper for json.Unmarshal
func jsonUnmarshal(data []byte, v interface{}) error {
	return json.Unmarshal(data, v)
}
