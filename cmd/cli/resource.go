package main

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/liliang-cn/sds/pkg/client"
	"github.com/liliang-cn/sds/pkg/util"
	"github.com/spf13/cobra"
)

// formatSize formats a size in GB to human-readable string
func formatSize(sizeGB uint64) string {
	if sizeGB == 0 {
		return "0 GB"
	}
	if sizeGB < 1 {
		return "< 1 GB"
	}
	return fmt.Sprintf("%d GB", sizeGB)
}

func resourceCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "resource",
		Short: "Resource management (DRBD resources with multiple volumes)",
	}

	cmd.AddCommand(resourceCreate())
	cmd.AddCommand(resourceGet())
	cmd.AddCommand(resourceDelete())
	cmd.AddCommand(resourceList())
	cmd.AddCommand(resourceAddVolume())
	cmd.AddCommand(resourceRemoveVolume())
	cmd.AddCommand(resourceResizeVolume())
	cmd.AddCommand(resourcePrimary())
	cmd.AddCommand(resourceSecondary())
	cmd.AddCommand(resourceFs())

	return cmd
}

func resourceCreate() *cobra.Command {
	var name string
	var port uint32
	var nodes string
	var pool string
	var protocol string
	var size string

	cmd := &cobra.Command{
		Use:   "create",
		Short: "Create a new DRBD resource",
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()

			if name == "" {
				return fmt.Errorf("resource name is required")
			}
			if port == 0 {
				return fmt.Errorf("DRBD port is required (use --port)")
			}
			if size == "" {
				return fmt.Errorf("size is required (use --size)")
			}

			var nodeList []string
			if nodes != "" {
				nodeList = strings.Split(nodes, ",")
			} else {
				return fmt.Errorf("nodes are required (use --nodes)")
			}

			if pool == "" {
				pool = "data-pool"
			}

			if protocol == "" {
				protocol = "C"
			}

			sizeBytes, err := util.ParseSize(size)
			if err != nil {
				return fmt.Errorf("invalid size format: %s: %w", size, err)
			}
			sizeGiB := util.BytesToGiB(sizeBytes)
			if sizeGiB == 0 {
				return fmt.Errorf("size too small (minimum 1 GiB)")
			}

			sdsClient, err := client.NewSDSClient(controllerAddr)
			if err != nil {
				return fmt.Errorf("failed to connect to controller: %w", err)
			}
			defer sdsClient.Close()

			err = sdsClient.CreateResourceWithPool(ctx, name, port, nodeList, protocol, uint32(sizeGiB), pool)
			if err != nil {
				return fmt.Errorf("failed to create resource: %w", err)
			}

			fmt.Printf("Resource created successfully\n")
			fmt.Printf("  Name:     %s\n", name)
			fmt.Printf("  Port:     %d\n", port)
			fmt.Printf("  Pool:     %s\n", pool)
			fmt.Printf("  Nodes:    %v\n", nodeList)
			fmt.Printf("  Protocol: %s\n", protocol)
			fmt.Printf("  Size:     %d GiB (%s)\n", sizeGiB, util.FormatBytes(sizeBytes))
			fmt.Printf("\nNext steps:\n")
			fmt.Printf("  1. sds-cli resource get %s\n", name)
			fmt.Printf("  2. sds-cli resource primary %s <node>\n", name)

			return nil
		},
	}

	cmd.Flags().StringVar(&name, "name", "", "Resource name (required)")
	cmd.Flags().Uint32Var(&port, "port", 0, "DRBD port (required)")
	cmd.Flags().StringVar(&nodes, "nodes", "", "Node names (comma-separated, required)")
	cmd.Flags().StringVar(&pool, "pool", "", "Storage pool name (default: data-pool)")
	cmd.Flags().StringVar(&protocol, "protocol", "C", "DRBD protocol (A, B, or C)")
	cmd.Flags().StringVar(&size, "size", "", "Volume size (e.g., 1G, 10GB, 1TB, 1GiB, required)")

	cmd.MarkFlagRequired("name")
	cmd.MarkFlagRequired("port")
	cmd.MarkFlagRequired("nodes")
	cmd.MarkFlagRequired("size")

	return cmd
}

