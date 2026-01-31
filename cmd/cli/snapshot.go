package main

import (
	"fmt"

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

			// TODO: Call controller to create snapshot
			fmt.Printf("Creating snapshot %s of volume %s (node=%s)\n", name, volume, node)

			return nil
		},
	}

	cmd.Flags().StringVar(&volume, "volume", "", "Volume name")
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

			// TODO: Call controller to delete snapshot
			fmt.Printf("Deleting snapshot %s of volume %s (node=%s)\n", name, volume, node)

			return nil
		},
	}

	cmd.Flags().StringVar(&volume, "volume", "", "Volume name")
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
		Short: "Restore a snapshot to its source volume",
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

			// TODO: Call controller to restore snapshot
			fmt.Printf("Restoring snapshot %s to volume %s (node=%s)\n", name, volume, node)

			return nil
		},
	}

	cmd.Flags().StringVar(&volume, "volume", "", "Volume name")
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

			// TODO: Call controller to list snapshots
			fmt.Printf("Listing snapshots for volume %s (node=%s)\n", volume, node)

			return nil
		},
	}

	cmd.Flags().StringVar(&volume, "volume", "", "Volume name")
	cmd.Flags().StringVar(&node, "node", "", "Node name")

	cmd.MarkFlagRequired("volume")
	cmd.MarkFlagRequired("node")

	return cmd
}
