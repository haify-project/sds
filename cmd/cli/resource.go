package main

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	sdspb "github.com/liliang-cn/sds/api/proto/v1"
	"github.com/liliang-cn/sds/pkg/client"
	"github.com/liliang-cn/sds/pkg/util"
	"github.com/spf13/cobra"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
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
	cmd.AddCommand(resourceDualPrimary())
	cmd.AddCommand(resourceFs())
	cmd.AddCommand(resourceStatus())
	cmd.AddCommand(resourceMount())
	cmd.AddCommand(resourceUnmount())
	cmd.AddCommand(resourcePromote())
	cmd.AddCommand(resourceDemote())
	cmd.AddCommand(resourceDiskless())
	cmd.AddCommand(resourceSnapshot())
	cmd.AddCommand(resourceProfileCommand())

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
	var replicas uint32
	var replicasOnDifferent []string
	var replicasOnSame []string
	var doNotPlaceWith []string
	var pool string
	var storageType string
	var protocol string
	var size string
	var drbdOptions map[string]string
	var wan bool
	var drNode string
	var drEndpoint string
	var wanPort uint32
	var profile string
	var labels map[string]string

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
			}
			// No --nodes ⇒ auto-placement: the controller picks the nodes with the
			// most free space in --pool. WAN needs an explicit primary, so require
			// --nodes there.
			if len(nodeList) == 0 && wan {
				return fmt.Errorf("WAN resource requires an explicit --nodes primary")
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

			if pool == "" && profile == "" {
				pool = "data-pool"
			}

			sizeBytes, err := util.ParseSize(size)
			if err != nil {
				return fmt.Errorf("invalid size format: %s: %w", size, err)
			}
			sizeGiB := util.BytesToGiB(sizeBytes)
			if sizeGiB == 0 {
				return fmt.Errorf("size too small (minimum 1 GiB)")
			}

			grpcClient, conn, err := newResourceGRPCClient()
			if err != nil {
				return fmt.Errorf("failed to connect to controller: %w", err)
			}
			defer conn.Close()

			requestReplicas := replicas
			if profile != "" && !cmd.Flags().Changed("replicas") {
				requestReplicas = 0
			}
			requestPool := pool
			requestStorageType := storageType
			requestProtocol := protocol
			if profile != "" {
				if !cmd.Flags().Changed("pool") {
					requestPool = ""
				}
				if !cmd.Flags().Changed("storage-type") {
					requestStorageType = ""
				}
				if !cmd.Flags().Changed("protocol") {
					requestProtocol = ""
				}
			}
			if wan {
				requestProtocol = "A"
			}
			resp, err := grpcClient.CreateResource(ctx, &sdspb.CreateResourceRequest{
				Name:                name,
				Port:                port,
				Nodes:               nodeList,
				Protocol:            requestProtocol,
				SizeGb:              uint32(sizeGiB),
				Pool:                requestPool,
				StorageType:         requestStorageType,
				DrbdOptions:         drbdOptions,
				Wan:                 wan,
				DrNode:              drNode,
				DrEndpoint:          drEndpoint,
				WanPort:             wanPort,
				Replicas:            requestReplicas,
				ReplicasOnDifferent: replicasOnDifferent,
				ReplicasOnSame:      replicasOnSame,
				DoNotPlaceWith:      doNotPlaceWith,
				Labels:              labels,
				Profile:             profile,
			})
			if err != nil {
				return fmt.Errorf("failed to create resource: %w", err)
			}
			if !resp.Success {
				return fmt.Errorf("failed to create resource: %s", resp.Message)
			}

			fmt.Printf("Resource created successfully\n")
			fmt.Printf("  Name:        %s\n", name)
			fmt.Printf("  Port:        %d\n", port)
			fmt.Printf("  Storage:     %s\n", profileCreateValue(requestStorageType, profile))
			fmt.Printf("  Pool:        %s\n", profileCreateValue(requestPool, profile))
			if len(nodeList) == 0 {
				if requestReplicas == 0 {
					fmt.Printf("  Nodes:       auto-placed (replicas from profile; see 'resource get %s')\n", name)
				} else {
					fmt.Printf("  Nodes:       auto-placed (%d replicas by free space; see 'resource get %s')\n", requestReplicas, name)
				}
			} else {
				fmt.Printf("  Nodes:       %v\n", nodeList)
			}
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
				fmt.Printf("  Protocol:    %s\n", profileCreateValue(requestProtocol, profile))
			}
			fmt.Printf("  Size:        %d GiB (%s)\n", sizeGiB, util.FormatBytes(sizeBytes))
			if len(drbdOptions) > 0 {
				fmt.Printf("  Options:     %v\n", drbdOptions)
			}
			if profile != "" {
				fmt.Printf("  Profile:     %s\n", profile)
			}
			if len(labels) > 0 {
				fmt.Printf("  Labels:      %s\n", formatLabels(labels))
			}
			fmt.Printf("\nNext steps:\n")
			fmt.Printf("  1. sds-cli resource get %s\n", name)
			fmt.Printf("  2. sds-cli resource primary %s <node>\n", name)

			return nil
		},
	}

	cmd.Flags().StringVar(&name, "name", "", "Resource name (required)")
	cmd.Flags().Uint32Var(&port, "port", 0, "DRBD port (required)")
	cmd.Flags().StringVar(&nodes, "nodes", "", "Node names (comma-separated); omit to auto-place by free space")
	cmd.Flags().Uint32Var(&replicas, "replicas", 2, "Replica count for auto-placement (used only when --nodes is omitted)")
	cmd.Flags().StringSliceVar(&replicasOnDifferent, "replicas-on-different", nil, "Node-label key(s) to spread replicas across (e.g. rack); each replica gets a distinct value. Repeatable. Auto-placement only")
	cmd.Flags().StringSliceVar(&replicasOnSame, "replicas-on-same", nil, "Node-label key(s) all replicas must share (e.g. zone). Repeatable. Auto-placement only")
	cmd.Flags().StringSliceVar(&doNotPlaceWith, "do-not-place-with", nil, "Resource name(s) whose nodes to avoid (anti-affinity). Repeatable. Auto-placement only")
	cmd.Flags().StringVar(&pool, "pool", "", "Storage pool name (default: data-pool)")
	cmd.Flags().StringVar(&storageType, "storage-type", "lvm", "Storage type: lvm, lvm-thin, or zfs")
	cmd.Flags().StringVar(&protocol, "protocol", "C", "DRBD protocol (A, B, or C)")
	cmd.Flags().StringVar(&size, "size", "", "Volume size (e.g., 1G, 10GB, 1TB, 1GiB, required)")
	cmd.Flags().StringToStringVar(&drbdOptions, "drbd-options", nil, "DRBD options as key=value pairs (e.g., on-no-quorum=suspend-io)")
	cmd.Flags().StringVar(&profile, "profile", "", "Resource profile name")
	cmd.Flags().StringToStringVar(&labels, "label", nil, "Resource label as key=value (repeatable)")
	cmd.Flags().BoolVar(&wan, "wan", false, "Enable opt-in WAN replication (protocol A via a per-resource sds-proxy pair)")
	cmd.Flags().StringVar(&drNode, "dr-node", "", "DR-site node name (requires --wan; must be a registered node)")
	cmd.Flags().StringVar(&drEndpoint, "dr-endpoint", "", "DR site's public WAN address the primary dials (requires --wan)")
	cmd.Flags().Uint32Var(&wanPort, "wan-port", 0, "WAN mTLS port (requires --wan; 0 = auto-pick a random port >3000)")

	_ = cmd.MarkFlagRequired("name")
	_ = cmd.MarkFlagRequired("port")
	// --nodes is intentionally NOT required: omitting it triggers auto-placement.
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
			fmt.Printf("  Profile:  %s\n", displayValue(resource.Profile))
			fmt.Printf("  Labels:   %s\n", formatLabels(resource.Labels))
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
			for _, node := range resource.DisklessClients {
				fmt.Printf("    %s: diskless (data client)\n", node)
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
				line += fmt.Sprintf(" profile=%s labels=%s", displayValue(r.Profile), formatLabels(r.Labels))
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

func newResourceGRPCClient() (sdspb.SDSControllerClient, *grpc.ClientConn, error) {
	dialOpts := []grpc.DialOption{grpc.WithTransportCredentials(insecure.NewCredentials())}
	if token := client.ResolveToken(tokenFlag); token != "" {
		dialOpts = append(dialOpts, grpc.WithUnaryInterceptor(func(ctx context.Context, method string, req, reply any, cc *grpc.ClientConn, invoker grpc.UnaryInvoker, opts ...grpc.CallOption) error {
			ctx = metadata.AppendToOutgoingContext(ctx, "authorization", "Bearer "+token)
			return invoker(ctx, method, req, reply, cc, opts...)
		}))
	}
	conn, err := grpc.NewClient(controllerAddr, dialOpts...)
	if err != nil {
		return nil, nil, err
	}
	return sdspb.NewSDSControllerClient(conn), conn, nil
}

func displayValue(value string) string {
	if value == "" {
		return "(none)"
	}
	return value
}

func profileCreateValue(value, profile string) string {
	if value == "" && profile != "" {
		return "from profile " + profile
	}
	return displayValue(value)
}

func resourceProfileCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "profile",
		Short: "Resource profile management",
	}
	cmd.AddCommand(resourceProfileCreate())
	cmd.AddCommand(resourceProfileGet())
	cmd.AddCommand(resourceProfileList())
	cmd.AddCommand(resourceProfileDelete())
	return cmd
}

