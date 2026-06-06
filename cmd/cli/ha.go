package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/liliang-cn/sds/pkg/client"
	"github.com/spf13/cobra"
)

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

func haCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "ha",
		Short: "Make a DRBD resource highly available with drbd-reactor",
	}

	cmd.AddCommand(haCreate())
	cmd.AddCommand(haEvict())
	cmd.AddCommand(haDelete())
	cmd.AddCommand(haList())
	cmd.AddCommand(haStatus())
	cmd.AddCommand(haSelfCommand())

	return cmd
}

func haCreate() *cobra.Command {
	var services string
	var mountPoint string
	var fsType string
	var vip string

	cmd := &cobra.Command{
		Use:   "create <resource>",
		Short: "Create HA configuration for a resource",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			resource := args[0]

			ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
			defer cancel()

			sdsClient, err := client.NewSDSClient(controllerAddr)
			if err != nil {
				return fmt.Errorf("failed to connect to controller: %w", err)
			}
			defer sdsClient.Close()

			// Parse services
			var serviceList []string
			if services != "" {
				serviceList = strings.Split(services, ",")
			}

			configPath, err := sdsClient.MakeHa(ctx, resource, serviceList, mountPoint, fsType, vip)
			if err != nil {
				return fmt.Errorf("failed to create HA config: %w", err)
			}

			fmt.Printf("HA configuration created successfully\n")
			fmt.Printf("  Resource:  %s\n", resource)
			fmt.Printf("  Config:    %s\n", configPath)
			if len(serviceList) > 0 {
				fmt.Printf("  Services:  %v\n", serviceList)
			}
			if mountPoint != "" {
				fmt.Printf("  Mount:     %s (%s)\n", mountPoint, fsType)
			}
			if vip != "" {
				fmt.Printf("  VIP:       %s\n", vip)
			}
			fmt.Printf("\nConfiguration distributed to all nodes and drbd-reactor reloaded\n")

			return nil
		},
	}

	cmd.Flags().StringVar(&services, "services", "", "Systemd services to start/stop (comma-separated)")
	cmd.Flags().StringVar(&mountPoint, "mount", "", "Mount point for filesystem")
	cmd.Flags().StringVar(&fsType, "fstype", "ext4", "Filesystem type (ext4, xfs, etc.)")
	cmd.Flags().StringVar(&vip, "vip", "", "Virtual IP (CIDR, e.g., 192.168.1.100/24)")

	return cmd
}

func haDelete() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "delete <resource>",
		Short: "Delete HA configuration for a resource",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			resource := args[0]

			ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
			defer cancel()

			sdsClient, err := client.NewSDSClient(controllerAddr)
			if err != nil {
				return fmt.Errorf("failed to connect to controller: %w", err)
			}
			defer sdsClient.Close()

			err = sdsClient.DeleteHa(ctx, resource)
			if err != nil {
				return fmt.Errorf("failed to delete HA config: %w", err)
			}

			fmt.Printf("HA configuration deleted successfully\n")
			fmt.Printf("  Resource: %s\n", resource)
			fmt.Printf("\nNote: Configuration files have been removed from all nodes\n")
			fmt.Printf("      You may need to reload drbd-reactor: sudo systemctl reload drbd-reactor\n")

			return nil
		},
	}

	return cmd
}

func haEvict() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "evict <resource>",
		Short: "Evict an HA resource from the current active node",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			resource := args[0]

			ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
			defer cancel()

			sdsClient, err := client.NewSDSClient(controllerAddr)
			if err != nil {
				return fmt.Errorf("failed to connect to controller: %w", err)
			}
			defer sdsClient.Close()

			if err := sdsClient.EvictHa(ctx, resource); err != nil {
				return fmt.Errorf("failed to evict HA resource: %w", err)
			}

			fmt.Printf("HA resource evicted successfully\n")
			fmt.Printf("  Resource: %s\n", resource)

			return nil
		},
	}

	return cmd
}

