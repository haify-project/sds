package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/liliang-cn/sds/pkg/client"
	"github.com/spf13/cobra"
)

func haCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "ha",
		Short: "Make a DRBD resource highly available with drbd-reactor",
	}

	cmd.AddCommand(haCreate())
	cmd.AddCommand(haDelete())
	cmd.AddCommand(haList())
	cmd.AddCommand(haStatus())
	cmd.AddCommand(haEvict())

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

			// For now, we need to remove the config file directly
			// In the future, this could be a proper RPC call
			configPath := fmt.Sprintf("/etc/drbd-reactor.d/sds-ha-%s.toml", resource)
			fmt.Printf("To delete HA config, remove the following file on all nodes:\n")
			fmt.Printf("  %s\n", configPath)
			fmt.Printf("Then reload drbd-reactor:\n")
			fmt.Printf("  systemctl reload drbd-reactor\n")

			return nil
		},
	}

	return cmd
}

func haEvict() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "evict <resource>",
		Short: "Evict HA resource from active node (triggers failover)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			resource := args[0]

			// Use longer timeout for evict operation
			// drbd-reactorctl evict waits for failover to complete (up to 60s)
			ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
			defer cancel()

			sdsClient, err := client.NewSDSClient(controllerAddr)
			if err != nil {
				return fmt.Errorf("failed to connect to controller: %w", err)
			}
			defer sdsClient.Close()

			err = sdsClient.EvictHa(ctx, resource)
			if err != nil {
				return fmt.Errorf("failed to evict HA resource: %w", err)
			}

			fmt.Printf("HA resource evicted successfully\n")
			fmt.Printf("  Resource: %s\n", resource)
			fmt.Printf("  The resource should fail over to another node\n")

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

			fmt.Printf("HA Configurations (%d):\n", len(configFiles))
			for _, cfg := range configFiles {
				fmt.Printf("  - %s\n", cfg.Resource)
				if cfg.ActiveNode != "" {
					fmt.Printf("      Active: %s\n", cfg.ActiveNode)
				}
				if len(cfg.Nodes) > 0 {
					fmt.Printf("      Nodes: %v\n", cfg.Nodes)
				}
				if cfg.MountPoint != "" {
					fmt.Printf("      Mount: %s (%s)\n", cfg.MountPoint, cfg.FSType)
				}
				if len(cfg.Services) > 0 {
					fmt.Printf("      Services: %v\n", cfg.Services)
				}
				if cfg.VIP != "" {
					fmt.Printf("      VIP: %s\n", cfg.VIP)
				}
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

			// Get active node and nodes from DRBD status
			cfg.ActiveNode = getActiveNode(resource)
			cfg.Nodes = getNodesFromDRBD(resource)

			fmt.Printf("HA Configuration: %s\n", resource)
			fmt.Printf("  Config:    %s\n", configPath)
			if cfg.ActiveNode != "" {
				fmt.Printf("  Active:    %s\n", cfg.ActiveNode)
			}
			if cfg.MountPoint != "" {
				fmt.Printf("  Mount:     %s (%s)\n", cfg.MountPoint, cfg.FSType)
				mountUnit := strings.TrimPrefix(cfg.MountPoint, "/")
				mountUnit = strings.ReplaceAll(mountUnit, "/", "-")
				fmt.Printf("  Mount Unit: %s.mount\n", mountUnit)
			}
			if len(cfg.Services) > 0 {
				fmt.Printf("  Services:  %v\n", cfg.Services)
			}
			if cfg.VIP != "" {
				fmt.Printf("  VIP:       %s\n", cfg.VIP)
			}
			fmt.Printf("  Nodes:     %v\n", cfg.Nodes)

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
	ActiveNode string
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
		// Get active node and nodes from DRBD status
		cfg.ActiveNode = getActiveNode(cfg.Resource)
		cfg.Nodes = getNodesFromDRBD(cfg.Resource)
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
	for _, line := range lines {
		line = strings.TrimSpace(line)

		// Parse mount unit (e.g., "var-lib-sds.mount")
		if strings.Contains(line, ".mount") {
			mountUnit := strings.TrimSpace(line)
			// Remove leading quote
			mountUnit = strings.TrimPrefix(mountUnit, "\"")
			// Remove trailing comma and quote
			mountUnit = strings.TrimSuffix(mountUnit, "\",")
			mountUnit = strings.TrimSuffix(mountUnit, ",")
			mountUnit = strings.TrimSuffix(mountUnit, "\"")
			// Remove .mount suffix
			mountUnit = strings.TrimSuffix(mountUnit, ".mount")
			mountUnit = strings.TrimSpace(mountUnit)
			// Convert mount unit back to path
			if mountUnit != "" {
				cfg.MountPoint = "/" + strings.ReplaceAll(mountUnit, "-", "/")
			}
		}

		// Parse service
		if strings.Contains(line, ".service") {
			svc := strings.TrimSpace(line)
			// Remove leading quote
			svc = strings.TrimPrefix(svc, "\"")
			// Remove trailing comma and quote
			svc = strings.TrimSuffix(svc, "\",")
			svc = strings.TrimSuffix(svc, ",")
			svc = strings.TrimSuffix(svc, "\"")
			svc = strings.TrimSpace(svc)
			if svc != "" {
				cfg.Services = append(cfg.Services, svc)
			}
		}

		// Parse VIP from OCF IPaddr2 line
		if strings.Contains(line, "ocf:heartbeat:IPaddr2") {
			// Format: "ocf:heartbeat:IPaddr2 vip_ha_res ip=192.168.123.70 cidr_netmask=24"
			cleanLine := strings.TrimPrefix(strings.TrimSpace(line), "\"")
			cleanLine = strings.TrimSuffix(cleanLine, "\",")
			cleanLine = strings.TrimSuffix(cleanLine, ",")
			cleanLine = strings.TrimSuffix(cleanLine, "\"")
			// Extract ip=X value
			parts := strings.Fields(cleanLine)
			for _, part := range parts {
				if strings.HasPrefix(part, "ip=") {
					cfg.VIP = strings.TrimPrefix(part, "ip=")
					// Find cidr_netmask and append
				}
				if strings.HasPrefix(part, "cidr_netmask=") {
					cidr := strings.TrimPrefix(part, "cidr_netmask=")
					if cfg.VIP != "" {
						cfg.VIP = cfg.VIP + "/" + cidr
					}
				}
			}
		}
	}

	return cfg, nil
}