func resourceGet() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "get <name>",
		Short: "Get resource details",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]

			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()

			sdsClient, err := client.NewSDSClient(controllerAddr)
			if err != nil {
				return fmt.Errorf("failed to connect to controller: %w", err)
			}
			defer sdsClient.Close()

			resource, err := sdsClient.GetResource(ctx, name)
			if err != nil {
				return fmt.Errorf("failed to get resource: %w", err)
			}

			fmt.Printf("Resource: %s\n", resource.Name)
			fmt.Printf("  Port:     %d\n", resource.Port)
			fmt.Printf("  Protocol: %s\n", resource.Protocol)
			fmt.Printf("  Nodes:\n")
			for _, node := range resource.Nodes {
				state := "Unknown"
				diskState := ""
				if ns, ok := resource.NodeStates[node]; ok {
					state = ns.Role
					if ns.DiskState != "" {
						diskState = fmt.Sprintf(", disk: %s", ns.DiskState)
					}
				}
				fmt.Printf("    %s: %s%s\n", node, state, diskState)
			}
			if len(resource.Volumes) > 0 {
				fmt.Printf("  Volumes:\n")
				for _, vol := range resource.Volumes {
					fmt.Printf("    Volume %d: %s (%s)\n", vol.VolumeId, vol.Device, formatSize(vol.SizeGb))
				}
			}

			return nil
		},
	}

	return cmd
}

func resourceDelete() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "delete <name>",
		Short: "Delete a resource",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]

			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()

			sdsClient, err := client.NewSDSClient(controllerAddr)
			if err != nil {
				return fmt.Errorf("failed to connect to controller: %w", err)
			}
			defer sdsClient.Close()

			err = sdsClient.DeleteResource(ctx, name)
			if err != nil {
				return fmt.Errorf("failed to delete resource: %w", err)
			}

			fmt.Printf("Resource '%s' deleted successfully\n", name)
			return nil
		},
	}

	return cmd
}

func resourceList() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List all resources",
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()

			sdsClient, err := client.NewSDSClient(controllerAddr)
			if err != nil {
				return fmt.Errorf("failed to connect to controller: %w", err)
			}
			defer sdsClient.Close()

			resources, err := sdsClient.ListResources(ctx)
			if err != nil {
				return fmt.Errorf("failed to list resources: %w", err)
			}

			if len(resources) == 0 {
				fmt.Println("No resources found")
				return nil
			}

			for _, r := range resources {
				fmt.Printf("%s (port=%d, protocol=%s, nodes=%v)\n", r.Name, r.Port, r.Protocol, r.Nodes)
			}

			return nil
		},
	}

	return cmd
}

func resourceAddVolume() *cobra.Command {
	var name string
	var volume string
	var pool string
	var size string

	cmd := &cobra.Command{
		Use:   "add-volume <resource>",
		Short: "Add a volume to resource",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			resource := args[0]

			if volume == "" {
				return fmt.Errorf("volume name is required (--volume)")
			}
			if size == "" {
				return fmt.Errorf("size is required (--size)")
			}
			if pool == "" {
				return fmt.Errorf("pool is required (--pool)")
			}

			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()

			sizeBytes, err := util.ParseSize(size)
			if err != nil {
				return fmt.Errorf("invalid size format: %s: %w", size, err)
			}
			sizeGiB := util.BytesToGiB(sizeBytes)
			if sizeGiB == 0 {
				return fmt.Errorf("size too small (minimum 1 GiB)")
			}

			sdsClient, err := client.NewSDSClient(controllerAddr)
			if err != nil {
				return fmt.Errorf("failed to connect to controller: %w", err)
			}
			defer sdsClient.Close()

			err = sdsClient.AddVolume(ctx, resource, volume, pool, uint32(sizeGiB))
			if err != nil {
				return fmt.Errorf("failed to add volume: %w", err)
			}

			fmt.Printf("Volume '%s' added to '%s' (size: %s)\n", volume, resource, util.FormatBytes(sizeBytes))
			return nil
		},
	}

	cmd.Flags().StringVar(&name, "name", "", "Volume name (required)")
	cmd.Flags().StringVar(&volume, "volume", "", "Volume name (required)")
	cmd.Flags().StringVar(&pool, "pool", "", "Storage pool (required)")
	cmd.Flags().StringVar(&size, "size", "", "Volume size (e.g., 1G, 10GB, 1TB, required)")

	// For compatibility, map --name to --volume
	cmd.PreRunE = func(cmd *cobra.Command, args []string) error {
		if name != "" && volume == "" {
			volume = name
		}
		return nil
	}

	return cmd
}

