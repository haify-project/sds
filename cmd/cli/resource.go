package main

import (
	"fmt"
	"strings"

	"github.com/liliang-cn/sds/pkg/client"
	"github.com/spf13/cobra"
)

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
	cmd.AddCommand(resourceStatus())
	cmd.AddCommand(resourcePrimary())
	cmd.AddCommand(resourceFs())
	cmd.AddCommand(resourceMount())
	cmd.AddCommand(resourceUnmount())

	return cmd
}

func resourceCreate() *cobra.Command {
	var name string
	var port uint32
	var nodes string
	var pool string
	var protocol string
	var replicas uint32
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
				return fmt.Errorf("DRBD port is required")
			}
			if size == "" {
				return fmt.Errorf("size is required")
			}

			var nodeList []string
			if nodes != "" {
				nodeList = strings.Split(nodes, ",")
			}

			// Default pool name
			if pool == "" {
				pool = "data-pool"
			}

			// Parse size to GB
			var sizeGB uint32
			_, err := fmt.Sscanf(size, "%d", &sizeGB)
			if err != nil {
				// Try with suffix
				if strings.HasSuffix(size, "G") || strings.HasSuffix(size, "GB") || strings.HasSuffix(size, "GiB") {
					fmt.Sscanf(size, "%d", &sizeGB)
				} else if strings.HasSuffix(size, "T") || strings.HasSuffix(size, "TB") {
					var tmp uint32
					fmt.Sscanf(size, "%d", &tmp)
					sizeGB = tmp * 1024
				}
			}

			if sizeGB == 0 {
				return fmt.Errorf("invalid size format: %s", size)
			}

			// Create SDS client
			sdsClient, err := client.NewSDSClient(controllerAddr)
			if err != nil {
				return fmt.Errorf("failed to connect to controller: %w", err)
			}
			defer sdsClient.Close()

			// Create resource with pool
			err = sdsClient.CreateResourceWithPool(ctx, name, port, nodeList, protocol, sizeGB, pool)
			if err != nil {
				return fmt.Errorf("failed to create resource: %w", err)
			}

			fmt.Printf("✓ Resource created successfully\n")
			fmt.Printf("  Name:     %s\n", name)
			fmt.Printf("  Port:     %d\n", port)
			fmt.Printf("  Pool:     %s\n", pool)
			fmt.Printf("  Nodes:    %v\n", nodeList)
			fmt.Printf("  Protocol: %s\n", protocol)
			fmt.Printf("  Size:     %d GB\n", sizeGB)
			fmt.Printf("\nNext steps:\n")
			fmt.Printf("  1. Check resource status: sds-cli resource status %s\n", name)
			fmt.Printf("  2. Set primary node: sds-cli resource primary %s <node>\n", name)

			return nil
		},
	}

	cmd.Flags().StringVar(&name, "name", "", "Resource name")
	cmd.Flags().Uint32Var(&port, "port", 0, "DRBD port")
	cmd.Flags().StringVar(&nodes, "nodes", "", "Node names (comma-separated)")
	cmd.Flags().StringVar(&pool, "pool", "", "Storage pool name (default: data-pool)")
	cmd.Flags().StringVar(&protocol, "protocol", "C", "DRBD protocol (A, B, or C)")
	cmd.Flags().Uint32Var(&replicas, "replicas", 2, "Number of replicas")
	cmd.Flags().StringVar(&size, "size", "", "Minimum free space")

	cmd.MarkFlagRequired("name")
	cmd.MarkFlagRequired("port")
	cmd.MarkFlagRequired("size")

	return cmd
}

