package main

import (
	"fmt"
	"os"
	"text/tabwriter"

	v1 "github.com/haify-project/sds/api/proto/v1"
	"github.com/spf13/cobra"
)

func gatewayNVMe() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "nvme",
		Short: "NVMe-oF gateway management",
	}

	cmd.AddCommand(nvmeCreate())
	cmd.AddCommand(nvmeList())
	cmd.AddCommand(nvmeNamespaceCommand())
	cmd.AddCommand(nvmeHostCommand())

	return cmd
}

func nvmeCreate() *cobra.Command {
	var resource, serviceIP, nqn, transportType string

	cmd := &cobra.Command{
		Use:   "create --resource <name> --nqn <nqn> --service-ip <ip/cidr>",
		Short: "Create NVMe-oF gateway",
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()

			if resource == "" {
				return fmt.Errorf("--resource is required")
			}
			if nqn == "" {
				return fmt.Errorf("--nqn is required")
			}
			if serviceIP == "" {
				return fmt.Errorf("--service-ip is required")
			}

			// Create SDS client
			sdsClient, err := newSDSClient()
			if err != nil {
				return fmt.Errorf("failed to connect to controller: %w", err)
			}
			defer closeClient(sdsClient)

			// Create NVMe-oF gateway
			req := &v1.CreateNVMeGatewayRequest{
				Resource:      resource,
				ServiceIp:     serviceIP,
				Nqn:           nqn,
				TransportType: transportType,
			}

			if req.TransportType == "" {
				req.TransportType = "tcp"
			}

			resp, err := sdsClient.CreateNVMeGateway(ctx, req)
			if err != nil {
				return fmt.Errorf("failed to create NVMe-oF gateway: %w", err)
			}

			if !resp.Success {
				return fmt.Errorf("failed to create NVMe-oF gateway: %s", resp.Message)
			}

			fmt.Printf("✓ NVMe-oF gateway created successfully\n")
			fmt.Printf("  Resource:     %s\n", resource)
			fmt.Printf("  NQN:          %s\n", nqn)
			fmt.Printf("  Service IP:   %s\n", serviceIP)
			fmt.Printf("  Config Path:  %s\n", resp.ConfigPath)
			fmt.Printf("\nCheck gateway status: sds gateway list\n")

			return nil
		},
	}

	cmd.Flags().StringVar(&resource, "resource", "", "DRBD resource name")
	cmd.Flags().StringVar(&nqn, "nqn", "", "NVMe Qualified Name (NQN)")
	cmd.Flags().StringVar(&serviceIP, "service-ip", "", "Service IP (e.g., 192.168.1.150/24)")
	cmd.Flags().StringVar(&transportType, "transport", "tcp", "Transport type (tcp, rdma); loads nvmet-tcp or nvmet-rdma on the nodes")

	_ = cmd.MarkFlagRequired("resource")
	_ = cmd.MarkFlagRequired("nqn")
	_ = cmd.MarkFlagRequired("service-ip")

	return cmd
}

func nvmeList() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List NVMe-oF gateways",
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

			// Filter only NVMe-oF gateways
			var nvmeGateways []*v1.GatewayInfo
			for _, gw := range gateways {
				if gw.Type == "nvmeof" {
					nvmeGateways = append(nvmeGateways, gw)
				}
			}

			if len(nvmeGateways) == 0 {
				fmt.Println("No NVMe-oF gateways configured")
				return nil
			}

			w := tabwriter.NewWriter(os.Stdout, 0, 0, 3, ' ', 0)
			_, _ = fmt.Fprintln(w, "ID\tTYPE\tRESOURCE\tSTATE")

			for _, gw := range nvmeGateways {
				_, _ = fmt.Fprintf(w, "%s\t%s\t%s\t%s\n",
					gw.Id, gw.Type, gw.Resource, gw.State)
			}

			_ = w.Flush()

			return nil
		},
	}

	return cmd
}