func resourceProfileCreate() *cobra.Command {
	profile := &sdspb.ResourceProfile{}
	cmd := &cobra.Command{
		Use:   "create",
		Short: "Create or replace a resource profile",
		RunE: func(cmd *cobra.Command, args []string) error {
			if strings.TrimSpace(profile.Name) == "" {
				return fmt.Errorf("profile name is required")
			}
			grpcClient, conn, err := newResourceGRPCClient()
			if err != nil {
				return fmt.Errorf("failed to connect to controller: %w", err)
			}
			defer conn.Close()
			resp, err := grpcClient.CreateResourceProfile(cmd.Context(), &sdspb.CreateResourceProfileRequest{Profile: profile})
			if err != nil {
				return fmt.Errorf("failed to create resource profile: %w", err)
			}
			if !resp.Success {
				return fmt.Errorf("failed to create resource profile: %s", resp.Message)
			}
			fmt.Printf("Resource profile '%s' saved\n", profile.Name)
			printResourceProfile(resp.Profile)
			return nil
		},
	}
	cmd.Flags().StringVar(&profile.Name, "name", "", "Profile name (required)")
	cmd.Flags().StringVar(&profile.Protocol, "protocol", "", "Default DRBD protocol (A, B, or C)")
	cmd.Flags().StringVar(&profile.StorageType, "storage-type", "", "Default storage type: lvm, lvm-thin, or zfs")
	cmd.Flags().StringVar(&profile.Pool, "pool", "", "Default storage pool")
	cmd.Flags().Uint32Var(&profile.Replicas, "replicas", 0, "Default replica count")
	cmd.Flags().StringSliceVar(&profile.ReplicasOnDifferent, "replicas-on-different", nil, "Node-label key(s) to spread replicas across (repeatable)")
	cmd.Flags().StringSliceVar(&profile.ReplicasOnSame, "replicas-on-same", nil, "Node-label key(s) all replicas must share (repeatable)")
	cmd.Flags().StringToStringVar(&profile.DrbdOptions, "drbd-options", nil, "Default DRBD options as key=value pairs")
	cmd.Flags().StringToStringVar(&profile.Labels, "label", nil, "Default resource label as key=value (repeatable)")
	_ = cmd.MarkFlagRequired("name")
	return cmd
}