func resourceGet() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "get",
		Short: "Get resource details",
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) == 0 {
				return fmt.Errorf("resource name is required")
			}
			name := args[0]

			// Create SDS client
			sdsClient, err := client.NewSDSClient(controllerAddr)
			if err != nil {
				return fmt.Errorf("failed to connect to controller: %w", err)
			}
			defer sdsClient.Close()

			// Get resource
			resource, err := sdsClient.GetResource(cmd.Context(), name)
			if err != nil {
				return fmt.Errorf("failed to get resource: %w", err)
			}

			fmt.Printf("Resource: %s\n", resource.Name)
			fmt.Printf("  Port:     %d\n", resource.Port)
			fmt.Printf("  Protocol: %s\n", resource.Protocol)
			fmt.Printf("  Role:     %s\n", resource.Role)
			fmt.Printf("  Nodes:    %v\n", resource.Nodes)
			fmt.Printf("  Volumes:\n")
			for _, vol := range resource.Volumes {
				fmt.Printf("    Volume %d: %s (%d GB)\n", vol.VolumeId, vol.Device, vol.SizeGb)
			}

			return nil
		},
	}

	return cmd
}

func resourceDelete() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "delete",
		Short: "Delete a resource",
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) == 0 {
				return fmt.Errorf("resource name is required")
			}
			name := args[0]

			// Create SDS client
			sdsClient, err := client.NewSDSClient(controllerAddr)
			if err != nil {
				return fmt.Errorf("failed to connect to controller: %w", err)
			}
			defer sdsClient.Close()

			// Delete resource
			err = sdsClient.DeleteResource(cmd.Context(), name)
			if err != nil {
				return fmt.Errorf("failed to delete resource: %w", err)
			}

			fmt.Printf("✓ Resource %s deleted successfully\n", name)

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
			// Create SDS client
			sdsClient, err := client.NewSDSClient(controllerAddr)
			if err != nil {
				return fmt.Errorf("failed to connect to controller: %w", err)
			}
			defer sdsClient.Close()

			// List resources
			resources, err := sdsClient.ListResources(cmd.Context())
			if err != nil {
				return fmt.Errorf("failed to list resources: %w", err)
			}

			if len(resources) == 0 {
				fmt.Println("No resources found")
				return nil
			}

			fmt.Printf("Found %d resource(s):\n", len(resources))
			for _, r := range resources {
				fmt.Printf("  - %s (port: %d, protocol: %s, role: %s)\n", r.Name, r.Port, r.Protocol, r.Role)
				fmt.Printf("    Nodes: %v\n", r.Nodes)
				fmt.Printf("    Volumes:\n")
				for _, vol := range r.Volumes {
					fmt.Printf("      Volume %d: %s (%d GB)\n", vol.VolumeId, vol.Device, vol.SizeGb)
				}
			}

			return nil
		},
	}

	return cmd
}

func resourceAddVolume() *cobra.Command {
	var resource string
	var volume string
	var pool string
	var size string

	cmd := &cobra.Command{
		Use:   "add-volume",
		Short: "Add volume to resource",
		RunE: func(cmd *cobra.Command, args []string) error {
			if resource == "" {
				return fmt.Errorf("resource name is required")
			}
			if volume == "" {
				return fmt.Errorf("volume name is required")
			}
			if pool == "" {
				return fmt.Errorf("pool name is required")
			}
			if size == "" {
				return fmt.Errorf("size is required")
			}

			// Parse size to GB
			var sizeGB uint32
			_, err := fmt.Sscanf(size, "%d", &sizeGB)
			if err != nil {
				// Try with suffix
				if strings.HasSuffix(size, "G") || strings.HasSuffix(size, "GB") || strings.HasSuffix(size, "GiB") {
					fmt.Sscanf(size, "%d", &sizeGB)
				} else if strings.HasSuffix(size, "T") || strings.HasSuffix(size, "TB") {
					var tmp uint32
					fmt.Sscanf(size, "%d", &tmp)
					sizeGB = tmp * 1024
				}
			}

			if sizeGB == 0 {
				return fmt.Errorf("invalid size format: %s", size)
			}

			// Create SDS client
			sdsClient, err := client.NewSDSClient(controllerAddr)
			if err != nil {
				return fmt.Errorf("failed to connect to controller: %w", err)
			}
			defer sdsClient.Close()

			// Add volume
			err = sdsClient.AddVolume(cmd.Context(), resource, volume, pool, sizeGB)
			if err != nil {
				return fmt.Errorf("failed to add volume: %w", err)
			}

			fmt.Printf("✓ Volume %s added to resource %s successfully\n", volume, resource)

			return nil
		},
	}

	cmd.Flags().StringVar(&resource, "resource", "", "Resource name")
	cmd.Flags().StringVar(&volume, "volume", "", "Volume name")
	cmd.Flags().StringVar(&pool, "pool", "", "Pool name")
	cmd.Flags().StringVar(&size, "size", "", "Volume size")

	cmd.MarkFlagRequired("resource")
	cmd.MarkFlagRequired("volume")
	cmd.MarkFlagRequired("pool")
	cmd.MarkFlagRequired("size")

	return cmd
}

