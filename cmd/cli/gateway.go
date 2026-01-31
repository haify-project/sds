package main

import (
	"fmt"
	"os"
	"text/tabwriter"

	v1 "github.com/liliang-cn/sds/api/proto/v1"
	"github.com/liliang-cn/sds/pkg/client"
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
	cmd.AddCommand(gatewayList())
	cmd.AddCommand(gatewayDelete())
	cmd.AddCommand(gatewayStart())
	cmd.AddCommand(gatewayStop())

	return cmd
}

func gatewayISCSI() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "iscsi",
		Short: "iSCSI gateway management",
	}

	cmd.AddCommand(iscsiCreate())
	cmd.AddCommand(iscsiList())

	return cmd
}

func iscsiCreate() *cobra.Command {
	var resource, serviceIP, iqn, username, password, implementation string
	var allowedInitiators []string

	cmd := &cobra.Command{
		Use:   "create --resource <name> --iqn <iqn> --service-ip <ip/cidr>",
		Short: "Create iSCSI gateway",
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()

			if resource == "" {
				return fmt.Errorf("--resource is required")
			}
			if iqn == "" {
				return fmt.Errorf("--iqn is required")
			}
			if serviceIP == "" {
				return fmt.Errorf("--service-ip is required")
			}

			// Create SDS client
			sdsClient, err := client.NewSDSClient(controllerAddr)
			if err != nil {
				return fmt.Errorf("failed to connect to controller: %w", err)
			}
			defer sdsClient.Close()

			// Create iSCSI gateway
			req := &v1.CreateISCSIGatewayRequest{
				Resource:           resource,
				ServiceIp:          serviceIP,
				Iqn:                iqn,
				AllowedInitiators:  allowedInitiators,
				Username:           username,
				Password:           password,
				Implementation:     implementation,
			}

			if req.Implementation == "" {
				req.Implementation = "lio"
			}

			resp, err := sdsClient.CreateISCSIGateway(ctx, req)
			if err != nil {
				return fmt.Errorf("failed to create iSCSI gateway: %w", err)
			}

			if !resp.Success {
				return fmt.Errorf("failed to create iSCSI gateway: %s", resp.Message)
			}

			fmt.Printf("✓ iSCSI gateway created successfully\n")
			fmt.Printf("  Resource:     %s\n", resource)
			fmt.Printf("  IQN:          %s\n", iqn)
			fmt.Printf("  Service IP:   %s\n", serviceIP)
			fmt.Printf("  Config Path:  %s\n", resp.ConfigPath)
			fmt.Printf("\nNext steps:\n")
			fmt.Printf("  1. Reload drbd-reactor: sudo systemctl reload drbd-reactor\n")
			fmt.Printf("  2. Check gateway status: sudo journalctl -u drbd-reactor -f\n")

			return nil
		},
	}

	cmd.Flags().StringVar(&resource, "resource", "", "DRBD resource name")
	cmd.Flags().StringVar(&iqn, "iqn", "", "iSCSI Qualified Name (IQN)")
	cmd.Flags().StringVar(&serviceIP, "service-ip", "", "Service IP (e.g., 192.168.1.100/24)")
	cmd.Flags().StringSliceVar(&allowedInitiators, "allowed-initiators", []string{}, "Allowed initiator IQNs")
	cmd.Flags().StringVar(&username, "username", "", "CHAP username")
	cmd.Flags().StringVar(&password, "password", "", "CHAP password")
	cmd.Flags().StringVar(&implementation, "implementation", "lio", "iSCSI implementation (lio, tgt, iet)")

	cmd.MarkFlagRequired("resource")
	cmd.MarkFlagRequired("iqn")
	cmd.MarkFlagRequired("service-ip")

	return cmd
}

func iscsiList() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List iSCSI gateways",
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()

			sdsClient, err := client.NewSDSClient(controllerAddr)
			if err != nil {
				return fmt.Errorf("failed to connect to controller: %w", err)
			}
			defer sdsClient.Close()

			gateways, err := sdsClient.ListGateways(ctx)
			if err != nil {
				return fmt.Errorf("failed to list gateways: %w", err)
			}

			// Filter only iSCSI gateways
			var iscsiGateways []*v1.GatewayInfo
			for _, gw := range gateways {
				if gw.Type == "iscsi" {
					iscsiGateways = append(iscsiGateways, gw)
				}
			}

			if len(iscsiGateways) == 0 {
				fmt.Println("No iSCSI gateways configured")
				return nil
			}

			w := tabwriter.NewWriter(os.Stdout, 0, 0, 3, ' ', 0)
			fmt.Fprintln(w, "ID\tTYPE\tRESOURCE\tSTATE")

			for _, gw := range iscsiGateways {
				fmt.Fprintf(w, "%s\t%s\t%s\t%s\n",
					gw.Id, gw.Type, gw.Resource, gw.State)
			}

			w.Flush()

			return nil
		},
	}

	return cmd
}

