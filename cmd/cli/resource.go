package main

import (
	"context"
	"fmt"
	"strings"
	"time"

	sdspb "github.com/liliang-cn/sds/api/proto/v1"
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
	cmd.AddCommand(resourceAdopt())
	cmd.AddCommand(resourceGet())
	cmd.AddCommand(resourceDelete())
	cmd.AddCommand(resourceList())
	cmd.AddCommand(resourceAddVolume())
	cmd.AddCommand(resourceRemoveVolume())
	cmd.AddCommand(resourceResizeVolume())
	cmd.AddCommand(resourceSetOptions())
	cmd.AddCommand(resourcePrimary())
	cmd.AddCommand(resourceDRFailover())
	cmd.AddCommand(resourceSecondary())
	cmd.AddCommand(resourceFs())
	cmd.AddCommand(resourceStatus())
	cmd.AddCommand(resourceMount())
	cmd.AddCommand(resourceUnmount())
	cmd.AddCommand(resourcePromote())
	cmd.AddCommand(resourceDemote())
	cmd.AddCommand(resourceSnapshot())

	return cmd
}

func resourceSetOptions() *cobra.Command {
	var options map[string]string
	cmd := &cobra.Command{
		Use:   "set-options <resource>",
		Short: "Update DRBD options on a resource and apply with drbdadm adjust",
		Long: "Update DRBD options on an existing resource and apply them with " +
			"`drbdadm adjust`, without recreating the resource. Options use the " +
			"\"section/key\" form (e.g. net/max-buffers=8000, disk/on-io-error=detach); " +
			"a bare key goes to the resource-level options section.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(options) == 0 {
				return fmt.Errorf("at least one --drbd-options key=value is required")
			}
			ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
			defer cancel()
			sdsClient, err := newSDSClient()
			if err != nil {
				return fmt.Errorf("failed to connect to controller: %w", err)
			}
			defer sdsClient.Close()
			if err := sdsClient.UpdateResourceOptions(ctx, args[0], options); err != nil {
				return fmt.Errorf("failed to update options: %w", err)
			}
			fmt.Printf("Options applied to '%s' and adjusted: %v\n", args[0], options)
			return nil
		},
	}
	cmd.Flags().StringToStringVar(&options, "drbd-options", nil,
		"DRBD options as section/key=value (e.g. net/max-buffers=8000,on-no-quorum=suspend-io)")
	return cmd
}