func resourceRemoveVolume() *cobra.Command {
	var resource string
	var volumeID uint32
	var node string

	cmd := &cobra.Command{
		Use:   "remove-volume",
		Short: "Remove volume from resource",
		RunE: func(cmd *cobra.Command, args []string) error {
			if resource == "" {
				return fmt.Errorf("resource name is required")
			}
			if volumeID == 0 {
				return fmt.Errorf("volume ID is required")
			}
			if node == "" {
				return fmt.Errorf("node is required")
			}

			// Create SDS client
			sdsClient, err := client.NewSDSClient(controllerAddr)
			if err != nil {
				return fmt.Errorf("failed to connect to controller: %w", err)
			}
			defer sdsClient.Close()

			// Remove volume
			err = sdsClient.RemoveVolume(cmd.Context(), resource, volumeID, node)
			if err != nil {
				return fmt.Errorf("failed to remove volume: %w", err)
			}

			fmt.Printf("✓ Volume %d removed from resource %s successfully\n", volumeID, resource)

			return nil
		},
	}

	cmd.Flags().StringVar(&resource, "resource", "", "Resource name")
	cmd.Flags().Uint32Var(&volumeID, "volume-id", 0, "Volume ID")
	cmd.Flags().StringVar(&node, "node", "", "Node name")

	cmd.MarkFlagRequired("resource")
	cmd.MarkFlagRequired("volume-id")
	cmd.MarkFlagRequired("node")

	return cmd
}

func resourceResizeVolume() *cobra.Command {
	var resource string
	var volumeID uint32
	var size string
	var node string

	cmd := &cobra.Command{
		Use:   "resize-volume",
		Short: "Resize volume in resource",
		RunE: func(cmd *cobra.Command, args []string) error {
			if resource == "" {
				return fmt.Errorf("resource name is required")
			}
			if volumeID == 0 {
				return fmt.Errorf("volume ID is required")
			}
			if size == "" {
				return fmt.Errorf("size is required")
			}
			if node == "" {
				return fmt.Errorf("node is required")
			}

			// Parse size to GB
			var sizeGB uint32
			_, err := fmt.Sscanf(size, "%d", &sizeGB)
			if err != nil {
				// Try with suffix
				if strings.HasSuffix(size, "G") || strings.HasSuffix(size, "GB") || strings.HasSuffix(size, "GiB") {
					fmt.Sscanf(size, "%d", &sizeGB)
				} else if strings.HasSuffix(size, "T") || strings.HasSuffix(size, "TB") {
					var tmp uint32
					fmt.Sscanf(size, "%d", &tmp)
					sizeGB = tmp * 1024
				}
			}

			if sizeGB == 0 {
				return fmt.Errorf("invalid size format: %s", size)
			}

			// Create SDS client
			sdsClient, err := client.NewSDSClient(controllerAddr)
			if err != nil {
				return fmt.Errorf("failed to connect to controller: %w", err)
			}
			defer sdsClient.Close()

			// Resize volume
			err = sdsClient.ResizeVolume(cmd.Context(), resource, volumeID, node, sizeGB)
			if err != nil {
				return fmt.Errorf("failed to resize volume: %w", err)
			}

			fmt.Printf("✓ Volume %d in resource %s resized to %d GB successfully\n", volumeID, resource, sizeGB)

			return nil
		},
	}

	cmd.Flags().StringVar(&resource, "resource", "", "Resource name")
	cmd.Flags().Uint32Var(&volumeID, "volume-id", 0, "Volume ID")
	cmd.Flags().StringVar(&size, "size", "", "New size")
	cmd.Flags().StringVar(&node, "node", "", "Node name")

	cmd.MarkFlagRequired("resource")
	cmd.MarkFlagRequired("volume-id")
	cmd.MarkFlagRequired("size")
	cmd.MarkFlagRequired("node")

	return cmd
}