func resourceRemoveVolume() *cobra.Command {
	var node string

	cmd := &cobra.Command{
		Use:   "remove-volume <resource> <volume-id>",
		Short: "Remove a volume from resource",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			resource := args[0]
			var volumeID uint32
			_, err := fmt.Sscanf(args[1], "%d", &volumeID)
			if err != nil {
				return fmt.Errorf("invalid volume ID: %s", args[1])
			}

			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()

			sdsClient, err := client.NewSDSClient(controllerAddr)
			if err != nil {
				return fmt.Errorf("failed to connect to controller: %w", err)
			}
			defer sdsClient.Close()

			err = sdsClient.RemoveVolume(ctx, resource, volumeID, node)
			if err != nil {
				return fmt.Errorf("failed to remove volume: %w", err)
			}

			fmt.Printf("Volume %d removed from '%s'\n", volumeID, resource)
			return nil
		},
	}

	cmd.Flags().StringVar(&node, "node", "", "Target node (required)")

	return cmd
}

func resourceResizeVolume() *cobra.Command {
	var node string
	var size string

	cmd := &cobra.Command{
		Use:   "resize-volume <resource> <volume-id> <size>",
		Short: "Resize a volume",
		Args:  cobra.ExactArgs(3),
		RunE: func(cmd *cobra.Command, args []string) error {
			resource := args[0]
			var volumeID uint32
			_, err := fmt.Sscanf(args[1], "%d", &volumeID)
			if err != nil {
				return fmt.Errorf("invalid volume ID: %s", args[1])
			}
			size = args[2]

			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()

			sizeBytes, err := util.ParseSize(size)
			if err != nil {
				return fmt.Errorf("invalid size format: %s: %w", size, err)
			}
			sizeGiB := util.BytesToGiB(sizeBytes)
			if sizeGiB == 0 {
				return fmt.Errorf("size too small (minimum 1 GiB)")
			}

			sdsClient, err := client.NewSDSClient(controllerAddr)
			if err != nil {
				return fmt.Errorf("failed to connect to controller: %w", err)
			}
			defer sdsClient.Close()

			err = sdsClient.ResizeVolume(ctx, resource, volumeID, node, uint32(sizeGiB))
			if err != nil {
				return fmt.Errorf("failed to resize volume: %w", err)
			}

			fmt.Printf("Volume %d resized to %s\n", volumeID, util.FormatBytes(sizeBytes))
			return nil
		},
	}

	cmd.Flags().StringVar(&node, "node", "", "Target node (required)")

	return cmd
}

func resourcePrimary() *cobra.Command {
	var force bool

	cmd := &cobra.Command{
		Use:   "primary <resource> <node>",
		Short: "Set resource primary on node",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			resource := args[0]
			node := args[1]

			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()

			sdsClient, err := client.NewSDSClient(controllerAddr)
			if err != nil {
				return fmt.Errorf("failed to connect to controller: %w", err)
			}
			defer sdsClient.Close()

			err = sdsClient.SetPrimary(ctx, resource, node, force)
			if err != nil {
				return fmt.Errorf("failed to set primary: %w", err)
			}

			fmt.Printf("Resource '%s' primary set to '%s'\n", resource, node)
			return nil
		},
	}

	cmd.Flags().BoolVar(&force, "force", false, "Force promotion")

	return cmd
}

func resourceSecondary() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "secondary <resource> <node>",
		Short: "Set resource secondary on node",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			resource := args[0]
			node := args[1]

			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()

			sdsClient, err := client.NewSDSClient(controllerAddr)
			if err != nil {
				return fmt.Errorf("failed to connect to controller: %w", err)
			}
			defer sdsClient.Close()

			err = sdsClient.SetSecondary(ctx, resource, node)
			if err != nil {
				return fmt.Errorf("failed to set secondary: %w", err)
			}

			fmt.Printf("Resource '%s' set to secondary on '%s'\n", resource, node)
			return nil
		},
	}

	return cmd
}

func resourceFs() *cobra.Command {
	var node string

	cmd := &cobra.Command{
		Use:   "fs <resource> <volume-id> <fstype>",
		Short: "Create filesystem on volume",
		Args:  cobra.ExactArgs(3),
		RunE: func(cmd *cobra.Command, args []string) error {
			resource := args[0]
			var volumeID uint32
			_, err := fmt.Sscanf(args[1], "%d", &volumeID)
			if err != nil {
				return fmt.Errorf("invalid volume ID: %s", args[1])
			}
			fstype := args[2]

			ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
			defer cancel()

			sdsClient, err := client.NewSDSClient(controllerAddr)
			if err != nil {
				return fmt.Errorf("failed to connect to controller: %w", err)
			}
			defer sdsClient.Close()

			err = sdsClient.CreateFilesystem(ctx, resource, volumeID, node, fstype)
			if err != nil {
				return fmt.Errorf("failed to create filesystem: %w", err)
			}

			fmt.Printf("Filesystem '%s' created on volume %d\n", fstype, volumeID)
			return nil
		},
	}

	cmd.Flags().StringVar(&node, "node", "", "Target node (required)")

	return cmd
}
