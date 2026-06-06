package main

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/spf13/cobra"
)

// haSelfCommand manages high availability of the controller itself: its
// metadata database lives on a DRBD resource and drbd-reactor decides which
// node runs the controller, reachable through a VIP.
func haSelfCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "self",
		Short: "Manage high availability of the SDS controller itself",
	}

	cmd.AddCommand(haSelfEnable())
	cmd.AddCommand(haSelfStatus())
	cmd.AddCommand(haSelfDisable())

	return cmd
}

func haSelfEnable() *cobra.Command {
	var vip string
	var pool string
	var sizeGB uint32
	var port uint32
	var nodes string

	cmd := &cobra.Command{
		Use:   "enable",
		Short: "Make the SDS controller highly available on its own DRBD + drbd-reactor machinery",
		Long: `Provisions a small DRBD resource (sds-meta) for the controller database,
distributes the controller to all nodes, and hands management over to
drbd-reactor: the node holding the DRBD Primary mounts the database, brings
up the VIP, and runs the controller. On node failure the controller fails
over automatically.

The current controller restarts under reactor management during the handoff,
so this command's connection drops by design. Track progress with
'sds-cli ha self status' (against the VIP) or the handoff log on the node.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if vip == "" {
				return fmt.Errorf("--vip is required (CIDR, e.g. 192.168.1.50/24)")
			}
			if pool == "" {
				return fmt.Errorf("--pool is required")
			}

			// Resource creation + artifact distribution take a while.
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
			defer cancel()

			sdsClient, err := newSDSClient()
			if err != nil {
				return fmt.Errorf("failed to connect to controller: %w", err)
			}
			defer sdsClient.Close()

			var nodeList []string
			if nodes != "" {
				nodeList = strings.Split(nodes, ",")
			}

			resource, handoffLog, err := sdsClient.EnableSelfHa(ctx, vip, pool, sizeGB, port, nodeList)
			if err != nil {
				return fmt.Errorf("failed to enable self-HA: %w", err)
			}

			fmt.Printf("Self-HA handoff started\n")
			fmt.Printf("  Resource:    %s\n", resource)
			fmt.Printf("  VIP:         %s\n", vip)
			fmt.Printf("  Handoff log: %s (on the controller node)\n", handoffLog)
			fmt.Printf("\nThe controller is restarting under drbd-reactor management.\n")
			vipAddr := strings.Split(vip, "/")[0]
			fmt.Printf("Verify with:  sds-cli -c %s:3374 ha self status\n", vipAddr)

			return nil
		},
	}

	cmd.Flags().StringVar(&vip, "vip", "", "Virtual IP for the controller (CIDR, required)")
	cmd.Flags().StringVar(&pool, "pool", "", "Storage pool backing the metadata resource (required)")
	cmd.Flags().Uint32Var(&sizeGB, "size", 1, "Metadata resource size in GB")
	cmd.Flags().Uint32Var(&port, "port", 7999, "DRBD port for the metadata resource")
	cmd.Flags().StringVar(&nodes, "nodes", "", "Target nodes (comma-separated, default: all registered nodes)")

	return cmd
}

func haSelfStatus() *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "Show controller self-HA status",
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()

			sdsClient, err := newSDSClient()
			if err != nil {
				return fmt.Errorf("failed to connect to controller: %w", err)
			}
			defer sdsClient.Close()

			status, err := sdsClient.GetSelfHaStatus(ctx)
			if err != nil {
				return fmt.Errorf("failed to get self-HA status: %w", err)
			}

			if !status.Enabled {
				fmt.Println("Controller self-HA: disabled (standalone controller)")
				return nil
			}

			fmt.Println("Controller self-HA: enabled")
			fmt.Printf("  Resource:    %s\n", status.Resource)
			fmt.Printf("  VIP:         %s\n", status.VIP)
			fmt.Printf("  Nodes:       %s\n", strings.Join(status.Nodes, ", "))
			if status.ActiveNode != "" {
				fmt.Printf("  Active node: %s\n", status.ActiveNode)
			} else {
				fmt.Printf("  Active node: unknown\n")
			}
			fmt.Printf("\nDetails: sds-cli ha status %s\n", status.Resource)

			return nil
		},
	}
}

func haSelfDisable() *cobra.Command {
	var node string

	cmd := &cobra.Command{
		Use:   "disable",
		Short: "Revert the controller to standalone operation on one node",
		Long: `Removes drbd-reactor management of the controller, copies the database
back to the node-local path on the given node, and re-enables the controller
as a normal systemd service there. The sds-meta resource is kept and can be
removed afterwards with 'sds-cli resource delete sds-meta'.

The managed controller stops during this operation, so this command's
connection drops by design.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if node == "" {
				return fmt.Errorf("--node is required (the node that keeps running the controller)")
			}

			ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
			defer cancel()

			sdsClient, err := newSDSClient()
			if err != nil {
				return fmt.Errorf("failed to connect to controller: %w", err)
			}
			defer sdsClient.Close()

			if err := sdsClient.DisableSelfHa(ctx, node); err != nil {
				return fmt.Errorf("failed to disable self-HA: %w", err)
			}

			fmt.Printf("Self-HA disable started\n")
			fmt.Printf("  Controller will restart standalone on: %s\n", node)
			fmt.Printf("\nVerify with:  sds-cli -c %s:3374 ha self status\n", node)

			return nil
		},
	}

	cmd.Flags().StringVar(&node, "node", "", "Node that keeps running the standalone controller (required)")

	return cmd
}