func resourceCreate() *cobra.Command {
	var name string
	var port uint32
	var nodes string
	var pool string
	var storageType string
	var protocol string
	var size string
	var drbdOptions map[string]string
	var wan bool
	var drNode string
	var drEndpoint string
	var wanPort uint32

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

			// WAN master switch (mirrors the server guard): the --dr-* / --wan-port
			// flags only apply with --wan.
			if !wan && (drNode != "" || drEndpoint != "" || wanPort != 0) {
				return fmt.Errorf("--dr-node/--dr-endpoint/--wan-port require --wan")
			}

			var nodeList []string
			if nodes != "" {
				nodeList = strings.Split(nodes, ",")
			} else {
				return fmt.Errorf("nodes are required (use --nodes)")
			}

			if wan {
				if len(nodeList) != 1 {
					return fmt.Errorf("WAN resource requires exactly one primary node in --nodes, got %d", len(nodeList))
				}
				if drNode == "" {
					return fmt.Errorf("WAN resource requires --dr-node")
				}
				if drEndpoint == "" {
					return fmt.Errorf("WAN resource requires --dr-endpoint")
				}
			}

			if pool == "" {
				pool = "data-pool"
			}

			if storageType == "" {
				storageType = "lvm"
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

			sdsClient, err := newSDSClient()
			if err != nil {
				return fmt.Errorf("failed to connect to controller: %w", err)
			}
			defer sdsClient.Close()

			// WAN mode routes replication through a per-resource sds-proxy pair
			// (protocol A + loopback DRBD). Otherwise use the unified LAN path
			// for all storage types (behavior unchanged).
			if wan {
				err = sdsClient.CreateResourceWAN(ctx, name, port, nodeList[0], uint32(sizeGiB), pool, storageType, drbdOptions, drNode, drEndpoint, wanPort)
			} else {
				err = sdsClient.CreateResourceWithPoolAndType(ctx, name, port, nodeList, protocol, uint32(sizeGiB), pool, storageType, drbdOptions)
			}
			if err != nil {
				return fmt.Errorf("failed to create resource: %w", err)
			}

			fmt.Printf("Resource created successfully\n")
			fmt.Printf("  Name:        %s\n", name)
			fmt.Printf("  Port:        %d\n", port)
			fmt.Printf("  Storage:     %s\n", storageType)
			fmt.Printf("  Pool:        %s\n", pool)
			fmt.Printf("  Nodes:       %v\n", nodeList)
			if wan {
				fmt.Printf("  Protocol:    A (WAN)\n")
				fmt.Printf("  DR node:     %s\n", drNode)
				fmt.Printf("  DR endpoint: %s\n", drEndpoint)
				if wanPort != 0 {
					fmt.Printf("  WAN port:    %d\n", wanPort)
				} else {
					fmt.Printf("  WAN port:    auto (random >3000)\n")
				}
			} else {
				fmt.Printf("  Protocol:    %s\n", protocol)
			}
			fmt.Printf("  Size:        %d GiB (%s)\n", sizeGiB, util.FormatBytes(sizeBytes))
			if len(drbdOptions) > 0 {
				fmt.Printf("  Options:     %v\n", drbdOptions)
			}
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
	cmd.Flags().StringVar(&storageType, "storage-type", "lvm", "Storage type: lvm or zfs")
	cmd.Flags().StringVar(&protocol, "protocol", "C", "DRBD protocol (A, B, or C)")
	cmd.Flags().StringVar(&size, "size", "", "Volume size (e.g., 1G, 10GB, 1TB, 1GiB, required)")
	cmd.Flags().StringToStringVar(&drbdOptions, "drbd-options", nil, "DRBD options as key=value pairs (e.g., on-no-quorum=suspend-io)")
	cmd.Flags().BoolVar(&wan, "wan", false, "Enable opt-in WAN replication (protocol A via a per-resource sds-proxy pair)")
	cmd.Flags().StringVar(&drNode, "dr-node", "", "DR-site node name (requires --wan; must be a registered node)")
	cmd.Flags().StringVar(&drEndpoint, "dr-endpoint", "", "DR site's public WAN address the primary dials (requires --wan)")
	cmd.Flags().Uint32Var(&wanPort, "wan-port", 0, "WAN mTLS port (requires --wan; 0 = auto-pick a random port >3000)")

	_ = cmd.MarkFlagRequired("name")
	_ = cmd.MarkFlagRequired("port")
	_ = cmd.MarkFlagRequired("nodes")
	_ = cmd.MarkFlagRequired("size")

	return cmd
}

func resourceAdopt() *cobra.Command {
	var nodes string
	var port uint32
	var protocol string

	cmd := &cobra.Command{
		Use:   "adopt <name>",
		Short: "Adopt an existing (foreign) DRBD resource into SDS management",
		Long: "Import an already-existing DRBD resource (created outside SDS) into\n" +
			"SDS management by recording its metadata. This never creates or\n" +
			"modifies the DRBD resource or its data — it only reads the live\n" +
			"/etc/drbd.d/<name>.res and records what it finds. Nodes, port and\n" +
			"protocol are auto-discovered from the config when not supplied.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]

			ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
			defer cancel()

			var nodeList []string
			if nodes != "" {
				nodeList = strings.Split(nodes, ",")
			}

			sdsClient, err := newSDSClient()
			if err != nil {
				return fmt.Errorf("failed to connect to controller: %w", err)
			}
			defer sdsClient.Close()

			resp, err := sdsClient.AdoptResource(ctx, name, nodeList, port, protocol)
			if err != nil {
				return fmt.Errorf("failed to adopt resource: %w", err)
			}

			fmt.Printf("Resource '%s' adopted into SDS management\n", name)
			fmt.Printf("  Nodes:    %v\n", resp.Nodes)
			fmt.Printf("  Port:     %d\n", resp.Port)
			fmt.Printf("  Protocol: %s\n", resp.Protocol)
			fmt.Printf("  Volumes:  %d\n", resp.Volumes)
			fmt.Printf("\nThe DRBD resource and its data were not modified.\n")
			fmt.Printf("Next: sds-cli ha create %s\n", name)

			return nil
		},
	}

	cmd.Flags().StringVar(&nodes, "nodes", "", "Node names (comma-separated); auto-discovered from the .res when omitted")
	cmd.Flags().Uint32Var(&port, "port", 0, "DRBD port; auto-discovered from the .res when omitted")
	cmd.Flags().StringVar(&protocol, "protocol", "", "DRBD protocol (A, B, or C); defaults to C when omitted")

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

			sdsClient, err := newSDSClient()
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
			for _, node := range resource.DisklessNodes {
				fmt.Printf("    %s: diskless (quorum tiebreaker)\n", node)
			}
			if resource.QuorumRisk {
				fmt.Printf("  Quorum:   ⚠ 2-node, no tiebreaker — a single node failure suspends I/O\n")
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

			sdsClient, err := newSDSClient()
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

			sdsClient, err := newSDSClient()
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
				line := fmt.Sprintf("%s (port=%d, protocol=%s, nodes=%v)", r.Name, r.Port, r.Protocol, r.Nodes)
				if len(r.DisklessNodes) > 0 {
					line += fmt.Sprintf(" tiebreaker=%v", r.DisklessNodes)
				}
				if r.QuorumRisk {
					line += " ⚠quorum-risk"
				}
				fmt.Println(line)
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

			sdsClient, err := newSDSClient()
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

			sdsClient, err := newSDSClient()
			if err != nil {
				return fmt.Errorf("failed to connect to controller: %w", err)
			}
			defer sdsClient.Close()

			err = sdsClient.RemoveVolume(ctx, resource, volumeID)
			if err != nil {
				return fmt.Errorf("failed to remove volume: %w", err)
			}

			fmt.Printf("Volume %d removed from '%s'\n", volumeID, resource)
			return nil
		},
	}

	return cmd
}

