package main

import (
	"fmt"
	"os"
	"text/tabwriter"

	v1 "github.com/haify-project/sds/api/proto/v1"
	"github.com/spf13/cobra"
)

func gatewayISCSI() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "iscsi",
		Short: "iSCSI gateway management",
	}

	cmd.AddCommand(iscsiCreate())
	cmd.AddCommand(iscsiList())
	cmd.AddCommand(iscsiLUNCommand())
	cmd.AddCommand(iscsiInitiatorCommand())
	cmd.AddCommand(iscsiCHAPCommand())

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

			// Create Haify client
			sdsClient, err := newSDSClient()
			if err != nil {
				return fmt.Errorf("failed to connect to controller: %w", err)
			}
			defer closeClient(sdsClient)

			// Create iSCSI gateway
			req := &v1.CreateISCSIGatewayRequest{
				Resource:          resource,
				ServiceIp:         serviceIP,
				Iqn:               iqn,
				AllowedInitiators: allowedInitiators,
				Username:          username,
				Password:          password,
				Implementation:    implementation,
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
			fmt.Printf("\nCheck gateway status: sds gateway list\n")

			return nil
		},
	}

	cmd.Flags().StringVar(&resource, "resource", "", "DRBD resource name")
	cmd.Flags().StringVar(&iqn, "iqn", "", "iSCSI Qualified Name (IQN)")
	cmd.Flags().StringVar(&serviceIP, "service-ip", "", "Service IP (e.g., 192.168.1.100/24)")
	cmd.Flags().StringSliceVar(&allowedInitiators, "allowed-initiators", []string{}, "Allowed initiator IQNs")
	cmd.Flags().StringVar(&username, "username", "", "CHAP username")
	cmd.Flags().StringVar(&password, "password", "", "CHAP password")
	cmd.Flags().StringVar(&implementation, "implementation", "lio", "iSCSI implementation: lio (the only one supported; tgt and iet are refused)")

	_ = cmd.MarkFlagRequired("resource")
	_ = cmd.MarkFlagRequired("iqn")
	_ = cmd.MarkFlagRequired("service-ip")

	return cmd
}

func iscsiList() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List iSCSI gateways",
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
			// Writes to the command's own output stream are best-effort. The only ways
			// they fail are a closed pipe (`sds ... | head`) or a full disk, neither of
			// which this command can report anywhere the operator is still looking, and
			// treating them as errors would report a successful operation as failed.
			_, _ = fmt.Fprintln(w, "ID\tTYPE\tRESOURCE\tSTATE")

			for _, gw := range iscsiGateways {
				_, _ = fmt.Fprintf(w, "%s\t%s\t%s\t%s\n",
					gw.Id, gw.Type, gw.Resource, gw.State)
			}

			// Flush pushes the buffered table to stdout; like the Fprint calls above it
			// is best-effort, and a write failure here says nothing about whether the
			// operation the operator asked for succeeded.
			_ = w.Flush()

			return nil
		},
	}

	return cmd
}
