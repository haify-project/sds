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
	Promoter  []ReactorPromoterStatus `json:"promoter"`
	Prometheus []ReactorPluginStatus  `json:"prometheus"`
	Debugger  []ReactorPluginStatus   `json:"debugger"`
	UMH       []ReactorPluginStatus   `json:"umh"`
	AgentX    []ReactorPluginStatus   `json:"agentx"`
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
	cmd.AddCommand(haDelete())
	cmd.AddCommand(haList())
	cmd.AddCommand(haStatus())

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

func haList() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List all HA configurations",
		RunE: func(cmd *cobra.Command, args []string) error {
			configDir := "/etc/drbd-reactor.d"
			configFiles, err := listHAConfigs(configDir)
			if err != nil {
				return fmt.Errorf("failed to list HA configs: %w", err)
			}

			if len(configFiles) == 0 {
				fmt.Println("No HA configurations found")
				return nil
			}

			// Get reactor status for all promoters
			reactorStatus, err := getReactorStatus()
			if err != nil {
				fmt.Printf("Warning: could not get reactor status: %v\n\n", err)
				reactorStatus = &ReactorStatus{}
			}

			// Build a map of resource -> promoter status
			promoterMap := make(map[string]*ReactorPromoterStatus)
			for i := range reactorStatus.Promoter {
				p := &reactorStatus.Promoter[i]
				promoterMap[p.DRBDResource] = p
			}

			fmt.Printf("HA Configurations (%d):\n", len(configFiles))
			for _, cfg := range configFiles {
				fmt.Printf("  - %s\n", cfg.Resource)

				// Show reactor status if available
				if promoter, ok := promoterMap[cfg.Resource]; ok {
					icon := statusIcon(promoter.Status)
					fmt.Printf("      Status:    %s %s\n", icon, promoter.Status)
					if promoter.PrimaryOn != "" {
						fmt.Printf("      Primary:   %s\n", promoter.PrimaryOn)
					}
				}

				if cfg.MountPoint != "" {
					fmt.Printf("      Mount:     %s (%s)\n", cfg.MountPoint, cfg.FSType)
				}
				if len(cfg.Services) > 0 {
					// Filter out internal services for cleaner display
					var userServices []string
					for _, svc := range cfg.Services {
						if !strings.HasPrefix(svc, "drbd-") && !strings.HasPrefix(svc, "service-ip@") {
							userServices = append(userServices, svc)
						}
					}
					if len(userServices) > 0 {
						fmt.Printf("      Services:  %v\n", userServices)
					}
				}
				if cfg.VIP != "" {
					fmt.Printf("      VIP:       %s\n", cfg.VIP)
				}
				fmt.Println()
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
			configPath := fmt.Sprintf("/etc/drbd-reactor.d/sds-ha-%s.toml", resource)

			cfg, err := readHAConfig(configPath)
			if err != nil {
				return fmt.Errorf("failed to read HA config: %w", err)
			}

			// Get reactor status
			promoter, err := getReactorPromoterStatus(resource)

			fmt.Printf("HA Configuration: %s\n", resource)
			fmt.Printf("  Config:  %s\n", configPath)

			// Show reactor status
			if err == nil && promoter != nil {
				icon := statusIcon(promoter.Status)
				fmt.Printf("  Status:  %s %s\n", icon, promoter.Status)
				if promoter.PrimaryOn != "" {
					fmt.Printf("  Active:  %s\n", promoter.PrimaryOn)
				}
			} else {
				fmt.Printf("  Status:  (unable to get reactor status)\n")
			}

			if cfg.MountPoint != "" {
				fmt.Printf("  Mount:   %s (%s)\n", cfg.MountPoint, cfg.FSType)
			}
			if cfg.VIP != "" {
				fmt.Printf("  VIP:     %s\n", cfg.VIP)
			}
			if len(cfg.Services) > 0 {
				// Show user services (filter internal ones)
				var userServices []string
				for _, svc := range cfg.Services {
					if !strings.HasPrefix(svc, "drbd-") && !strings.HasPrefix(svc, "service-ip@") {
						userServices = append(userServices, svc)
					}
				}
				if len(userServices) > 0 {
					fmt.Printf("  Services: %v\n", userServices)
				}
			}

			// Show detailed service status from reactor
			if promoter != nil {
				fmt.Printf("\nServices:\n")
				fmt.Printf("  %s %s\n", statusIcon(promoter.Target.Status), promoter.Target.Name)
				for i, dep := range promoter.Dependencies {
					prefix := "├─"
					if i == len(promoter.Dependencies)-1 {
						prefix = "└─"
					}
					fmt.Printf("    %s %s %s\n", statusIcon(dep.Status), prefix, dep.Name)
				}
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
