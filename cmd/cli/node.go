package main

import (
	"fmt"
	"os"
	"sort"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"
)

func nodeCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "node",
		Short: "Node management (storage nodes)",
	}

	cmd.AddCommand(nodeList())
	cmd.AddCommand(nodeGet())
	cmd.AddCommand(nodeRegister())
	cmd.AddCommand(nodeUnregister())
	cmd.AddCommand(nodeLabel())

	return cmd
}

func nodeList() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List all nodes in the cluster",
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()

			// Create SDS client
			sdsClient, err := newSDSClient()
			if err != nil {
				return fmt.Errorf("failed to connect to controller: %w", err)
			}
			defer sdsClient.Close()

			// List nodes
			nodes, err := sdsClient.ListNodes(ctx)
			if err != nil {
				return fmt.Errorf("failed to list nodes: %w", err)
			}

			if len(nodes) == 0 {
				fmt.Println("No nodes registered")
				return nil
			}

			// Print nodes in table format
			w := tabwriter.NewWriter(os.Stdout, 0, 0, 3, ' ', 0)
			fmt.Fprintln(w, "NAME\tADDRESS\tSTATE\tVERSION")

			for _, node := range nodes {
				// Strip port from address for display
				displayAddr := node.Address
				if idx := strings.LastIndex(node.Address, ":"); idx != -1 {
					displayAddr = node.Address[:idx]
				}
				fmt.Fprintf(w, "%s\t%s\t%s\t%s\n",
					node.Name,
					displayAddr,
					node.State,
					node.Version)
			}

			w.Flush()

			return nil
		},
	}

	return cmd
}

func nodeGet() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "get <node>",
		Short: "Get node details",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			nodeRef := args[0]

			// Create SDS client
			sdsClient, err := newSDSClient()
			if err != nil {
				return fmt.Errorf("failed to connect to controller: %w", err)
			}
			defer sdsClient.Close()

			foundNode, err := sdsClient.GetNode(ctx, nodeRef)
			if err != nil {
				return fmt.Errorf("failed to get node: %w", err)
			}

			// Print node details
			fmt.Printf("Address:   %s\n", foundNode.Address)
			fmt.Printf("Hostname:  %s\n", foundNode.Hostname)
			fmt.Printf("State:     %s\n", foundNode.State)
			fmt.Printf("Version:   %s\n", foundNode.Version)
			fmt.Printf("Last Seen: %d\n", foundNode.LastSeen)
			if len(foundNode.Labels) > 0 {
				fmt.Printf("Labels:    %s\n", formatLabels(foundNode.Labels))
			}

			return nil
		},
	}

	return cmd
}

// nodeLabel sets key=value labels on a node, used by placement constraints such
// as `resource create --replicas-on-different rack`. An empty value (key=)
// deletes that label.
func nodeLabel() *cobra.Command {
	var replace bool
	cmd := &cobra.Command{
		Use:   "label <node> <key=value> [<key=value>...]",
		Short: "Set labels on a node (e.g. rack=A zone=east); key= deletes a label",
		Args:  cobra.MinimumNArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			nodeRef := args[0]
			labels := make(map[string]string, len(args)-1)
			for _, kv := range args[1:] {
				k, v, ok := strings.Cut(kv, "=")
				k = strings.TrimSpace(k)
				if !ok || k == "" {
					return fmt.Errorf("invalid label %q (want key=value)", kv)
				}
				labels[k] = strings.TrimSpace(v)
			}

			ctx := cmd.Context()
			sdsClient, err := newSDSClient()
			if err != nil {
				return fmt.Errorf("failed to connect to controller: %w", err)
			}
			defer sdsClient.Close()

			node, err := sdsClient.SetNodeLabels(ctx, nodeRef, labels, replace)
			if err != nil {
				return fmt.Errorf("failed to set node labels: %w", err)
			}
			fmt.Printf("Node '%s' labels: %s\n", node.Name, formatLabels(node.Labels))
			return nil
		},
	}
	cmd.Flags().BoolVar(&replace, "replace", false, "Replace all labels instead of merging")
	return cmd
}

// formatLabels renders a label map as a stable, comma-separated key=value list.
func formatLabels(labels map[string]string) string {
	if len(labels) == 0 {
		return "(none)"
	}
	keys := make([]string, 0, len(labels))
	for k := range labels {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, k+"="+labels[k])
	}
	return strings.Join(parts, ", ")
}

func nodeRegister() *cobra.Command {
	var name string
	var address string

	cmd := &cobra.Command{
		Use:   "register --name <name> --address <ip>",
		Short: "Register a storage node",
		RunE: func(cmd *cobra.Command, args []string) error {
			if name == "" {
				return fmt.Errorf("--name is required")
			}
			if address == "" {
				return fmt.Errorf("--address is required")
			}

			ctx := cmd.Context()

			// Create SDS client
			sdsClient, err := newSDSClient()
			if err != nil {
				return fmt.Errorf("failed to connect to controller: %w", err)
			}
			defer sdsClient.Close()

			// Register node
			node, err := sdsClient.RegisterNode(ctx, name, address)
			if err != nil {
				return fmt.Errorf("failed to register node: %w", err)
			}

			fmt.Printf("✓ Node registered successfully\n")
			fmt.Printf("  Name:    %s\n", node.Name)
			fmt.Printf("  Address: %s\n", node.Address)

			return nil
		},
	}

	cmd.Flags().StringVar(&name, "name", "", "Node name (e.g., orange1)")
	cmd.Flags().StringVar(&address, "address", "", "Node IP address (e.g., 192.168.1.10)")

	_ = cmd.MarkFlagRequired("name")
	_ = cmd.MarkFlagRequired("address")

	return cmd
}

func nodeUnregister() *cobra.Command {
	var address string

	cmd := &cobra.Command{
		Use:   "unregister --address <ip>",
		Short: "Unregister a storage node",
		Long: `Unregister a storage node from the cluster.
This removes the node from the database but does not affect the node itself.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if address == "" {
				return fmt.Errorf("--address is required")
			}

			ctx := cmd.Context()

			sdsClient, err := newSDSClient()
			if err != nil {
				return fmt.Errorf("failed to connect to controller: %w", err)
			}
			defer sdsClient.Close()

			err = sdsClient.UnregisterNode(ctx, address)
			if err != nil {
				return fmt.Errorf("failed to unregister node: %w", err)
			}

			fmt.Printf("✓ Node unregistered successfully\n")
			fmt.Printf("  Address: %s\n", address)

			return nil
		},
	}

	cmd.Flags().StringVar(&address, "address", "", "Node address (IP:port)")
	_ = cmd.MarkFlagRequired("address")

	return cmd
}