func resourceProfileGet() *cobra.Command {
	return &cobra.Command{
		Use:   "get <name>",
		Short: "Get a resource profile",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			grpcClient, conn, err := newResourceGRPCClient()
			if err != nil {
				return fmt.Errorf("failed to connect to controller: %w", err)
			}
			defer conn.Close()
			resp, err := grpcClient.GetResourceProfile(cmd.Context(), &sdspb.GetResourceProfileRequest{Name: args[0]})
			if err != nil {
				return fmt.Errorf("failed to get resource profile: %w", err)
			}
			if !resp.Success {
				return fmt.Errorf("failed to get resource profile: %s", resp.Message)
			}
			printResourceProfile(resp.Profile)
			return nil
		},
	}
}

func resourceProfileList() *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List resource profiles",
		RunE: func(cmd *cobra.Command, args []string) error {
			grpcClient, conn, err := newResourceGRPCClient()
			if err != nil {
				return fmt.Errorf("failed to connect to controller: %w", err)
			}
			defer conn.Close()
			resp, err := grpcClient.ListResourceProfiles(cmd.Context(), &sdspb.ListResourceProfilesRequest{})
			if err != nil {
				return fmt.Errorf("failed to list resource profiles: %w", err)
			}
			if !resp.Success {
				return fmt.Errorf("failed to list resource profiles: %s", resp.Message)
			}
			if len(resp.Profiles) == 0 {
				fmt.Println("No resource profiles found")
				return nil
			}
			sort.Slice(resp.Profiles, func(i, j int) bool { return resp.Profiles[i].Name < resp.Profiles[j].Name })
			for _, profile := range resp.Profiles {
				fmt.Println(formatResourceProfile(profile))
			}
			return nil
		},
	}
}

