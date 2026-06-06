package main

import (
	"fmt"
	"slices"
	"strings"

	"github.com/liliang-cn/sds/pkg/client"
	"github.com/spf13/cobra"
)

func gatewayGet() *cobra.Command {
	var resource string

	cmd := &cobra.Command{
		Use:   "get --resource <name>",
		Short: "Get gateway details",
		RunE: func(cmd *cobra.Command, args []string) error {
			if resource == "" {
				return fmt.Errorf("--resource is required")
			}

			sdsClient, err := client.NewSDSClient(controllerAddr)
			if err != nil {
				return fmt.Errorf("failed to connect to controller: %w", err)
			}
			defer sdsClient.Close()

			gw, err := sdsClient.GetGateway(cmd.Context(), resource)
			if err != nil {
				return fmt.Errorf("failed to get gateway: %w", err)
			}

			fmt.Printf("Resource: %s\n", gw.Resource)
			fmt.Printf("Type:     %s\n", gw.Type)
			fmt.Printf("State:    %s\n", emptyGatewayValue(gw.State, "unknown"))
			fmt.Printf("Node:     %s\n", emptyGatewayValue(gw.Node, "-"))
			if gw.Path != "" {
				fmt.Printf("Path:     %s\n", gw.Path)
			}
			if len(gw.Options) > 0 {
				fmt.Printf("Details:\n")
				for _, key := range sortedGatewayOptionKeys(gw.Options) {
					fmt.Printf("  %s: %s\n", key, gw.Options[key])
				}
			}

			return nil
		},
	}

	cmd.Flags().StringVar(&resource, "resource", "", "DRBD resource name")
	_ = cmd.MarkFlagRequired("resource")

	return cmd
}

func gatewayStatus() *cobra.Command {
	var resource string

	cmd := &cobra.Command{
		Use:   "status --resource <name>",
		Short: "Show gateway status",
		RunE: func(cmd *cobra.Command, args []string) error {
			if resource == "" {
				return fmt.Errorf("--resource is required")
			}

			sdsClient, err := client.NewSDSClient(controllerAddr)
			if err != nil {
				return fmt.Errorf("failed to connect to controller: %w", err)
			}
			defer sdsClient.Close()

			gw, err := sdsClient.GetGateway(cmd.Context(), resource)
			if err != nil {
				return fmt.Errorf("failed to get gateway status: %w", err)
			}

			fmt.Printf("%s %s\n", strings.ToUpper(gw.Type), gw.Resource)
			fmt.Printf("  state: %s\n", emptyGatewayValue(gw.State, "unknown"))
			fmt.Printf("  node:  %s\n", emptyGatewayValue(gw.Node, "-"))
			if role := gw.Options["role"]; role != "" {
				fmt.Printf("  role:  %s\n", role)
			}
			if volumes := gw.Options["volumes"]; volumes != "" {
				fmt.Printf("  vols:  %s\n", volumes)
			}
			if path := gw.Path; path != "" {
				fmt.Printf("  path:  %s\n", path)
			}

			return nil
		},
	}

	cmd.Flags().StringVar(&resource, "resource", "", "DRBD resource name")
	_ = cmd.MarkFlagRequired("resource")

	return cmd
}

func sortedGatewayOptionKeys(options map[string]string) []string {
	keys := make([]string, 0, len(options))
	for key := range options {
		keys = append(keys, key)
	}
	slices.Sort(keys)
	return keys
}

func emptyGatewayValue(value, fallback string) string {
	if strings.TrimSpace(value) == "" {
		return fallback
	}
	return value
}