func haList() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List all HA configurations",
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()

			sdsClient, err := client.NewSDSClient(controllerAddr)
			if err != nil {
				return fmt.Errorf("failed to connect to controller: %w", err)
			}
			defer sdsClient.Close()

			configs, err := sdsClient.ListHa(ctx)
			if err != nil {
				return fmt.Errorf("failed to list HA configs: %w", err)
			}

			if len(configs) == 0 {
				fmt.Println("No HA configurations found")
				return nil
			}

			fmt.Printf("HA Configurations (%d):\n", len(configs))
			reactorStatus, reactorErr := getReactorStatus()
			promoterMap := make(map[string]*ReactorPromoterStatus)
			if reactorErr == nil && reactorStatus != nil {
				for i := range reactorStatus.Promoter {
					p := &reactorStatus.Promoter[i]
					promoterMap[p.DRBDResource] = p
				}
			}

			for _, cfg := range configs {
				fmt.Printf("  - %s\n", cfg.GetResource())
				if promoter, ok := promoterMap[cfg.GetResource()]; ok {
					fmt.Printf("      Status:    %s %s\n", statusIcon(promoter.Status), promoter.Status)
					if promoter.PrimaryOn != "" {
						fmt.Printf("      Primary:   %s\n", promoter.PrimaryOn)
					}
					if promoter.Target.Name != "" {
						fmt.Printf("      Target:    %s (%s)\n", promoter.Target.Name, promoter.Target.Status)
					}
				}
				if status, err := sdsClient.ResourceStatus(ctx, cfg.GetResource()); err == nil {
					activeNode := ""
					for node, nodeState := range status.GetNodeStates() {
						if nodeState.GetRole() == "Primary" {
							activeNode = node
							break
						}
					}
					if activeNode != "" {
						fmt.Printf("      Active:    %s\n", activeNode)
					} else if status.GetRole() != "" {
						fmt.Printf("      Role:      %s\n", status.GetRole())
					}
				}
				if cfg.GetMountPoint() != "" {
					fmt.Printf("      Mount:     %s (%s)\n", cfg.GetMountPoint(), cfg.GetFsType())
				}
				if len(cfg.GetServices()) > 0 {
					fmt.Printf("      Services:  %v\n", cfg.GetServices())
				}
				if cfg.GetVip() != "" {
					fmt.Printf("      VIP:       %s\n", cfg.GetVip())
				}
				fmt.Println()
			}

			if reactorErr != nil {
				fmt.Printf("Warning: failed to read local drbd-reactor status JSON: %v\n", reactorErr)
			}

			return nil
		},
	}

	return cmd
}

func haStatus() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "status <resource>",
		Short: "Show HA configuration status for a resource",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			resource := args[0]
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()

			sdsClient, err := client.NewSDSClient(controllerAddr)
			if err != nil {
				return fmt.Errorf("failed to connect to controller: %w", err)
			}
			defer sdsClient.Close()

			cfg, err := sdsClient.GetHa(ctx, resource)
			if err != nil {
				return fmt.Errorf("failed to get HA config: %w", err)
			}

			resourceStatus, statusErr := sdsClient.ResourceStatus(ctx, resource)
			promoter, promoterErr := getReactorPromoterStatus(resource)

			fmt.Printf("HA Configuration: %s\n", resource)
			fmt.Printf("  Config:  controller database\n")
			if statusErr == nil && resourceStatus != nil {
				activeNode := ""
				for node, nodeState := range resourceStatus.GetNodeStates() {
					if nodeState.GetRole() == "Primary" {
						activeNode = node
						break
					}
				}
				if activeNode != "" {
					fmt.Printf("  Active:  %s\n", activeNode)
				}
				fmt.Printf("  Role:    %s\n", resourceStatus.GetRole())
				fmt.Printf("  Nodes:   %v\n", resourceStatus.GetNodes())
			}
			if promoterErr == nil && promoter != nil {
				fmt.Printf("  Status:  %s %s\n", statusIcon(promoter.Status), promoter.Status)
				if promoter.PrimaryOn != "" {
					fmt.Printf("  Primary: %s\n", promoter.PrimaryOn)
				}
			}

			if cfg.GetMountPoint() != "" {
				fmt.Printf("  Mount:   %s (%s)\n", cfg.GetMountPoint(), cfg.GetFsType())
			}
			if cfg.GetVip() != "" {
				fmt.Printf("  VIP:     %s\n", cfg.GetVip())
			}
			if len(cfg.GetServices()) > 0 {
				fmt.Printf("  Services: %v\n", cfg.GetServices())
			}
			if statusErr == nil && resourceStatus != nil && len(resourceStatus.GetNodeStates()) > 0 {
				fmt.Printf("  Node States:\n")
				for node, nodeState := range resourceStatus.GetNodeStates() {
					fmt.Printf("    - %s: role=%s disk=%s repl=%s\n",
						node,
						nodeState.GetRole(),
						nodeState.GetDiskState(),
						nodeState.GetReplicationState())
				}
			}
			if promoterErr == nil && promoter != nil {
				fmt.Printf("  Promoter Target:\n")
				fmt.Printf("    - %s %s (%s)\n",
					statusIcon(promoter.Target.Status),
					promoter.Target.Name,
					promoter.Target.Status)
				if len(promoter.Dependencies) > 0 {
					fmt.Printf("  Dependencies:\n")
					for _, dep := range promoter.Dependencies {
						fmt.Printf("    - %s %s (%s)\n",
							statusIcon(dep.Status),
							dep.Name,
							dep.Status)
					}
				}
			} else if promoterErr != nil {
				fmt.Printf("  Reactor: local promoter status unavailable (%v)\n", promoterErr)
			}

			return nil
		},
	}

	return cmd
}