// getActiveNode gets the active (primary) node for a DRBD resource
func getActiveNode(resource string) string {
	// Get hostname first
	hostnameBytes, err := os.ReadFile("/proc/sys/kernel/hostname")
	if err != nil {
		hostnameBytes, err = os.ReadFile("/etc/hostname")
		if err != nil {
			return ""
		}
	}
	hostname := strings.TrimSpace(string(hostnameBytes))

	// Check drbdsetup status output
	statusOutput, err := exec.Command("drbdsetup", "status", resource).Output()
	if err != nil {
		return ""
	}

	statusStr := string(statusOutput)
	lines := strings.Split(statusStr, "\n")

	// First line: "resource_name role:Role" - this is LOCAL node's role
	// Following lines: "nodename role:Role" - these are REMOTE nodes' roles
	for i, line := range lines {
		line = strings.TrimSpace(line)

		// Skip empty lines
		if line == "" {
			continue
		}

		// Check if this line contains role information
		if strings.Contains(line, " role:Primary") || strings.Contains(line, " role:Secondary") {
			// Parse the node name and role
			// Format: "resource role:Primary" (local node) or "nodename role:Primary" (remote)
			parts := strings.SplitN(line, " ", 2)
			if len(parts) >= 2 && strings.HasPrefix(parts[1], "role:") {
				nodeName := strings.TrimSpace(parts[0])
				role := strings.TrimPrefix(parts[1], "role:")

				// For the first line, the node name is the resource name (local node)
				if i == 0 && nodeName == resource {
					if strings.HasPrefix(role, "Primary") {
						return hostname
					}
				} else if strings.HasPrefix(role, "Primary") {
					return nodeName
				}
			}
		}
	}

	return ""
}

// getNodesFromDRBD gets all nodes for a DRBD resource from drbdsetup status
func getNodesFromDRBD(resource string) []string {
	// Get hostname first
	hostnameBytes, err := os.ReadFile("/proc/sys/kernel/hostname")
	if err != nil {
		hostnameBytes, err = os.ReadFile("/etc/hostname")
		if err != nil {
			return nil
		}
	}
	hostname := strings.TrimSpace(string(hostnameBytes))

	// Check drbdsetup status output
	statusOutput, err := exec.Command("drbdsetup", "status", resource).Output()
	if err != nil {
		return nil
	}

	statusStr := string(statusOutput)
	lines := strings.Split(statusStr, "\n")

	var nodes []string
	nodeMap := make(map[string]bool)

	// First line: "resource_name role:Role" - this is LOCAL node's role
	// Following lines: "nodename role:Role" - these are REMOTE nodes' roles
	for i, line := range lines {
		line = strings.TrimSpace(line)

		// Skip empty lines
		if line == "" {
			continue
		}

		// Check if this line contains role information
		if strings.Contains(line, " role:Primary") || strings.Contains(line, " role:Secondary") {
			// Parse the node name and role
			// Format: "resource role:Primary" (local node) or "nodename role:Primary" (remote)
			parts := strings.SplitN(line, " ", 2)
			if len(parts) >= 2 && strings.HasPrefix(parts[1], "role:") {
				nodeName := strings.TrimSpace(parts[0])

				// For the first line, the node name is the resource name (local node)
				if i == 0 && nodeName == resource {
					nodeMap[hostname] = true
				} else if nodeName != resource && nodeName != "" {
					nodeMap[nodeName] = true
				}
			}
		}
	}

	// Convert map to slice
	for node := range nodeMap {
		nodes = append(nodes, node)
	}

	return nodes
}
