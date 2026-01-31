package main

import (
	"fmt"
	"os"
	"strings"
	"text/tabwriter"

	v1 "github.com/liliang-cn/sds/api/proto/v1"
	"github.com/liliang-cn/sds/pkg/client"
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

	return cmd
}

func nodeList() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List all nodes in the cluster",
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()

			// Create SDS client
			sdsClient, err := client.NewSDSClient(controllerAddr)
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
		Use:   "get <node-address>",
		Short: "Get node details",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			address := args[0]

			// Create SDS client
			sdsClient, err := client.NewSDSClient(controllerAddr)
			if err != nil {
				return fmt.Errorf("failed to connect to controller: %w", err)
			}
			defer sdsClient.Close()

			// List all nodes and find the requested one
			nodes, err := sdsClient.ListNodes(ctx)
			if err != nil {
				return fmt.Errorf("failed to list nodes: %w", err)
			}

			// Find the node
			var foundNode *v1.NodeInfo
			for _, node := range nodes {
				if node.Address == address {
					foundNode = node
					break
				}
			}

			if foundNode == nil {
				fmt.Printf("Node not found: %s\n", address)
				return nil
			}

			// Print node details
			fmt.Printf("Address:   %s\n", foundNode.Address)
			fmt.Printf("Hostname:  %s\n", foundNode.Hostname)
			fmt.Printf("State:     %s\n", foundNode.State)
			fmt.Printf("Version:   %s\n", foundNode.Version)
			fmt.Printf("Last Seen: %d\n", foundNode.LastSeen)

			return nil
		},
	}

	return cmd
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
			sdsClient, err := client.NewSDSClient(controllerAddr)
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

	cmd.MarkFlagRequired("name")
	cmd.MarkFlagRequired("address")

	return cmd
}
