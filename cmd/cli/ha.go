package main

import (
	"context"
	"fmt"
	"os"
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

			fmt.Printf("HA Configurations (%d):\n", len(configFiles))
			for _, cfg := range configFiles {
				fmt.Printf("  - %s\n", cfg.Resource)
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

			fmt.Printf("HA Configuration: %s\n", resource)
			fmt.Printf("  Config:    %s\n", configPath)
			if cfg.MountPoint != "" {
				fmt.Printf("  Mount:     %s (%s)\n", cfg.MountPoint, cfg.FSType)
				fmt.Printf("  Mount Unit: %s.mount\n", strings.TrimPrefix(strings.ReplaceAll(cfg.MountPoint, "/", "-"), "/"))
			}
			if len(cfg.Services) > 0 {
				fmt.Printf("  Services:  %v\n", cfg.Services)
			}
			if cfg.VIP != "" {
				fmt.Printf("  VIP:       %s\n", cfg.VIP)
			}
			fmt.Printf("  Nodes:     %v\n", cfg.Nodes)

			// Show drbd-reactor status
			fmt.Printf("\nChecking drbd-reactor status...\n")
			fmt.Printf("Run: drbd-reactorctl status sds-ha-%s.toml\n", resource)

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
