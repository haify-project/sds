package main

import (
	"context"
	"fmt"

	"github.com/liliang-cn/sds/pkg/util"
	"github.com/spf13/cobra"
)

func resourceAddVolume() *cobra.Command {
	var name string
	var volume string
	var pool string
	var size string

	cmd := &cobra.Command{
		Use:   "add-volume <resource>",
		Short: "Add a volume to resource",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			resource := args[0]

			if volume == "" {
				return fmt.Errorf("volume name is required (--volume)")
			}
			if size == "" {
				return fmt.Errorf("size is required (--size)")
			}
			if pool == "" {
				return fmt.Errorf("pool is required (--pool)")
			}

			ctx, cancel := context.WithTimeout(context.Background(), nodeOpTimeout)
			defer cancel()

			sizeBytes, err := util.ParseSize(size)
			if err != nil {
				return fmt.Errorf("invalid size format: %s: %w", size, err)
			}
			sizeGiB := util.BytesToGiB(sizeBytes)
			if sizeGiB == 0 {
				return fmt.Errorf("size too small (minimum 1 GiB)")
			}

			sdsClient, err := newSDSClient()
			if err != nil {
				return fmt.Errorf("failed to connect to controller: %w", err)
			}
			defer closeClient(sdsClient)

			err = sdsClient.AddVolume(ctx, resource, volume, pool, uint32(sizeGiB))
			if err != nil {
				return fmt.Errorf("failed to add volume: %w", err)
			}

			fmt.Printf("Volume '%s' added to '%s' (size: %s)\n", volume, resource, util.FormatBytes(sizeBytes))
			return nil
		},
	}

	cmd.Flags().StringVar(&name, "name", "", "Volume name (alias of --volume)")
	cmd.Flags().StringVar(&volume, "volume", "", "Volume name (required)")
	// Kept so existing scripts work, hidden so the help does not list two
	// required flags that mean the same thing.
	_ = cmd.Flags().MarkHidden("name")
	cmd.Flags().StringVar(&pool, "pool", "", "Storage pool (required)")
	cmd.Flags().StringVar(&size, "size", "", "Volume size (e.g., 1G, 10GB, 1TB, required)")

	// For compatibility, map --name to --volume
	cmd.PreRunE = func(cmd *cobra.Command, args []string) error {
		if name != "" && volume == "" {
			volume = name
		}
		return nil
	}

	return cmd
}

func resourceRemoveVolume() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "remove-volume <resource> <volume-id>",
		Short: "Remove a volume from resource",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			resource := args[0]
			var volumeID uint32
			_, err := fmt.Sscanf(args[1], "%d", &volumeID)
			if err != nil {
				return fmt.Errorf("invalid volume ID: %s", args[1])
			}

			ctx, cancel := context.WithTimeout(context.Background(), nodeOpTimeout)
			defer cancel()

			sdsClient, err := newSDSClient()
			if err != nil {
				return fmt.Errorf("failed to connect to controller: %w", err)
			}
			defer closeClient(sdsClient)

			err = sdsClient.RemoveVolume(ctx, resource, volumeID)
			if err != nil {
				return fmt.Errorf("failed to remove volume: %w", err)
			}

			fmt.Printf("Volume %d removed from '%s'\n", volumeID, resource)
			return nil
		},
	}

	return cmd
}

func resourceResizeVolume() *cobra.Command {
	var size string
	var ignoreFreeSpace bool

	cmd := &cobra.Command{
		Use:   "resize-volume <resource> <volume-id> <size>",
		Short: "Resize a volume",
		Args:  cobra.ExactArgs(3),
		RunE: func(cmd *cobra.Command, args []string) error {
			resource := args[0]
			var volumeID uint32
			_, err := fmt.Sscanf(args[1], "%d", &volumeID)
			if err != nil {
				return fmt.Errorf("invalid volume ID: %s", args[1])
			}
			size = args[2]

			ctx, cancel := context.WithTimeout(context.Background(), nodeOpTimeout)
			defer cancel()

			sizeBytes, err := util.ParseSize(size)
			if err != nil {
				return fmt.Errorf("invalid size format: %s: %w", size, err)
			}
			sizeGiB := util.BytesToGiB(sizeBytes)
			if sizeGiB == 0 {
				return fmt.Errorf("size too small (minimum 1 GiB)")
			}

			sdsClient, err := newSDSClient()
			if err != nil {
				return fmt.Errorf("failed to connect to controller: %w", err)
			}
			defer closeClient(sdsClient)

			resize := sdsClient.ResizeVolume
			if ignoreFreeSpace {
				resize = sdsClient.ResizeVolumeIgnoringFreeSpace
			}
			err = resize(ctx, resource, volumeID, uint32(sizeGiB))
			if err != nil {
				return fmt.Errorf("failed to resize volume: %w", err)
			}

			fmt.Printf("Volume %d resized to %s\n", volumeID, util.FormatBytes(sizeBytes))
			return nil
		},
	}
	cmd.Flags().BoolVar(&ignoreFreeSpace, "ignore-free-space", false,
		"Grow it although a replica's pool has less free space than the growth (the new area is written to every replica; a full pool drops the disk)")

	return cmd
}