func resourceResizeVolume() *cobra.Command {
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

			sdsClient, err := newSDSClient()
			if err != nil {
				return fmt.Errorf("failed to connect to controller: %w", err)
			}
			defer sdsClient.Close()

			err = sdsClient.ResizeVolume(ctx, resource, volumeID, uint32(sizeGiB))
			if err != nil {
				return fmt.Errorf("failed to resize volume: %w", err)
			}

			fmt.Printf("Volume %d resized to %s\n", volumeID, util.FormatBytes(sizeBytes))
			return nil
		},
	}

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

			sdsClient, err := newSDSClient()
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

// resourceDRFailover promotes a WAN resource's DR node — a MANUAL disaster-recovery
// action. WAN is protocol A (async), so the DR peer can lag: promoting it may lose
// the writes still in the WAN buffer. This is never automatic (auto-promoting a
// possibly-behind secondary risks data loss); the operator invokes it knowingly.
func resourceDRFailover() *cobra.Command {
	var yes bool
	cmd := &cobra.Command{
		Use:   "dr-failover <resource>",
		Short: "Promote a WAN resource's DR node (manual disaster recovery)",
		Long: "Force-promote the DR node of a WAN (async) resource. Use when the primary\n" +
			"site is lost. Because replication is asynchronous, any writes still buffered\n" +
			"in the WAN link at failure time are lost. This action is deliberately manual.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			resource := args[0]

			ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
			defer cancel()

			sdsClient, err := newSDSClient()
			if err != nil {
				return fmt.Errorf("failed to connect to controller: %w", err)
			}
			defer sdsClient.Close()

			status, err := sdsClient.ResourceStatus(ctx, resource)
			if err != nil {
				return fmt.Errorf("failed to get resource status: %w", err)
			}
			if !status.GetWan() {
				return fmt.Errorf("%q is not a WAN resource; use `resource primary` for LAN promotion", resource)
			}
			drNode := status.GetDrNode()
			if drNode == "" {
				return fmt.Errorf("%q has no DR node recorded", resource)
			}

			fmt.Printf("DR failover: promote %q on DR node %q.\n", resource, drNode)
			fmt.Printf("WARNING: WAN replication is asynchronous — writes still in the WAN\n")
			fmt.Printf("buffer at failure time will be LOST.\n")
			if !yes {
				fmt.Printf("Re-run with --yes to proceed.\n")
				return nil
			}

			// Force is required: the DR peer may not be UpToDate relative to a lost
			// primary, and a plain promote would refuse.
			if err := sdsClient.SetPrimary(ctx, resource, drNode, true); err != nil {
				return fmt.Errorf("DR failover failed: %w", err)
			}
			fmt.Printf("Resource %q promoted on DR node %q. Mount its volume(s) and resume service there.\n", resource, drNode)
			return nil
		},
	}
	cmd.Flags().BoolVar(&yes, "yes", false, "Confirm the (lossy) DR failover")
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

			sdsClient, err := newSDSClient()
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

			sdsClient, err := newSDSClient()
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