func gatewayNFS() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "nfs",
		Short: "NFS gateway management",
	}

	cmd.AddCommand(nfsCreate())
	cmd.AddCommand(nfsList())

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

			// Create SDS client
			sdsClient, err := client.NewSDSClient(controllerAddr)
			if err != nil {
				return fmt.Errorf("failed to connect to controller: %w", err)
			}
			defer sdsClient.Close()

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
			fmt.Printf("  1. Reload drbd-reactor: sudo systemctl reload drbd-reactor\n")
			fmt.Printf("  2. Check gateway status: sudo journalctl -u drbd-reactor -f\n")
			fmt.Printf("  3. Mount on client: sudo mount -t nfs %s:%s /mnt\n", serviceIP, exportPath)

			return nil
		},
	}

	cmd.Flags().StringVar(&resource, "resource", "", "DRBD resource name")
	cmd.Flags().StringVar(&serviceIP, "service-ip", "", "Service IP (e.g., 192.168.1.200/24)")
	cmd.Flags().StringVar(&exportPath, "export-path", "", "Export path (e.g., /data)")
	cmd.Flags().StringSliceVar(&allowedIPs, "allowed-ips", []string{}, "Allowed client IPs (e.g., 192.168.1.0/24)")
	cmd.Flags().StringVar(&fsType, "fs-type", "ext4", "Filesystem type (ext4, xfs)")

	cmd.MarkFlagRequired("resource")
	cmd.MarkFlagRequired("service-ip")
	cmd.MarkFlagRequired("export-path")

	return cmd
}

func nfsList() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List NFS gateways",
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()

			sdsClient, err := client.NewSDSClient(controllerAddr)
			if err != nil {
				return fmt.Errorf("failed to connect to controller: %w", err)
			}
			defer sdsClient.Close()

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
			fmt.Fprintln(w, "ID\tTYPE\tRESOURCE\tSTATE")

			for _, gw := range nfsGateways {
				fmt.Fprintf(w, "%s\t%s\t%s\t%s\n",
					gw.Id, gw.Type, gw.Resource, gw.State)
			}

			w.Flush()

			return nil
		},
	}

	return cmd
}

func gatewayNVMe() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "nvme",
		Short: "NVMe-oF gateway management",
	}

	cmd.AddCommand(nvmeCreate())
	cmd.AddCommand(nvmeList())

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
			sdsClient, err := client.NewSDSClient(controllerAddr)
			if err != nil {
				return fmt.Errorf("failed to connect to controller: %w", err)
			}
			defer sdsClient.Close()

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
			fmt.Printf("\nNext steps:\n")
			fmt.Printf("  1. Reload drbd-reactor: sudo systemctl reload drbd-reactor\n")
			fmt.Printf("  2. Check gateway status: sudo journalctl -u drbd-reactor -f\n")

			return nil
		},
	}

	cmd.Flags().StringVar(&resource, "resource", "", "DRBD resource name")
	cmd.Flags().StringVar(&nqn, "nqn", "", "NVMe Qualified Name (NQN)")
	cmd.Flags().StringVar(&serviceIP, "service-ip", "", "Service IP (e.g., 192.168.1.150/24)")
	cmd.Flags().StringVar(&transportType, "transport", "tcp", "Transport type (tcp, rdma)")

	cmd.MarkFlagRequired("resource")
	cmd.MarkFlagRequired("nqn")
	cmd.MarkFlagRequired("service-ip")

	return cmd
}

func nvmeList() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List NVMe-oF gateways",
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()

			sdsClient, err := client.NewSDSClient(controllerAddr)
			if err != nil {
				return fmt.Errorf("failed to connect to controller: %w", err)
			}
			defer sdsClient.Close()

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
			fmt.Fprintln(w, "ID\tTYPE\tRESOURCE\tSTATE")

			for _, gw := range nvmeGateways {
				fmt.Fprintf(w, "%s\t%s\t%s\t%s\n",
					gw.Id, gw.Type, gw.Resource, gw.State)
			}

			w.Flush()

			return nil
		},
	}

	return cmd
}

func gatewayList() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List all gateways",
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()

			sdsClient, err := client.NewSDSClient(controllerAddr)
			if err != nil {
				return fmt.Errorf("failed to connect to controller: %w", err)
			}
			defer sdsClient.Close()

			gateways, err := sdsClient.ListGateways(ctx)
			if err != nil {
				return fmt.Errorf("failed to list gateways: %w", err)
			}

			if len(gateways) == 0 {
				fmt.Println("No gateways configured")
				return nil
			}

			w := tabwriter.NewWriter(os.Stdout, 0, 0, 3, ' ', 0)
			fmt.Fprintln(w, "ID\tTYPE\tRESOURCE\tSTATE")

			for _, gw := range gateways {
				fmt.Fprintf(w, "%s\t%s\t%s\t%s\n",
					gw.Id, gw.Type, gw.Resource, gw.State)
			}

			w.Flush()

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
			sdsClient, err := client.NewSDSClient(controllerAddr)
			if err != nil {
				return fmt.Errorf("failed to connect to controller: %w", err)
			}
			defer sdsClient.Close()

			// Delete gateway
			err = sdsClient.DeleteGateway(ctx, resource)
			if err != nil {
				return fmt.Errorf("failed to delete gateway: %w", err)
			}

			fmt.Printf("✓ Gateway deleted successfully\n")
			fmt.Printf("  Resource: %s\n", resource)
			fmt.Printf("\nNote: Configuration files have been removed from all nodes\n")
			fmt.Printf("      You may need to reload drbd-reactor: sudo systemctl reload drbd-reactor\n")

			return nil
		},
	}

	cmd.Flags().StringVar(&resource, "resource", "", "DRBD resource name")
	cmd.MarkFlagRequired("resource")

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

			sdsClient, err := client.NewSDSClient(controllerAddr)
			if err != nil {
				return fmt.Errorf("failed to connect to controller: %w", err)
			}
			defer sdsClient.Close()

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
	cmd.MarkFlagRequired("resource")

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

			sdsClient, err := client.NewSDSClient(controllerAddr)
			if err != nil {
				return fmt.Errorf("failed to connect to controller: %w", err)
			}
			defer sdsClient.Close()

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
	cmd.MarkFlagRequired("resource")

	return cmd
}

