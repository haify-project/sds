package main

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/liliang-cn/sds/pkg/client"
	"github.com/liliang-cn/sds/pkg/util"
)

func poolCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "pool",
		Short: "Pool management (storage pools on nodes)",
	}

	cmd.AddCommand(poolCreate())
	cmd.AddCommand(poolDelete())
	cmd.AddCommand(poolGet())
	cmd.AddCommand(poolList())
	cmd.AddCommand(poolAddDisk())

	return cmd
}

func poolCreate() *cobra.Command {
	var name string
	var poolType string
	var node string
	var disks string
	var size string

	cmd := &cobra.Command{
		Use:   "create",
		Short: "Create a new storage pool",
		RunE: func(cmd *cobra.Command, args []string) error {
			if name == "" {
				return fmt.Errorf("pool name is required")
			}
			if poolType == "" {
				poolType = "vg"
			}
			if node == "" {
				return fmt.Errorf("node is required")
			}
			if disks == "" {
				return fmt.Errorf("disks is required (comma-separated)")
			}

			diskList := strings.Split(disks, ",")
			var sizeBytes uint64 = 0
			if size != "" {
				var err error
				sizeBytes, err = util.ParseSize(size)
				if err != nil {
					return fmt.Errorf("invalid size format: %s: %w", size, err)
				}
				if sizeBytes == 0 {
					return fmt.Errorf("size must be greater than 0")
				}
			}

			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()

			sdsClient, err := client.NewSDSClient(controllerAddr)
			if err != nil {
				return fmt.Errorf("failed to connect to controller: %w", err)
			}
			defer sdsClient.Close()

			err = sdsClient.CreatePool(ctx, name, poolType, node, diskList, util.BytesToGiB(sizeBytes))
			if err != nil {
				return fmt.Errorf("failed to create pool: %w", err)
			}

			if sizeBytes > 0 {
				fmt.Printf("Pool '%s' created successfully on node '%s' (size: %s)\n", name, node, util.FormatBytes(sizeBytes))
			} else {
				fmt.Printf("Pool '%s' created successfully on node '%s'\n", name, node)
			}
			return nil
		},
	}

	cmd.Flags().StringVar(&name, "name", "", "Pool name")
	cmd.Flags().StringVar(&poolType, "type", "", "Pool type (vg, thin_pool)")
	cmd.Flags().StringVar(&node, "node", "", "Node where to create the pool")
	cmd.Flags().StringVar(&disks, "disks", "", "Comma-separated list of disks")
	cmd.Flags().StringVar(&size, "size", "", "Pool size (e.g., 10G, 10GB, 10GiB, 1T, 1TB)")

	return cmd
}

func poolDelete() *cobra.Command {
	var name string
	var node string

	cmd := &cobra.Command{
		Use:   "delete",
		Short: "Delete a storage pool",
		RunE: func(cmd *cobra.Command, args []string) error {
			if name == "" {
				return fmt.Errorf("pool name is required")
			}
			if node == "" {
				return fmt.Errorf("node is required")
			}

			fmt.Printf("Delete pool '%s' on node '%s' - not yet implemented\n", name, node)
			return nil
		},
	}

	cmd.Flags().StringVar(&name, "name", "", "Pool name")
	cmd.Flags().StringVar(&node, "node", "", "Node where the pool exists")

	return cmd
}

func poolGet() *cobra.Command {
	var name string
	var node string

	cmd := &cobra.Command{
		Use:   "get",
		Short: "Get pool information",
		RunE: func(cmd *cobra.Command, args []string) error {
			if name == "" {
				return fmt.Errorf("pool name is required")
			}
			if node == "" {
				return fmt.Errorf("node is required")
			}

			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()

			sdsClient, err := client.NewSDSClient(controllerAddr)
			if err != nil {
				return fmt.Errorf("failed to connect to controller: %w", err)
			}
			defer sdsClient.Close()

			pool, err := sdsClient.GetPool(ctx, name, node)
			if err != nil {
				return fmt.Errorf("failed to get pool: %w", err)
			}

			fmt.Printf("Pool: %s\n", pool.Name)
			fmt.Printf("  Type: %s\n", pool.Type)
			fmt.Printf("  Node: %s\n", pool.Node)
			fmt.Printf("  Total: %d GB (%s)\n", pool.TotalGb, util.FormatBytes(pool.TotalGb*1000*1000*1000))
			fmt.Printf("  Free: %d GB (%s)\n", pool.FreeGb, util.FormatBytes(pool.FreeGb*1000*1000*1000))

			return nil
		},
	}

	cmd.Flags().StringVar(&name, "name", "", "Pool name")
	cmd.Flags().StringVar(&node, "node", "", "Node where the pool exists")

	return cmd
}

func poolList() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List all pools",
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()

			sdsClient, err := client.NewSDSClient(controllerAddr)
			if err != nil {
				return fmt.Errorf("failed to connect to controller: %w", err)
			}
			defer sdsClient.Close()

			pools, err := sdsClient.ListPools(ctx)
			if err != nil {
				return fmt.Errorf("failed to list pools: %w", err)
			}

			if len(pools) == 0 {
				fmt.Println("No pools found")
				return nil
			}

			fmt.Println("Pools:")
			for _, p := range pools {
				fmt.Printf("  - %s (type=%s, node=%s, %d/%d GB free - %s)\n",
					p.Name, p.Type, p.Node, p.FreeGb, p.TotalGb,
					util.FormatBytes(p.FreeGb*1000*1000*1000))
			}

			return nil
		},
	}

	return cmd
}

func poolAddDisk() *cobra.Command {
	var pool string
	var disk string
	var node string

	cmd := &cobra.Command{
		Use:   "add-disk",
		Short: "Add a disk to a pool",
		RunE: func(cmd *cobra.Command, args []string) error {
			if pool == "" {
				return fmt.Errorf("pool name is required")
			}
			if disk == "" {
				return fmt.Errorf("disk is required")
			}
			if node == "" {
				return fmt.Errorf("node is required")
			}

			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()

			sdsClient, err := client.NewSDSClient(controllerAddr)
			if err != nil {
				return fmt.Errorf("failed to connect to controller: %w", err)
			}
			defer sdsClient.Close()

			err = sdsClient.AddDiskToPool(ctx, pool, disk, node)
			if err != nil {
				return fmt.Errorf("failed to add disk: %w", err)
			}

			fmt.Printf("Disk '%s' added to pool '%s'\n", disk, pool)
			return nil
		},
	}

	cmd.Flags().StringVar(&pool, "pool", "", "Pool name")
	cmd.Flags().StringVar(&disk, "disk", "", "Disk device (e.g., /dev/sdb)")
	cmd.Flags().StringVar(&node, "node", "", "Node where the pool exists")

	return cmd
}