func resourceStatus() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "status <resource>",
		Short: "Show detailed resource status",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			resource := args[0]

			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()

			sdsClient, err := newSDSClient()
			if err != nil {
				return fmt.Errorf("failed to connect to controller: %w", err)
			}
			defer sdsClient.Close()

			status, err := sdsClient.ResourceStatus(ctx, resource)
			if err != nil {
				return fmt.Errorf("failed to get resource status: %w", err)
			}

			fmt.Printf("Resource Status: %s\n", status.GetName())
			fmt.Printf("  Role:  %s\n", status.GetRole())
			fmt.Printf("  Nodes: %v\n", status.GetNodes())

			volumes := status.GetVolumes()
			if len(volumes) > 0 {
				fmt.Printf("\n  Volumes:\n")
				for _, vol := range volumes {
					fmt.Printf("    %d: %s (%d GB)\n",
						vol.GetVolumeId(), vol.GetDevice(), vol.GetSizeGb())
				}
			}

			if ns := status.GetNodeStates(); len(ns) > 0 {
				fmt.Printf("\n  Node states:\n")
				for node, st := range ns {
					fmt.Printf("    %s: role=%s disk=%s repl=%s\n",
						node, st.GetRole(), st.GetDiskState(), st.GetReplicationState())
				}
			}

			if status.GetWan() {
				fmt.Printf("\n  WAN replication (protocol A / async):\n")
				fmt.Printf("    DR node:     %s\n", status.GetDrNode())
				fmt.Printf("    DR endpoint: %s\n", status.GetDrEndpoint())
				if p := status.GetWanPort(); p != 0 {
					fmt.Printf("    WAN port:    %d\n", p)
				}
				for node, st := range status.GetWanProxy() {
					fmt.Printf("    sds-proxy@%s: %s\n", node, st)
				}
				fmt.Printf("    NOTE: the DR peer can lag (async). Failover is a manual DR action:\n")
				fmt.Printf("          sds-cli resource dr-failover %s\n", status.GetName())
			}

			return nil
		},
	}

	return cmd
}

func resourceMount() *cobra.Command {
	var node string
	var fstype string

	cmd := &cobra.Command{
		Use:   "mount <resource> <volume-id> <mount-path>",
		Short: "Mount a DRBD volume",
		Args:  cobra.ExactArgs(3),
		RunE: func(cmd *cobra.Command, args []string) error {
			resource := args[0]
			var volumeID uint32
			_, err := fmt.Sscanf(args[1], "%d", &volumeID)
			if err != nil {
				return fmt.Errorf("invalid volume ID: %s", args[1])
			}
			mountPath := args[2]

			ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
			defer cancel()

			sdsClient, err := newSDSClient()
			if err != nil {
				return fmt.Errorf("failed to connect to controller: %w", err)
			}
			defer sdsClient.Close()

			err = sdsClient.MountResource(ctx, resource, volumeID, mountPath, node, fstype)
			if err != nil {
				return fmt.Errorf("failed to mount resource: %w", err)
			}

			fmt.Printf("Resource '%s' volume %d mounted at %s\n", resource, volumeID, mountPath)
			return nil
		},
	}

	cmd.Flags().StringVar(&node, "node", "", "Target node (required)")
	cmd.Flags().StringVar(&fstype, "fstype", "ext4", "Filesystem type")
	_ = cmd.MarkFlagRequired("node")

	return cmd
}

func resourceUnmount() *cobra.Command {
	var node string

	cmd := &cobra.Command{
		Use:   "unmount <resource> <volume-id>",
		Short: "Unmount a DRBD volume",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			resource := args[0]
			var volumeID uint32
			_, err := fmt.Sscanf(args[1], "%d", &volumeID)
			if err != nil {
				return fmt.Errorf("invalid volume ID: %s", args[1])
			}

			ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
			defer cancel()

			sdsClient, err := newSDSClient()
			if err != nil {
				return fmt.Errorf("failed to connect to controller: %w", err)
			}
			defer sdsClient.Close()

			err = sdsClient.UnmountResource(ctx, resource, volumeID, node)
			if err != nil {
				return fmt.Errorf("failed to unmount resource: %w", err)
			}

			fmt.Printf("Resource '%s' volume %d unmounted\n", resource, volumeID)
			return nil
		},
	}

	cmd.Flags().StringVar(&node, "node", "", "Target node (required)")
	_ = cmd.MarkFlagRequired("node")

	return cmd
}

