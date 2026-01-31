package main

import (
	"fmt"
	"os"
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
			fmt.Fprintln(w, "ADDRESS\tHOSTNAME\tSTATE\tVERSION")

			for _, node := range nodes {
				fmt.Fprintf(w, "%s\t%s\t%s\t%s\n",
					node.Address,
					node.Hostname,
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
	var address string

	cmd := &cobra.Command{
		Use:   "register",
		Short: "Force register a node (emergency use only)",
		RunE: func(cmd *cobra.Command, args []string) error {
			if address == "" {
				return fmt.Errorf("node address is required")
			}

			ctx := cmd.Context()

			// Create SDS client
			sdsClient, err := client.NewSDSClient(controllerAddr)
			if err != nil {
				return fmt.Errorf("failed to connect to controller: %w", err)
			}
			defer sdsClient.Close()

			// Register node
			node, err := sdsClient.RegisterNode(ctx, address)
			if err != nil {
				return fmt.Errorf("failed to register node: %w", err)
			}

			if node != nil {
				fmt.Printf("✓ Node registered successfully\n")
				fmt.Printf("  Address:  %s\n", node.Address)
				fmt.Printf("  Hostname: %s\n", node.Hostname)
				fmt.Printf("  State:    %s\n", node.State)
			}

			return nil
		},
	}

	cmd.Flags().StringVar(&address, "address", "", "Node address (IP:PORT)")

	cmd.MarkFlagRequired("address")

	return cmd
}