func resourceProfileDelete() *cobra.Command {
	return &cobra.Command{
		Use:   "delete <name>",
		Short: "Delete a resource profile",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			grpcClient, conn, err := newResourceGRPCClient()
			if err != nil {
				return fmt.Errorf("failed to connect to controller: %w", err)
			}
			defer conn.Close()
			resp, err := grpcClient.DeleteResourceProfile(cmd.Context(), &sdspb.DeleteResourceProfileRequest{Name: args[0]})
			if err != nil {
				return fmt.Errorf("failed to delete resource profile: %w", err)
			}
			if !resp.Success {
				return fmt.Errorf("failed to delete resource profile: %s", resp.Message)
			}
			fmt.Printf("Resource profile '%s' deleted successfully\n", args[0])
			return nil
		},
	}
}

func printResourceProfile(profile *sdspb.ResourceProfile) {
	if profile == nil {
		return
	}
	fmt.Printf("Profile: %s\n", profile.Name)
	fmt.Printf("  Protocol:              %s\n", displayValue(profile.Protocol))
	fmt.Printf("  Storage type:          %s\n", displayValue(profile.StorageType))
	fmt.Printf("  Pool:                  %s\n", displayValue(profile.Pool))
	fmt.Printf("  Replicas:              %d\n", profile.Replicas)
	fmt.Printf("  Replicas on different: %s\n", formatStringSlice(profile.ReplicasOnDifferent))
	fmt.Printf("  Replicas on same:      %s\n", formatStringSlice(profile.ReplicasOnSame))
	fmt.Printf("  DRBD options:          %s\n", formatLabels(profile.DrbdOptions))
	fmt.Printf("  Labels:                %s\n", formatLabels(profile.Labels))
}

func formatResourceProfile(profile *sdspb.ResourceProfile) string {
	if profile == nil {
		return "(invalid profile)"
	}
	return fmt.Sprintf("%s (protocol=%s, storage-type=%s, pool=%s, replicas=%d, replicas-on-different=%s, replicas-on-same=%s, drbd-options=%s, labels=%s)",
		profile.Name, displayValue(profile.Protocol), displayValue(profile.StorageType), displayValue(profile.Pool), profile.Replicas,
		formatStringSlice(profile.ReplicasOnDifferent), formatStringSlice(profile.ReplicasOnSame), formatLabels(profile.DrbdOptions), formatLabels(profile.Labels))
}

