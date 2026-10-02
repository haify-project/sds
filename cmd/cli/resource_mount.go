package main

import (
	"context"
	"fmt"
	"time"

	"github.com/spf13/cobra"
)

func resourceFs() *cobra.Command {
	var node string

	cmd := &cobra.Command{
		Use:   "fs <resource> <volume-id> <fstype>",
		Short: "Create filesystem on volume",
		Args:  cobra.ExactArgs(3),
		RunE: func(cmd *cobra.Command, args []string) error {
			resource := args[0]
			var volumeID uint32
			_, err := fmt.Sscanf(args[1], "%d", &volumeID)
			if err != nil {
				return fmt.Errorf("invalid volume ID: %s", args[1])
			}
			fstype := args[2]

			ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
			defer cancel()

			sdsClient, err := newSDSClient()
			if err != nil {
				return fmt.Errorf("failed to connect to controller: %w", err)
			}
			defer closeClient(sdsClient)

			err = sdsClient.CreateFilesystem(ctx, resource, volumeID, node, fstype)
			if err != nil {
				return fmt.Errorf("failed to create filesystem: %w", err)
			}

			fmt.Printf("Filesystem '%s' created on volume %d\n", fstype, volumeID)
			return nil
		},
	}

	cmd.Flags().StringVar(&node, "node", "", "Node to run mkfs on (default: the resource's current Primary)")

	return cmd
}

func resourceMount() *cobra.Command {
	var node string
	var fstype string

	cmd := &cobra.Command{
		Use:   "mount <resource> <volume-id> <mount-path>",
		Short: "Mount a DRBD volume",
		Args:  cobra.ExactArgs(3),
		RunE: func(cmd *cobra.Command, args []string) error {
			resource := args[0]
			var volumeID uint32
			_, err := fmt.Sscanf(args[1], "%d", &volumeID)
			if err != nil {
				return fmt.Errorf("invalid volume ID: %s", args[1])
			}
			mountPath := args[2]

			ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
			defer cancel()

			sdsClient, err := newSDSClient()
			if err != nil {
				return fmt.Errorf("failed to connect to controller: %w", err)
			}
			defer closeClient(sdsClient)

			err = sdsClient.MountResource(ctx, resource, volumeID, mountPath, node, fstype)
			if err != nil {
				return fmt.Errorf("failed to mount resource: %w", err)
			}

			fmt.Printf("Resource '%s' volume %d mounted at %s\n", resource, volumeID, mountPath)
			return nil
		},
	}

	cmd.Flags().StringVar(&node, "node", "", "Target node (required)")
	cmd.Flags().StringVar(&fstype, "fstype", "ext4", "Filesystem type")
	_ = cmd.MarkFlagRequired("node")

	return cmd
}

func resourceUnmount() *cobra.Command {
	var node string

	cmd := &cobra.Command{
		Use:   "unmount <resource> <volume-id>",
		Short: "Unmount a DRBD volume",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			resource := args[0]
			var volumeID uint32
			_, err := fmt.Sscanf(args[1], "%d", &volumeID)
			if err != nil {
				return fmt.Errorf("invalid volume ID: %s", args[1])
			}

			ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
			defer cancel()

			sdsClient, err := newSDSClient()
			if err != nil {
				return fmt.Errorf("failed to connect to controller: %w", err)
			}
			defer closeClient(sdsClient)

			err = sdsClient.UnmountResource(ctx, resource, volumeID, node)
			if err != nil {
				return fmt.Errorf("failed to unmount resource: %w", err)
			}

			fmt.Printf("Resource '%s' volume %d unmounted\n", resource, volumeID)
			return nil
		},
	}

	cmd.Flags().StringVar(&node, "node", "", "Target node (required)")
	_ = cmd.MarkFlagRequired("node")

	return cmd
}