// HAConfig represents a parsed HA configuration
type HAConfig struct {
	Resource   string
	MountPoint string
	FSType     string
	Services   []string
	VIP        string
	Nodes      []string
}

// listHAConfigs lists all HA configurations in the directory
func listHAConfigs(configDir string) ([]*HAConfig, error) {
	var configs []*HAConfig

	files, err := os.ReadDir(configDir)
	if err != nil {
		return nil, err
	}

	for _, file := range files {
		if !strings.HasPrefix(file.Name(), "sds-ha-") || !strings.HasSuffix(file.Name(), ".toml") {
			continue
		}

		configPath := fmt.Sprintf("%s/%s", configDir, file.Name())
		cfg, err := readHAConfig(configPath)
		if err != nil {
			continue
		}
		configs = append(configs, cfg)
	}

	return configs, nil
}

// readHAConfig reads and parses an HA configuration file
func readHAConfig(configPath string) (*HAConfig, error) {
	content, err := os.ReadFile(configPath)
	if err != nil {
		return nil, err
	}

	cfg := &HAConfig{
		FSType: "ext4", // default
	}

	lines := strings.Split(string(content), "\n")

	// Extract resource name from filename
	parts := strings.Split(configPath, "/")
	lastPart := parts[len(parts)-1]
	cfg.Resource = strings.TrimPrefix(lastPart, "sds-ha-")
	cfg.Resource = strings.TrimSuffix(cfg.Resource, ".toml")

	// Parse TOML content
	inResourceBlock := false
	for _, line := range lines {
		line = strings.TrimSpace(line)

		// Check for resource block
		if strings.HasPrefix(line, "[promoter.resources.") {
			inResourceBlock = true
			continue
		}
		if inResourceBlock && strings.HasPrefix(line, "]") {
			inResourceBlock = false
			continue
		}

		// Parse start array
		if strings.HasPrefix(line, "start = [") {
			// Multi-line start array
			continue
		}

		// Parse mount unit (e.g., "var-lib-sds.mount")
		if strings.Contains(line, ".mount") {
			mountUnit := strings.TrimSpace(line)
			mountUnit = strings.TrimPrefix(mountUnit, `"`)
			mountUnit = strings.TrimSuffix(mountUnit, `"`)
			mountUnit = strings.TrimSuffix(mountUnit, ",")
			cfg.MountPoint = mountUnit
			// Convert mount unit back to path
			cfg.MountPoint = strings.ReplaceAll(mountUnit, "-", "/")
		}

		// Parse service
		if strings.Contains(line, ".service") {
			svc := strings.TrimSpace(line)
			svc = strings.TrimPrefix(svc, `"`)
			svc = strings.TrimSuffix(svc, `",`)
			svc = strings.TrimSuffix(svc, `"`)
			cfg.Services = append(cfg.Services, svc)
		}

		// Parse preferred-nodes
		if strings.HasPrefix(line, "preferred-nodes = [") {
			nodesStr := strings.TrimPrefix(line, "preferred-nodes = [")
			nodesStr = strings.TrimSuffix(nodesStr, "]")
			nodes := strings.Split(nodesStr, ",")
			for _, node := range nodes {
				node = strings.TrimSpace(node)
				node = strings.TrimPrefix(node, `"`)
				node = strings.TrimSuffix(node, `"`)
				if node != "" {
					cfg.Nodes = append(cfg.Nodes, node)
				}
			}
		}
	}

	return cfg, nil
}

// getReactorStatus gets the reactor status using JSON output
func getReactorStatus() (*ReactorStatus, error) {
	cmd := exec.Command("sudo", "drbd-reactorctl", "status", "--json")
	output, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("failed to get reactor status: %w", err)
	}

	var status ReactorStatus
	if err := json.Unmarshal(output, &status); err != nil {
		return nil, fmt.Errorf("failed to parse reactor status JSON: %w", err)
	}

	return &status, nil
}

// getReactorPromoterStatus gets the promoter status for a specific resource
func getReactorPromoterStatus(resource string) (*ReactorPromoterStatus, error) {
	status, err := getReactorStatus()
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

// statusIcon returns a visual indicator for service status
func statusIcon(status string) string {
	switch status {
	case "active":
		return "●"
	case "inactive":
		return "○"
	case "failed":
		return "✗"
	default:
		return "?"
	}
}
