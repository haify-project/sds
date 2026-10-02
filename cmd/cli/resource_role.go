package main

import (
	"context"
	"fmt"
	"time"

	"github.com/spf13/cobra"
)

func resourcePrimary() *cobra.Command {
	var force bool

	cmd := &cobra.Command{
		Use:   "primary <resource> <node>",
		Short: "Set resource primary on node",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			resource := args[0]
			node := args[1]

			ctx, cancel := context.WithTimeout(context.Background(), nodeOpTimeout)
			defer cancel()

			sdsClient, err := newSDSClient()
			if err != nil {
				return fmt.Errorf("failed to connect to controller: %w", err)
			}
			defer closeClient(sdsClient)

			err = sdsClient.SetPrimary(ctx, resource, node, force)
			if err != nil {
				return fmt.Errorf("failed to set primary: %w", err)
			}

			fmt.Printf("Resource '%s' primary set to '%s'\n", resource, node)
			return nil
		},
	}

	cmd.Flags().BoolVar(&force, "force", false, "Force promotion")

	return cmd
}

func resourceSecondary() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "secondary <resource> <node>",
		Short: "Set resource secondary on node",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			resource := args[0]
			node := args[1]

			ctx, cancel := context.WithTimeout(context.Background(), nodeOpTimeout)
			defer cancel()

			sdsClient, err := newSDSClient()
			if err != nil {
				return fmt.Errorf("failed to connect to controller: %w", err)
			}
			defer closeClient(sdsClient)

			err = sdsClient.SetSecondary(ctx, resource, node)
			if err != nil {
				return fmt.Errorf("failed to set secondary: %w", err)
			}

			fmt.Printf("Resource '%s' set to secondary on '%s'\n", resource, node)
			return nil
		},
	}

	return cmd
}

// resourceDualPrimary exposes the allow-two-primaries toggle used to bracket a
// hypervisor live migration. It is primarily driven by the Proxmox storage
// plugin; the CLI form exists for operators to inspect/repair a stranded window.
func resourceDualPrimary() *cobra.Command {
	return &cobra.Command{
		Use:   "dual-primary <resource> on|off",
		Short: "Open or close a dual-primary window (live migration only)",
		Long: "Toggle DRBD's allow-two-primaries on a resource.\n\n" +
			"This exists so a hypervisor can live-migrate a guest: source and target both\n" +
			"hold the disk open during the hand-off. It is NOT a way to use one volume from\n" +
			"two machines at once — an ordinary filesystem mounted twice will corrupt.\n\n" +
			"WAN resources are refused (their replication is asynchronous). The toggle is\n" +
			"runtime-only, so a reboot or `drbdadm adjust` restores single-primary anyway.\n" +
			"`off` is idempotent and verifies that no node is left dual-primary.",
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			resource := args[0]
			var enable bool
			switch args[1] {
			case "on":
				enable = true
			case "off":
				enable = false
			default:
				return fmt.Errorf("invalid state %q (use on or off)", args[1])
			}

			ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
			defer cancel()

			sdsClient, err := newSDSClient()
			if err != nil {
				return fmt.Errorf("failed to connect to controller: %w", err)
			}
			defer closeClient(sdsClient)

			if err := sdsClient.SetDualPrimary(ctx, resource, enable); err != nil {
				return fmt.Errorf("failed to set dual-primary: %w", err)
			}

			if enable {
				fmt.Printf("Dual-primary window OPEN on '%s'.\n", resource)
				fmt.Printf("Close it as soon as the migration finishes: sds resource dual-primary %s off\n", resource)
			} else {
				fmt.Printf("Dual-primary window closed on '%s'.\n", resource)
			}
			return nil
		},
	}
}

func resourcePromote() *cobra.Command {
	var force bool

	cmd := &cobra.Command{
		Use:   "promote <resource> <node>",
		Short: "Promote DRBD resource to primary",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			resource := args[0]
			node := args[1]

			ctx, cancel := context.WithTimeout(context.Background(), nodeOpTimeout)
			defer cancel()

			sdsClient, err := newSDSClient()
			if err != nil {
				return fmt.Errorf("failed to connect to controller: %w", err)
			}
			defer closeClient(sdsClient)

			err = sdsClient.SetPrimary(ctx, resource, node, force)
			if err != nil {
				return fmt.Errorf("failed to promote resource: %w", err)
			}

			fmt.Printf("Resource '%s' promoted on '%s'\n", resource, node)
			return nil
		},
	}

	cmd.Flags().BoolVar(&force, "force", false, "Force promotion")

	return cmd
}

func resourceDemote() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "demote <resource> <node>",
		Short: "Demote DRBD resource to secondary",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			resource := args[0]
			node := args[1]

			ctx, cancel := context.WithTimeout(context.Background(), nodeOpTimeout)
			defer cancel()

			sdsClient, err := newSDSClient()
			if err != nil {
				return fmt.Errorf("failed to connect to controller: %w", err)
			}
			defer closeClient(sdsClient)

			err = sdsClient.SetSecondary(ctx, resource, node)
			if err != nil {
				return fmt.Errorf("failed to demote resource: %w", err)
			}

			fmt.Printf("Resource '%s' demoted on '%s'\n", resource, node)
			return nil
		},
	}

	return cmd
}
