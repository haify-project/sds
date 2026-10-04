package main

import (
	"fmt"
	"os"
	"text/tabwriter"

	"github.com/spf13/cobra"
)

func gatewayCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "gateway",
		Short: "Gateway management (iSCSI, NFS, NVMe-oF)",
	}

	cmd.AddCommand(gatewayISCSI())
	cmd.AddCommand(gatewayNFS())
	cmd.AddCommand(gatewayNVMe())
	cmd.AddCommand(gatewaySMB())
	cmd.AddCommand(gatewayList())
	cmd.AddCommand(gatewayGet())
	cmd.AddCommand(gatewayStatus())
	cmd.AddCommand(gatewayDelete())
	cmd.AddCommand(gatewayStart())
	cmd.AddCommand(gatewayStop())

	return cmd
}

func gatewayList() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List all gateways",
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()

			sdsClient, err := newSDSClient()
			if err != nil {
				return fmt.Errorf("failed to connect to controller: %w", err)
			}
			defer closeClient(sdsClient)

			gateways, err := sdsClient.ListGateways(ctx)
			if err != nil {
				return fmt.Errorf("failed to list gateways: %w", err)
			}

			if len(gateways) == 0 {
				fmt.Println("No gateways configured")
				return nil
			}

			w := tabwriter.NewWriter(os.Stdout, 0, 0, 3, ' ', 0)
			_, _ = fmt.Fprintln(w, "ID\tTYPE\tRESOURCE\tSTATE")

			for _, gw := range gateways {
				_, _ = fmt.Fprintf(w, "%s\t%s\t%s\t%s\n",
					gw.Id, gw.Type, gw.Resource, gw.State)
			}

			_ = w.Flush()

			return nil
		},
	}

	return cmd
}

func gatewayDelete() *cobra.Command {
	var resource string

	cmd := &cobra.Command{
		Use:   "delete --resource <name>",
		Short: "Delete a gateway",
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()

			if resource == "" {
				return fmt.Errorf("--resource is required")
			}

			// Create SDS client
			sdsClient, err := newSDSClient()
			if err != nil {
				return fmt.Errorf("failed to connect to controller: %w", err)
			}
			defer closeClient(sdsClient)

			// Delete gateway
			err = sdsClient.DeleteGateway(ctx, resource)
			if err != nil {
				return fmt.Errorf("failed to delete gateway: %w", err)
			}

			fmt.Printf("✓ Gateway deleted successfully\n")
			fmt.Printf("  Resource: %s\n", resource)

			return nil
		},
	}

	cmd.Flags().StringVar(&resource, "resource", "", "DRBD resource name")
	_ = cmd.MarkFlagRequired("resource")

	return cmd
}

func gatewayStart() *cobra.Command {
	var resource string

	cmd := &cobra.Command{
		Use:   "start --resource <name>",
		Short: "Start a gateway (activate resources and services)",
		Long: `Start a gateway by promoting the DRBD resource and starting all services.
This is typically handled automatically by drbd-reactor.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()

			if resource == "" {
				return fmt.Errorf("--resource is required")
			}

			sdsClient, err := newSDSClient()
			if err != nil {
				return fmt.Errorf("failed to connect to controller: %w", err)
			}
			defer closeClient(sdsClient)

			err = sdsClient.StartGateway(ctx, resource)
			if err != nil {
				return fmt.Errorf("failed to start gateway: %w", err)
			}

			fmt.Printf("✓ Gateway started successfully\n")
			fmt.Printf("  Resource: %s\n", resource)
			return nil
		},
	}

	cmd.Flags().StringVar(&resource, "resource", "", "DRBD resource name")
	_ = cmd.MarkFlagRequired("resource")

	return cmd
}

func gatewayStop() *cobra.Command {
	var resource string

	cmd := &cobra.Command{
		Use:   "stop --resource <name>",
		Short: "Stop a gateway (demote resources and stop services)",
		Long: `Stop a gateway by demoting the DRBD resource and stopping all services.
This is typically handled automatically by drbd-reactor.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()

			if resource == "" {
				return fmt.Errorf("--resource is required")
			}

			sdsClient, err := newSDSClient()
			if err != nil {
				return fmt.Errorf("failed to connect to controller: %w", err)
			}
			defer closeClient(sdsClient)

			err = sdsClient.StopGateway(ctx, resource)
			if err != nil {
				return fmt.Errorf("failed to stop gateway: %w", err)
			}

			fmt.Printf("✓ Gateway stopped successfully\n")
			fmt.Printf("  Resource: %s\n", resource)
			return nil
		},
	}

	cmd.Flags().StringVar(&resource, "resource", "", "DRBD resource name")
	_ = cmd.MarkFlagRequired("resource")

	return cmd
}