func resourceStatus() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "status",
		Short: "Get DRBD status for a resource",
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) == 0 {
				return fmt.Errorf("resource name is required")
			}
			name := args[0]

			// Create SDS client
			sdsClient, err := client.NewSDSClient(controllerAddr)
			if err != nil {
				return fmt.Errorf("failed to connect to controller: %w", err)
			}
			defer sdsClient.Close()

			// Get resource status
			status, err := sdsClient.ResourceStatus(cmd.Context(), name)
			if err != nil {
				return fmt.Errorf("failed to get resource status: %w", err)
			}

			fmt.Printf("Resource Status: %s\n", status.Name)
			fmt.Printf("  Role:  %s\n", status.Role)
			fmt.Printf("  Nodes: %v\n", status.Nodes)
			if len(status.NodeStates) > 0 {
				fmt.Printf("  Node States:\n")
				for node, state := range status.NodeStates {
					fmt.Printf("    %s:\n", node)
					fmt.Printf("      Role: %s\n", state.Role)
					fmt.Printf("      Disk: %s\n", state.DiskState)
					fmt.Printf("      Replication: %s\n", state.ReplicationState)
				}
			}

			return nil
		},
	}

	return cmd
}

func resourcePrimary() *cobra.Command {
	var resource string
	var node string
	var force bool

	cmd := &cobra.Command{
		Use:   "primary",
		Short: "Set a node as Primary for the resource",
		RunE: func(cmd *cobra.Command, args []string) error {
			if resource == "" {
				return fmt.Errorf("resource name is required")
			}
			if node == "" {
				return fmt.Errorf("node name is required")
			}

			// Create SDS client
			sdsClient, err := client.NewSDSClient(controllerAddr)
			if err != nil {
				return fmt.Errorf("failed to connect to controller: %w", err)
			}
			defer sdsClient.Close()

			// Set primary
			err = sdsClient.SetPrimary(cmd.Context(), resource, node, force)
			if err != nil {
				return fmt.Errorf("failed to set primary: %w", err)
			}

			fmt.Printf("✓ Node %s set as Primary for resource %s successfully\n", node, resource)

			return nil
		},
	}

	cmd.Flags().StringVar(&resource, "resource", "", "Resource name")
	cmd.Flags().StringVar(&node, "node", "", "Node name")
	cmd.Flags().BoolVar(&force, "force", false, "Force primary")

	cmd.MarkFlagRequired("resource")
	cmd.MarkFlagRequired("node")

	return cmd
}

func resourceFs() *cobra.Command {
	var resource string
	var volume uint32
	var fstype string
	var node string

	cmd := &cobra.Command{
		Use:   "fs",
		Short: "Create filesystem on DRBD device",
		RunE: func(cmd *cobra.Command, args []string) error {
			if resource == "" {
				return fmt.Errorf("resource name is required")
			}
			if volume == 0 {
				return fmt.Errorf("volume ID is required")
			}
			if fstype == "" {
				return fmt.Errorf("filesystem type is required")
			}
			if node == "" {
				return fmt.Errorf("node name is required")
			}

			// Create SDS client
			sdsClient, err := client.NewSDSClient(controllerAddr)
			if err != nil {
				return fmt.Errorf("failed to connect to controller: %w", err)
			}
			defer sdsClient.Close()

			// Create filesystem
			err = sdsClient.CreateFilesystem(cmd.Context(), resource, volume, node, fstype)
			if err != nil {
				return fmt.Errorf("failed to create filesystem: %w", err)
			}

			fmt.Printf("✓ Filesystem %s created on resource %s volume %d (node=%s)\n", fstype, resource, volume, node)

			return nil
		},
	}

	cmd.Flags().StringVar(&resource, "resource", "", "Resource name")
	cmd.Flags().Uint32Var(&volume, "volume", 0, "Volume ID")
	cmd.Flags().StringVar(&fstype, "fstype", "", "Filesystem type")
	cmd.Flags().StringVar(&node, "node", "", "Node name")

	cmd.MarkFlagRequired("resource")
	cmd.MarkFlagRequired("volume")
	cmd.MarkFlagRequired("fstype")
	cmd.MarkFlagRequired("node")

	return cmd
}

