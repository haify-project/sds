package main

import (
	"fmt"
	"os"
	"text/tabwriter"

	v1 "github.com/haify-project/sds/api/proto/v1"
	"github.com/spf13/cobra"
)

func gatewayNFS() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "nfs",
		Short: "NFS gateway management",
	}

	cmd.AddCommand(nfsCreate())
	cmd.AddCommand(nfsList())
	cmd.AddCommand(nfsExportCommand())
	cmd.AddCommand(nfsMount())

	return cmd
}

func nfsCreate() *cobra.Command {
	var resource, serviceIP, exportPath, fsType string
	var allowedIPs []string

	cmd := &cobra.Command{
		Use:   "create --resource <name> --service-ip <ip/cidr> --export-path <path>",
		Short: "Create NFS gateway",
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()

			if resource == "" {
				return fmt.Errorf("--resource is required")
			}
			if serviceIP == "" {
				return fmt.Errorf("--service-ip is required")
			}
			if exportPath == "" {
				return fmt.Errorf("--export-path is required")
			}

			// Create Haify client
			sdsClient, err := newSDSClient()
			if err != nil {
				return fmt.Errorf("failed to connect to controller: %w", err)
			}
			defer closeClient(sdsClient)

			// Create NFS gateway
			req := &v1.CreateNFSGatewayRequest{
				Resource:   resource,
				ServiceIp:  serviceIP,
				ExportPath: exportPath,
				AllowedIps: allowedIPs,
				FsType:     fsType,
			}

			if req.FsType == "" {
				req.FsType = "ext4"
			}

			resp, err := sdsClient.CreateNFSGateway(ctx, req)
			if err != nil {
				return fmt.Errorf("failed to create NFS gateway: %w", err)
			}

			if !resp.Success {
				return fmt.Errorf("failed to create NFS gateway: %s", resp.Message)
			}

			fmt.Printf("✓ NFS gateway created successfully\n")
			fmt.Printf("  Resource:     %s\n", resource)
			fmt.Printf("  Service IP:   %s\n", serviceIP)
			fmt.Printf("  Export Path:  %s\n", exportPath)
			fmt.Printf("  Config Path:  %s\n", resp.ConfigPath)
			fmt.Printf("\nNext steps:\n")
			fmt.Printf("  1. Check gateway status: sds gateway list\n")
			fmt.Printf("  2. Mount on client: sudo mount -t nfs %s:%s /mnt\n", gatewayServiceHost(serviceIP), gatewayExportDirectory(resource, exportPath))

			return nil
		},
	}

	cmd.Flags().StringVar(&resource, "resource", "", "DRBD resource name")
	cmd.Flags().StringVar(&serviceIP, "service-ip", "", "Service IP (e.g., 192.168.1.200/24)")
	cmd.Flags().StringVar(&exportPath, "export-path", "", "Export path (e.g., /data)")
	cmd.Flags().StringSliceVar(&allowedIPs, "allowed-ips", []string{}, "Allowed client IPs (e.g., 192.168.1.0/24)")
	cmd.Flags().StringVar(&fsType, "fs-type", "ext4", "Filesystem type (ext4, xfs, btrfs)")

	_ = cmd.MarkFlagRequired("resource")
	_ = cmd.MarkFlagRequired("service-ip")
	_ = cmd.MarkFlagRequired("export-path")

	return cmd
}

func nfsList() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List NFS gateways",
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

			// Filter only NFS gateways
			var nfsGateways []*v1.GatewayInfo
			for _, gw := range gateways {
				if gw.Type == "nfs" {
					nfsGateways = append(nfsGateways, gw)
				}
			}

			if len(nfsGateways) == 0 {
				fmt.Println("No NFS gateways configured")
				return nil
			}

			w := tabwriter.NewWriter(os.Stdout, 0, 0, 3, ' ', 0)
			_, _ = fmt.Fprintln(w, "ID\tTYPE\tRESOURCE\tSTATE")

			for _, gw := range nfsGateways {
				_, _ = fmt.Fprintf(w, "%s\t%s\t%s\t%s\n",
					gw.Id, gw.Type, gw.Resource, gw.State)
			}

			_ = w.Flush()

			return nil
		},
	}

	return cmd
}