func resourcePromote() *cobra.Command {
	var force bool

	cmd := &cobra.Command{
		Use:   "promote <resource> <node>",
		Short: "Promote DRBD resource to primary",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			resource := args[0]
			node := args[1]

			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()

			sdsClient, err := newSDSClient()
			if err != nil {
				return fmt.Errorf("failed to connect to controller: %w", err)
			}
			defer sdsClient.Close()

			err = sdsClient.SetPrimary(ctx, resource, node, force)
			if err != nil {
				return fmt.Errorf("failed to promote resource: %w", err)
			}

			fmt.Printf("Resource '%s' promoted on '%s'\n", resource, node)
			return nil
		},
	}

	cmd.Flags().BoolVar(&force, "force", false, "Force promotion")

	return cmd
}

func resourceDemote() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "demote <resource> <node>",
		Short: "Demote DRBD resource to secondary",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			resource := args[0]
			node := args[1]

			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()

			sdsClient, err := newSDSClient()
			if err != nil {
				return fmt.Errorf("failed to connect to controller: %w", err)
			}
			defer sdsClient.Close()

			err = sdsClient.SetSecondary(ctx, resource, node)
			if err != nil {
				return fmt.Errorf("failed to demote resource: %w", err)
			}

			fmt.Printf("Resource '%s' demoted on '%s'\n", resource, node)
			return nil
		},
	}

	return cmd
}

// resourceSnapshot manages snapshots for DRBD resources
func resourceSnapshot() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "snapshot",
		Short: "Snapshot management for DRBD resources",
	}

	cmd.AddCommand(resourceSnapshotCreate())
	cmd.AddCommand(resourceSnapshotList())
	cmd.AddCommand(resourceSnapshotRestore())
	cmd.AddCommand(resourceSnapshotDelete())
	cmd.AddCommand(resourceSnapshotSchedule())

	return cmd
}

// resourceSnapshotSchedule manages cron-driven snapshot schedules with GFS retention.
func resourceSnapshotSchedule() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "schedule",
		Short: "Scheduled snapshots with GFS retention",
	}
	cmd.AddCommand(resourceSnapshotScheduleCreate())
	cmd.AddCommand(resourceSnapshotScheduleList())
	cmd.AddCommand(resourceSnapshotScheduleDelete())
	return cmd
}

func resourceSnapshotScheduleCreate() *cobra.Command {
	var resource, cronExpr string
	var hourly, daily, weekly, monthly, yearly int
	var disabled bool

	cmd := &cobra.Command{
		Use:   "create",
		Short: "Create or replace a snapshot schedule for a resource",
		RunE: func(cmd *cobra.Command, args []string) error {
			if resource == "" {
				return fmt.Errorf("--resource is required")
			}
			if cronExpr == "" {
				return fmt.Errorf("--cron is required (standard 5-field cron, e.g. \"0 * * * *\")")
			}

			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			sdsClient, err := newSDSClient()
			if err != nil {
				return fmt.Errorf("failed to connect to controller: %w", err)
			}
			defer sdsClient.Close()

			keep := &sdspb.GFSRetention{
				Hourly:  int32(hourly),
				Daily:   int32(daily),
				Weekly:  int32(weekly),
				Monthly: int32(monthly),
				Yearly:  int32(yearly),
			}
			if err := sdsClient.CreateSnapshotSchedule(ctx, resource, cronExpr, keep, !disabled); err != nil {
				return fmt.Errorf("failed to create snapshot schedule: %w", err)
			}
			fmt.Printf("Snapshot schedule for %q created (cron=%q)\n", resource, cronExpr)
			return nil
		},
	}
	cmd.Flags().StringVar(&resource, "resource", "", "Target resource (required)")
	cmd.Flags().StringVar(&cronExpr, "cron", "", "Standard 5-field cron expression (required)")
	cmd.Flags().IntVar(&hourly, "keep-hourly", 0, "Hourly snapshots to retain")
	cmd.Flags().IntVar(&daily, "keep-daily", 0, "Daily snapshots to retain")
	cmd.Flags().IntVar(&weekly, "keep-weekly", 0, "Weekly snapshots to retain")
	cmd.Flags().IntVar(&monthly, "keep-monthly", 0, "Monthly snapshots to retain")
	cmd.Flags().IntVar(&yearly, "keep-yearly", 0, "Yearly snapshots to retain")
	cmd.Flags().BoolVar(&disabled, "disabled", false, "Create the schedule disabled")
	return cmd
}