func formatStringSlice(values []string) string {
	if len(values) == 0 {
		return "(none)"
	}
	result := append([]string(nil), values...)
	sort.Strings(result)
	return strings.Join(result, ", ")
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

// printWANMetrics renders the proxy counters under a WAN resource's status.
//
// The headline is the un-replicated backlog. Under protocol A those are writes
// the primary already acknowledged that the DR site has not seen, so it is the
// amount a DR failover would lose — the question status could not answer before
// the proxy published these. When the snapshot is absent we say so rather than
// print zeros, because a confident "0 lost" would be the worst possible lie here.
func printWANMetrics(m *sdspb.WANMetrics) {
	if m == nil {
		fmt.Printf("    Replication lag: unknown (proxy published no metrics)\n")
		return
	}

	fmt.Printf("    Un-replicated:   %s", humanBytes(m.GetBufferUsedBytes()))
	if cap := m.GetBufferCapBytes(); cap > 0 {
		fmt.Printf(" of %s buffer (%.1f%%)", humanBytes(cap), m.GetBufferFillPercent())
	}
	fmt.Printf("\n")
	if m.GetBufferUsedBytes() > 0 {
		fmt.Printf("      ⚠ a DR failover right now would lose up to this much\n")
	}

	fmt.Printf("    Replicated:      %s sent as %s on the wire",
		humanBytes(m.GetDrbdToWanBytes()), humanBytes(m.GetWanWireBytes()))
	if r := m.GetCompressionRatio(); r > 0 {
		fmt.Printf(" (%.2fx compression)", r)
	}
	fmt.Printf("\n")

	// Only worth the operator's attention when non-zero.
	if n := m.GetReconnects(); n > 0 {
		fmt.Printf("    WAN reconnects:  %d (a climbing count means a flapping link)\n", n)
	}
	if n := m.GetRingFullEvents(); n > 0 {
		fmt.Printf("    Buffer full:     %d times (the WAN could not keep up; DRBD went Ahead)\n", n)
	}
}

// humanBytes renders a byte count for operator eyes rather than exact accounting.
func humanBytes(b uint64) string {
	const unit = 1024
	if b < unit {
		return fmt.Sprintf("%d B", b)
	}
	units := []string{"KiB", "MiB", "GiB", "TiB", "PiB"}
	v := float64(b)
	for _, u := range units {
		v /= unit
		if v < unit {
			return fmt.Sprintf("%.1f %s", v, u)
		}
	}
	return fmt.Sprintf("%.1f EiB", v/unit)
}

// resourceDualPrimary exposes the allow-two-primaries toggle used to bracket a
// hypervisor live migration. It is primarily driven by the Proxmox storage
// plugin; the CLI form exists for operators to inspect/repair a stranded window.
func resourceDualPrimary() *cobra.Command {
	return &cobra.Command{
		Use:   "dual-primary <resource> on|off",
		Short: "Open or close a dual-primary window (live migration only)",
		Long: "Toggle DRBD's allow-two-primaries on a resource.\n\n" +
			"This exists so a hypervisor can live-migrate a guest: source and target both\n" +
			"hold the disk open during the hand-off. It is NOT a way to use one volume from\n" +
			"two machines at once — an ordinary filesystem mounted twice will corrupt.\n\n" +
			"WAN resources are refused (their replication is asynchronous). The toggle is\n" +
			"runtime-only, so a reboot or `drbdadm adjust` restores single-primary anyway.\n" +
			"`off` is idempotent and verifies that no node is left dual-primary.",
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			resource := args[0]
			var enable bool
			switch args[1] {
			case "on":
				enable = true
			case "off":
				enable = false
			default:
				return fmt.Errorf("invalid state %q (use on or off)", args[1])
			}

			ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
			defer cancel()

			sdsClient, err := newSDSClient()
			if err != nil {
				return fmt.Errorf("failed to connect to controller: %w", err)
			}
			defer sdsClient.Close()

			if err := sdsClient.SetDualPrimary(ctx, resource, enable); err != nil {
				return fmt.Errorf("failed to set dual-primary: %w", err)
			}

			if enable {
				fmt.Printf("Dual-primary window OPEN on '%s'.\n", resource)
				fmt.Printf("Close it as soon as the migration finishes: sds-cli resource dual-primary %s off\n", resource)
			} else {
				fmt.Printf("Dual-primary window closed on '%s'.\n", resource)
			}
			return nil
		},
	}
}

// resourceDiskless groups attach/detach of diskless data clients — nodes that
// mount a resource with no local replica, accessing it over the DRBD network.
func resourceDiskless() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "diskless",
		Short: "Manage diskless data clients (mount a resource with no local replica)",
	}
	cmd.AddCommand(resourceDisklessAttach())
	cmd.AddCommand(resourceDisklessDetach())
	return cmd
}

func resourceDisklessAttach() *cobra.Command {
	return &cobra.Command{
		Use:   "attach <resource> <node>",
		Short: "Attach a node as a diskless client so it can mount the resource over the network",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			resource, node := args[0], args[1]

			ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
			defer cancel()

			sdsClient, err := newSDSClient()
			if err != nil {
				return fmt.Errorf("failed to connect to controller: %w", err)
			}
			defer sdsClient.Close()

			if err := sdsClient.AttachDisklessClient(ctx, resource, node); err != nil {
				return fmt.Errorf("failed to attach diskless client: %w", err)
			}
			fmt.Printf("Node '%s' attached to '%s' as a diskless client\n", node, resource)
			return nil
		},
	}
}

func resourceDisklessDetach() *cobra.Command {
	return &cobra.Command{
		Use:   "detach <resource> <node>",
		Short: "Detach a diskless client from the resource",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			resource, node := args[0], args[1]

			ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
			defer cancel()

			sdsClient, err := newSDSClient()
			if err != nil {
				return fmt.Errorf("failed to connect to controller: %w", err)
			}
			defer sdsClient.Close()

			if err := sdsClient.DetachDisklessClient(ctx, resource, node); err != nil {
				return fmt.Errorf("failed to detach diskless client: %w", err)
			}
			fmt.Printf("Node '%s' detached from '%s'\n", node, resource)
			return nil
		},
	}
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
				reach := "unreachable ⚠"
				if status.GetWanReachable() {
					reach = "reachable"
				}
				fmt.Printf("    DR link:     %s (primary → %s)\n", reach, status.GetDrEndpoint())
				for node, st := range status.GetWanProxy() {
					fmt.Printf("    sds-proxy@%s: %s\n", node, st)
				}
				printWANMetrics(status.GetWanMetrics())
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