func resourceMount() *cobra.Command {
	var resource string
	var volume uint32
	var path string
	var node string
	var fstype string

	cmd := &cobra.Command{
		Use:   "mount",
		Short: "Mount DRBD device",
		RunE: func(cmd *cobra.Command, args []string) error {
			if resource == "" {
				return fmt.Errorf("resource name is required")
			}
			if volume == 0 {
				return fmt.Errorf("volume ID is required")
			}
			if path == "" {
				return fmt.Errorf("mount point is required")
			}
			if node == "" {
				return fmt.Errorf("node name is required")
			}

			// Create SDS client
			sdsClient, err := client.NewSDSClient(controllerAddr)
			if err != nil {
				return fmt.Errorf("failed to connect to controller: %w", err)
			}
			defer sdsClient.Close()

			// Mount resource
			err = sdsClient.MountResource(cmd.Context(), resource, volume, path, node, fstype)
			if err != nil {
				return fmt.Errorf("failed to mount resource: %w", err)
			}

			fmt.Printf("✓ Resource %s volume %d mounted to %s (node=%s)\n", resource, volume, path, node)
			if fstype != "" {
				fmt.Printf("  Filesystem: %s\n", fstype)
			}

			return nil
		},
	}

	cmd.Flags().StringVar(&resource, "resource", "", "Resource name")
	cmd.Flags().Uint32Var(&volume, "volume", 0, "Volume ID")
	cmd.Flags().StringVar(&path, "path", "", "Mount point")
	cmd.Flags().StringVar(&node, "node", "", "Node name")
	cmd.Flags().StringVar(&fstype, "fstype", "", "Filesystem type (optional, creates filesystem if specified)")

	cmd.MarkFlagRequired("resource")
	cmd.MarkFlagRequired("volume")
	cmd.MarkFlagRequired("path")
	cmd.MarkFlagRequired("node")

	return cmd
}

func resourceUnmount() *cobra.Command {
	var resource string
	var volume uint32
	var node string

	cmd := &cobra.Command{
		Use:   "unmount",
		Short: "Unmount DRBD device",
		RunE: func(cmd *cobra.Command, args []string) error {
			if resource == "" {
				return fmt.Errorf("resource name is required")
			}
			if volume == 0 {
				return fmt.Errorf("volume ID is required")
			}
			if node == "" {
				return fmt.Errorf("node name is required")
			}

			// Create SDS client
			sdsClient, err := client.NewSDSClient(controllerAddr)
			if err != nil {
				return fmt.Errorf("failed to connect to controller: %w", err)
			}
			defer sdsClient.Close()

			// Unmount resource
			err = sdsClient.UnmountResource(cmd.Context(), resource, volume, node)
			if err != nil {
				return fmt.Errorf("failed to unmount resource: %w", err)
			}

			fmt.Printf("✓ Resource %s volume %d unmounted successfully (node=%s)\n", resource, volume, node)

			return nil
		},
	}

	cmd.Flags().StringVar(&resource, "resource", "", "Resource name")
	cmd.Flags().Uint32Var(&volume, "volume", 0, "Volume ID")
	cmd.Flags().StringVar(&node, "node", "", "Node name")

	cmd.MarkFlagRequired("resource")
	cmd.MarkFlagRequired("volume")
	cmd.MarkFlagRequired("node")

	return cmd
}