func resourceSnapshotScheduleList() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List snapshot schedules",
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			sdsClient, err := newSDSClient()
			if err != nil {
				return fmt.Errorf("failed to connect to controller: %w", err)
			}
			defer sdsClient.Close()

			schedules, err := sdsClient.ListSnapshotSchedules(ctx)
			if err != nil {
				return fmt.Errorf("failed to list snapshot schedules: %w", err)
			}
			if len(schedules) == 0 {
				fmt.Println("No snapshot schedules found")
				return nil
			}
			for _, s := range schedules {
				state := "enabled"
				if !s.Enabled {
					state = "disabled"
				}
				k := s.Keep
				fmt.Printf("%s (resource=%s, cron=%q, %s)\n", s.Name, s.Resource, s.Cron, state)
				fmt.Printf("  keep: hourly=%d daily=%d weekly=%d monthly=%d yearly=%d\n",
					k.GetHourly(), k.GetDaily(), k.GetWeekly(), k.GetMonthly(), k.GetYearly())
				if s.LastRun != "" {
					fmt.Printf("  last run: %s\n", s.LastRun)
				}
				if s.NextRun != "" {
					fmt.Printf("  next run: %s\n", s.NextRun)
				}
			}
			return nil
		},
	}
	return cmd
}

func resourceSnapshotScheduleDelete() *cobra.Command {
	var name string
	cmd := &cobra.Command{
		Use:   "delete",
		Short: "Delete a snapshot schedule (existing snapshots are kept)",
		RunE: func(cmd *cobra.Command, args []string) error {
			if name == "" {
				return fmt.Errorf("--name is required (the resource name)")
			}
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			sdsClient, err := newSDSClient()
			if err != nil {
				return fmt.Errorf("failed to connect to controller: %w", err)
			}
			defer sdsClient.Close()

			if err := sdsClient.DeleteSnapshotSchedule(ctx, name); err != nil {
				return fmt.Errorf("failed to delete snapshot schedule: %w", err)
			}
			fmt.Printf("Snapshot schedule %q deleted\n", name)
			return nil
		},
	}
	cmd.Flags().StringVar(&name, "name", "", "Schedule name (= resource name) (required)")
	return cmd
}

func resourceSnapshotDelete() *cobra.Command {
	var resource string
	var snapshotName string
	var node string
	var storageType string
	var pool string

	cmd := &cobra.Command{
		Use:   "delete",
		Short: "Delete a snapshot",
		RunE: func(cmd *cobra.Command, args []string) error {
			if resource == "" {
				return fmt.Errorf("resource name is required")
			}
			if snapshotName == "" {
				return fmt.Errorf("snapshot name is required")
			}
			if node == "" {
				return fmt.Errorf("node is required")
			}
			if pool == "" {
				pool = "data-pool"
			}

			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()

			sdsClient, err := newSDSClient()
			if err != nil {
				return fmt.Errorf("failed to connect to controller: %w", err)
			}
			defer sdsClient.Close()

			if storageType == "zfs" {
				// ZFS snapshot
				// ZFS snapshot names are unique per dataset, usually passed as snapshot name only to destroy?
				// But DeleteZFSSnapshot in backend takes 'snapshot' arg.
				// Is it "snap1" or "pool/dataset@snap1"?
				// pkg/deployment/deployment.go ZFSDestroySnapshot: "sudo zfs destroy %s"
				// So it needs FULL path.
				snapshotPath := fmt.Sprintf("%s/%s_data@%s", pool, resource, snapshotName)
				err = sdsClient.DeleteZFSSnapshot(ctx, snapshotPath, node)
				if err != nil {
					return fmt.Errorf("failed to delete ZFS snapshot: %w", err)
				}
				fmt.Printf("ZFS snapshot '%s' deleted on node '%s'\n", snapshotName, node)
			} else {
				// LVM snapshot
				// Pass pool as VG name
				err = sdsClient.DeleteLvmSnapshot(ctx, pool, snapshotName, node)
				if err != nil {
					return fmt.Errorf("failed to delete LVM snapshot: %w", err)
				}
				fmt.Printf("LVM snapshot '%s' deleted on node '%s'\n", snapshotName, node)
			}

			return nil
		},
	}

	cmd.Flags().StringVar(&resource, "resource", "", "DRBD resource name")
	cmd.Flags().StringVar(&snapshotName, "name", "", "Snapshot name")
	cmd.Flags().StringVar(&node, "node", "", "Node where resource exists")
	cmd.Flags().StringVar(&storageType, "storage-type", "lvm", "Storage type: lvm or zfs")
	cmd.Flags().StringVar(&pool, "pool", "data-pool", "Storage pool name")

	_ = cmd.MarkFlagRequired("resource")
	_ = cmd.MarkFlagRequired("name")
	_ = cmd.MarkFlagRequired("node")

	return cmd
}

