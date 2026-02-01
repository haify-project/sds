package main

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/liliang-cn/sds/pkg/client"
)

func healthCommand() *cobra.Command {
	var nodes string

	cmd := &cobra.Command{
		Use:   "health-check",
		Short: "Check node health (drbd, drbd-reactor, resource-agents)",
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
			defer cancel()

			sdsClient, err := client.NewSDSClient(controllerAddr)
			if err != nil {
				return fmt.Errorf("failed to connect to controller: %w", err)
			}
			defer sdsClient.Close()

			// Get nodes to check
			var nodeList []string
			if nodes != "" {
				nodeList = strings.Split(nodes, ",")
				for i := range nodeList {
					nodeList[i] = strings.TrimSpace(nodeList[i])
				}
			} else {
				// Get all registered nodes
				nodeInfos, err := sdsClient.ListNodes(ctx)
				if err != nil {
					return fmt.Errorf("failed to list nodes: %w", err)
				}
				if len(nodeInfos) == 0 {
					return fmt.Errorf("no registered nodes found")
				}
				for _, n := range nodeInfos {
					nodeList = append(nodeList, n.Address)
				}
			}

			// Check health for each node
			allHealthy := true
			for _, node := range nodeList {
				fmt.Printf("\n=== Node: %s ===\n", node)
				healthy, err := sdsClient.HealthCheck(ctx, node)
				if err != nil {
					fmt.Printf("  Error: %v\n", err)
					allHealthy = false
					continue
				}

				// Print DRBD status
				if healthy.DrbdInstalled {
					fmt.Printf("  [OK] DRBD: %s\n", healthy.DrbdVersion)
				} else {
					fmt.Printf("  [MISSING] DRBD not installed\n")
					allHealthy = false
				}

				// Print drbd-reactor status
				if healthy.DrbdReactorInstalled {
					fmt.Printf("  [OK] drbd-reactor: %s\n", healthy.DrbdReactorVersion)
					if healthy.DrbdReactorRunning {
						fmt.Printf("  [OK] drbd-reactor service: running\n")
					} else {
						fmt.Printf("  [WARN] drbd-reactor service: not running\n")
					}
				} else {
					fmt.Printf("  [MISSING] drbd-reactor not installed\n")
					allHealthy = false
				}

				// Print resource-agents-extra status
				if healthy.ResourceAgentsInstalled {
					fmt.Printf("  [OK] resource-agents-extra: installed\n")
					if len(healthy.AvailableAgents) > 0 {
						fmt.Printf("  [INFO] Available agents: %s\n", strings.Join(healthy.AvailableAgents, ", "))
					}
				} else {
					fmt.Printf("  [MISSING] resource-agents-extra not installed\n")
					allHealthy = false
				}
			}

			fmt.Println()
			if allHealthy {
				fmt.Println("Overall: All nodes are healthy")
				return nil
			}
			fmt.Println("Overall: Some nodes have issues")
			return fmt.Errorf("health check failed")
		},
	}

	cmd.Flags().StringVar(&nodes, "nodes", "", "Comma-separated nodes to check (default: all registered nodes)")

	return cmd
}
