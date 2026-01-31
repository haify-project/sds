package main

import (
	"context"
	"fmt"
	"time"

	"github.com/liliang-cn/sds/pkg/client"
	"github.com/spf13/cobra"
)

func snapshotCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "snapshot",
		Short: "Snapshot management (volume snapshots)",
	}

	cmd.AddCommand(snapshotCreate())
	cmd.AddCommand(snapshotDelete())
	cmd.AddCommand(snapshotRestore())
	cmd.AddCommand(snapshotList())

	return cmd
}

func snapshotCreate() *cobra.Command {
	var volume string
	var name string
	var node string

	cmd := &cobra.Command{
		Use:   "create",
		Short: "Create a snapshot of a volume",
		RunE: func(cmd *cobra.Command, args []string) error {
			if volume == "" {
				return fmt.Errorf("volume name is required")
			}
			if name == "" {
				return fmt.Errorf("snapshot name is required")
			}
			if node == "" {
				return fmt.Errorf("node name is required")
			}

			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()

			sdsClient, err := client.NewSDSClient(controllerAddr)
			if err != nil {
				return fmt.Errorf("failed to connect to controller: %w", err)
			}
			defer sdsClient.Close()

			err = sdsClient.CreateSnapshot(ctx, volume, name, node)
			if err != nil {
				return fmt.Errorf("failed to create snapshot: %w", err)
			}

			fmt.Printf("Snapshot '%s' created successfully for volume '%s' (node=%s)\n", name, volume, node)
			return nil
		},
	}

	cmd.Flags().StringVar(&volume, "volume", "", "Volume name (vg/lv format)")
	cmd.Flags().StringVar(&name, "name", "", "Snapshot name")
	cmd.Flags().StringVar(&node, "node", "", "Node name")

	cmd.MarkFlagRequired("volume")
	cmd.MarkFlagRequired("name")
	cmd.MarkFlagRequired("node")

	return cmd
}

func snapshotDelete() *cobra.Command {
	var volume string
	var name string
	var node string

	cmd := &cobra.Command{
		Use:   "delete",
		Short: "Delete a snapshot",
		RunE: func(cmd *cobra.Command, args []string) error {
			if volume == "" {
				return fmt.Errorf("volume name is required")
			}
			if name == "" {
				return fmt.Errorf("snapshot name is required")
			}
			if node == "" {
				return fmt.Errorf("node name is required")
			}

			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()

			sdsClient, err := client.NewSDSClient(controllerAddr)
			if err != nil {
				return fmt.Errorf("failed to connect to controller: %w", err)
			}
			defer sdsClient.Close()

			err = sdsClient.DeleteSnapshot(ctx, volume, name, node)
			if err != nil {
				return fmt.Errorf("failed to delete snapshot: %w", err)
			}

			fmt.Printf("Snapshot '%s' deleted successfully for volume '%s' (node=%s)\n", name, volume, node)
			return nil
		},
	}

	cmd.Flags().StringVar(&volume, "volume", "", "Volume name (vg/lv format)")
	cmd.Flags().StringVar(&name, "name", "", "Snapshot name")
	cmd.Flags().StringVar(&node, "node", "", "Node name")

	cmd.MarkFlagRequired("volume")
	cmd.MarkFlagRequired("name")
	cmd.MarkFlagRequired("node")

	return cmd
}

func snapshotRestore() *cobra.Command {
	var volume string
	var name string
	var node string

	cmd := &cobra.Command{
		Use:   "restore",
		Short: "Restore a snapshot to its source volume (merges snapshot back)",
		Long: `Restore a snapshot by merging it back into the source volume.
This operation requires the volume to be unmounted first.

Example:
  sds-cli snapshot restore --volume ubuntu-vg/lv0 --name lv0_snap --node orange1`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if volume == "" {
				return fmt.Errorf("volume name is required")
			}
			if name == "" {
				return fmt.Errorf("snapshot name is required")
			}
			if node == "" {
				return fmt.Errorf("node name is required")
			}

			ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
			defer cancel()

			sdsClient, err := client.NewSDSClient(controllerAddr)
			if err != nil {
				return fmt.Errorf("failed to connect to controller: %w", err)
			}
			defer sdsClient.Close()

			err = sdsClient.RestoreSnapshot(ctx, volume, name, node)
			if err != nil {
				return fmt.Errorf("failed to restore snapshot: %w", err)
			}

			fmt.Printf("Snapshot '%s' restored successfully to volume '%s' (node=%s)\n", name, volume, node)
			fmt.Println("\nNote: The snapshot has been merged back into the original volume.")
			return nil
		},
	}

	cmd.Flags().StringVar(&volume, "volume", "", "Volume name (vg/lv format)")
	cmd.Flags().StringVar(&name, "name", "", "Snapshot name")
	cmd.Flags().StringVar(&node, "node", "", "Node name")

	cmd.MarkFlagRequired("volume")
	cmd.MarkFlagRequired("name")
	cmd.MarkFlagRequired("node")

	return cmd
}

func snapshotList() *cobra.Command {
	var volume string
	var node string

	cmd := &cobra.Command{
		Use:   "list",
		Short: "List snapshots for a volume",
		RunE: func(cmd *cobra.Command, args []string) error {
			if volume == "" {
				return fmt.Errorf("volume name is required")
			}
			if node == "" {
				return fmt.Errorf("node name is required")
			}

			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()

			sdsClient, err := client.NewSDSClient(controllerAddr)
			if err != nil {
				return fmt.Errorf("failed to connect to controller: %w", err)
			}
			defer sdsClient.Close()

			snapshots, err := sdsClient.ListSnapshots(ctx, volume, node)
			if err != nil {
				return fmt.Errorf("failed to list snapshots: %w", err)
			}

			if len(snapshots) == 0 {
				fmt.Printf("No snapshots found for volume '%s' (node=%s)\n", volume, node)
				return nil
			}

			fmt.Printf("Snapshots for volume '%s' (node=%s):\n", volume, node)
			fmt.Println("  Name            Size")
			fmt.Println("  --------------- ----")
			for _, snap := range snapshots {
				fmt.Printf("  %-15s %d GB\n", snap.Name, snap.SizeGb)
			}

			return nil
		},
	}

	cmd.Flags().StringVar(&volume, "volume", "", "Volume name (vg/lv format)")
	cmd.Flags().StringVar(&node, "node", "", "Node name")

	cmd.MarkFlagRequired("volume")
	cmd.MarkFlagRequired("node")

	return cmd
}