func resourceSnapshotCreate() *cobra.Command {
	var resource string
	var snapshotName string
	var node string
	var size string
	var storageType string
	var pool string

	cmd := &cobra.Command{
		Use:   "create",
		Short: "Create a snapshot of DRBD resource",
		RunE: func(cmd *cobra.Command, args []string) error {
			if resource == "" {
				return fmt.Errorf("resource name is required")
			}
			if snapshotName == "" {
				return fmt.Errorf("snapshot name is required")
			}
			if node == "" {
				return fmt.Errorf("node is required")
			}
			if pool == "" {
				pool = "data-pool"
			}

			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()

			sdsClient, err := newSDSClient()
			if err != nil {
				return fmt.Errorf("failed to connect to controller: %w", err)
			}
			defer sdsClient.Close()

			if storageType == "zfs" {
				// ZFS snapshot: pool/resource_data@snapshot
				dataset := fmt.Sprintf("%s/%s_data", pool, resource)
				err = sdsClient.CreateZFSSnapshot(ctx, dataset, snapshotName, node)
				if err != nil {
					return fmt.Errorf("failed to create ZFS snapshot: %w", err)
				}
				fmt.Printf("ZFS snapshot '%s' created for resource '%s' on node '%s'\n", snapshotName, resource, node)
			} else {
				// LVM snapshot (default)
				if size == "" {
					size = "1G"
				}
				lvName := fmt.Sprintf("%s_data", resource)
				// Pass pool as the VG name (first argument)
				err = sdsClient.CreateLvmSnapshot(ctx, pool, lvName, snapshotName, node, size)
				if err != nil {
					return fmt.Errorf("failed to create LVM snapshot: %w", err)
				}
				fmt.Printf("LVM snapshot '%s' created for resource '%s' on node '%s'\n", snapshotName, resource, node)
			}

			return nil
		},
	}

	cmd.Flags().StringVar(&resource, "resource", "", "DRBD resource name")
	cmd.Flags().StringVar(&snapshotName, "name", "", "Snapshot name")
	cmd.Flags().StringVar(&node, "node", "", "Node where resource exists")
	cmd.Flags().StringVar(&size, "size", "1G", "Snapshot size for LVM (e.g., 1G)")
	cmd.Flags().StringVar(&storageType, "storage-type", "lvm", "Storage type: lvm or zfs")
	cmd.Flags().StringVar(&pool, "pool", "data-pool", "Storage pool name")

	_ = cmd.MarkFlagRequired("resource")
	_ = cmd.MarkFlagRequired("name")
	_ = cmd.MarkFlagRequired("node")

	return cmd
}

func resourceSnapshotList() *cobra.Command {
	var resource string
	var node string
	var storageType string
	var pool string

	cmd := &cobra.Command{
		Use:   "list",
		Short: "List snapshots for DRBD resource",
		RunE: func(cmd *cobra.Command, args []string) error {
			if resource == "" {
				return fmt.Errorf("resource name is required")
			}
			if node == "" {
				return fmt.Errorf("node is required")
			}
			if pool == "" {
				pool = "data-pool"
			}

			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()

			sdsClient, err := newSDSClient()
			if err != nil {
				return fmt.Errorf("failed to connect to controller: %w", err)
			}
			defer sdsClient.Close()

			if storageType == "zfs" {
				// ZFS snapshots
				dataset := fmt.Sprintf("%s/%s_data", pool, resource)
				snapshots, err := sdsClient.ListZFSSnapshots(ctx, dataset, node)
				if err != nil {
					return fmt.Errorf("failed to list ZFS snapshots: %w", err)
				}

				if len(snapshots) == 0 {
					fmt.Printf("No ZFS snapshots found for resource '%s'\n", resource)
					return nil
				}

				fmt.Printf("ZFS snapshots for resource '%s':\n", resource)
				for _, snap := range snapshots {
					fmt.Printf("  - %s (created: %s)\n", snap.Name, snap.CreatedAt)
				}
			} else {
				// LVM snapshots
				// Pass pool as VG name
				snapshots, err := sdsClient.ListLvmSnapshots(ctx, pool, node)
				if err != nil {
					return fmt.Errorf("failed to list LVM snapshots: %w", err)
				}

				if len(snapshots) == 0 {
					fmt.Printf("No LVM snapshots found for resource '%s'\n", resource)
					return nil
				}

				fmt.Printf("LVM snapshots for resource '%s':\n", resource)
				fmt.Println("  Name                    Size")
				fmt.Println("  ----------------------- ----")
				for _, snap := range snapshots {
					fmt.Printf("  %-23s %d GB\n", snap.Name, snap.SizeGb)
				}
			}

			return nil
		},
	}

	cmd.Flags().StringVar(&resource, "resource", "", "DRBD resource name")
	cmd.Flags().StringVar(&node, "node", "", "Node where resource exists")
	cmd.Flags().StringVar(&storageType, "storage-type", "lvm", "Storage type: lvm or zfs")
	cmd.Flags().StringVar(&pool, "pool", "data-pool", "Storage pool name")

	_ = cmd.MarkFlagRequired("resource")
	_ = cmd.MarkFlagRequired("node")

	return cmd
}

func resourceSnapshotRestore() *cobra.Command {
	var resource string
	var snapshotName string
	var node string
	var storageType string
	var pool string

	cmd := &cobra.Command{
		Use:   "restore",
		Short: "Restore DRBD resource from snapshot",
		RunE: func(cmd *cobra.Command, args []string) error {
			if resource == "" {
				return fmt.Errorf("resource name is required")
			}
			if snapshotName == "" {
				return fmt.Errorf("snapshot name is required")
			}
			if node == "" {
				return fmt.Errorf("node is required")
			}
			if pool == "" {
				pool = "data-pool"
			}

			ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
			defer cancel()

			sdsClient, err := newSDSClient()
			if err != nil {
				return fmt.Errorf("failed to connect to controller: %w", err)
			}
			defer sdsClient.Close()

			if storageType == "zfs" {
				// ZFS rollback
				dataset := fmt.Sprintf("%s/%s_data", pool, resource)
				err = sdsClient.RestoreZFSSnapshot(ctx, dataset, snapshotName, node)
				if err != nil {
					return fmt.Errorf("failed to restore ZFS snapshot: %w", err)
				}
				fmt.Printf("ZFS snapshot '%s' restored for resource '%s' on node '%s'\n", snapshotName, resource, node)
			} else {
				// LVM snapshot restore (merge)
				// Pass pool as VG name
				err = sdsClient.RestoreLvmSnapshot(ctx, pool, snapshotName, node)
				if err != nil {
					return fmt.Errorf("failed to restore LVM snapshot: %w", err)
				}
				fmt.Printf("LVM snapshot '%s' restored for resource '%s' on node '%s'\n", snapshotName, resource, node)
				fmt.Println("Note: The snapshot has been merged back into the original volume.")
			}

			return nil
		},
	}

	cmd.Flags().StringVar(&resource, "resource", "", "DRBD resource name")
	cmd.Flags().StringVar(&snapshotName, "name", "", "Snapshot name")
	cmd.Flags().StringVar(&node, "node", "", "Node where resource exists")
	cmd.Flags().StringVar(&storageType, "storage-type", "lvm", "Storage type: lvm or zfs")
	cmd.Flags().StringVar(&pool, "pool", "data-pool", "Storage pool name")

	_ = cmd.MarkFlagRequired("resource")
	_ = cmd.MarkFlagRequired("name")
	_ = cmd.MarkFlagRequired("node")

	return cmd
}
