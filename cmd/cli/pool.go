package main

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/liliang-cn/sds/pkg/util"
	"github.com/spf13/cobra"
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
	cmd.AddCommand(poolConvertThin())

	return cmd
}

func poolCreate() *cobra.Command {
	var name string
	var poolType string
	var nodes string
	var devices string
	var size string

	cmd := &cobra.Command{
		Use:   "create",
		Short: "Create a new storage pool",
		RunE: func(cmd *cobra.Command, args []string) error {
			if name == "" {
				return fmt.Errorf("pool name is required")
			}
			if poolType == "" {
				// Thin by default. A thick pool cannot hold a snapshot history:
				// LVM makes every snapshot reserve a fixed COW area up front
				// (SDS reserves 20% of the origin), so a 10 GiB pool holding a
				// 6 GiB volume fits two snapshots — which is not a retention
				// policy. Thin snapshots cost only the blocks that diverge.
				// Pass --type lvm explicitly for the old behaviour.
				poolType = "lvm-thin"
			}
			if nodes == "" {
				return fmt.Errorf("nodes is required")
			}
			if devices == "" {
				return fmt.Errorf("devices is required (comma-separated)")
			}

			diskList := strings.Split(devices, ",")
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

			// Parse comma-separated nodes
			nodeList := strings.Split(nodes, ",")
			for i := range nodeList {
				nodeList[i] = strings.TrimSpace(nodeList[i])
			}

			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()

			sdsClient, err := newSDSClient()
			if err != nil {
				return fmt.Errorf("failed to connect to controller: %w", err)
			}
			defer sdsClient.Close()

			// Create pool on each node
			successCount := 0
			var failedNodes []string
			for _, n := range nodeList {
				var err error
				switch poolType {
				case "zfs":
					// For ZFS, 'disks' are vdevs. A zpool has no thin/thick mode;
					// thin provisioning is a per-zvol property set at volume creation.
					err = sdsClient.CreateZFSPool(ctx, name, n, diskList)
				case "vg", "lvm", "lvm-thin", "thin_pool":
					// normalize type for backend if needed, but backend supports "vg" and "thin_pool"
					// map lvm -> vg, lvm-thin -> thin_pool
					backendType := poolType
					if poolType == "lvm" {
						backendType = "vg"
					} else if poolType == "lvm-thin" {
						backendType = "thin_pool"
					}
					err = sdsClient.CreatePool(ctx, name, backendType, n, diskList, util.BytesToGiB(sizeBytes))
				default:
					err = fmt.Errorf("unsupported pool type: %s", poolType)
				}

				if err != nil {
					failedNodes = append(failedNodes, fmt.Sprintf("%s: %v", n, err))
					continue
				}
				successCount++
				if strings.HasPrefix(poolType, "zfs") {
					fmt.Printf("ZFS Pool '%s' created successfully on node '%s' (type: %s)\n", name, n, poolType)
				} else if sizeBytes > 0 {
					fmt.Printf("Pool '%s' created successfully on node '%s' (type: %s, size: %s)\n", name, n, poolType, util.FormatBytes(sizeBytes))
				} else {
					fmt.Printf("Pool '%s' created successfully on node '%s' (type: %s)\n", name, n, poolType)
				}
			}

			if len(failedNodes) > 0 {
				fmt.Fprintf(os.Stderr, "\nFailed to create pool on %d node(s):\n", len(failedNodes))
				for _, fail := range failedNodes {
					fmt.Fprintf(os.Stderr, "  - %s\n", fail)
				}
			}

			if successCount == 0 {
				return fmt.Errorf("failed to create pool on any node")
			}
			return nil
		},
	}

	cmd.Flags().StringVar(&name, "name", "", "Pool name")
	cmd.Flags().StringVar(&poolType, "type", "", "Pool type: lvm-thin (default, snapshot-capable), lvm, zfs")
	cmd.Flags().StringVar(&nodes, "nodes", "", "Comma-separated nodes where to create the pool")
	cmd.Flags().StringVar(&devices, "devices", "", "Comma-separated list of devices")
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

			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()

			sdsClient, err := newSDSClient()
			if err != nil {
				return fmt.Errorf("failed to connect to controller: %w", err)
			}
			defer sdsClient.Close()

			err = sdsClient.DeletePool(ctx, name, node)
			if err != nil {
				return fmt.Errorf("failed to delete pool: %w", err)
			}

			fmt.Printf("Pool '%s' deleted successfully on node '%s'\n", name, node)
			return nil
		},
	}

	cmd.Flags().StringVar(&name, "name", "", "Pool name")
	cmd.Flags().StringVar(&node, "node", "", "Node where the pool exists")

	_ = cmd.MarkFlagRequired("name")
	_ = cmd.MarkFlagRequired("node")

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

			sdsClient, err := newSDSClient()
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

			sdsClient, err := newSDSClient()
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
	var devices string
	var nodes string

	cmd := &cobra.Command{
		Use:   "add",
		Short: "Add devices to a pool",
		RunE: func(cmd *cobra.Command, args []string) error {
			if pool == "" {
				return fmt.Errorf("pool name is required")
			}
			if devices == "" {
				return fmt.Errorf("devices is required")
			}
			if nodes == "" {
				return fmt.Errorf("nodes is required")
			}

			deviceList := strings.Split(devices, ",")
			nodeList := strings.Split(nodes, ",")
			for i := range nodeList {
				nodeList[i] = strings.TrimSpace(nodeList[i])
			}

			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()

			sdsClient, err := newSDSClient()
			if err != nil {
				return fmt.Errorf("failed to connect to controller: %w", err)
			}
			defer sdsClient.Close()

			successCount := 0
			var failedOps []string

			for _, node := range nodeList {
				for _, device := range deviceList {
					err = sdsClient.AddDiskToPool(ctx, pool, strings.TrimSpace(device), node)
					if err != nil {
						failedOps = append(failedOps, fmt.Sprintf("%s@%s: %v", device, node, err))
						continue
					}
					successCount++
					fmt.Printf("Device '%s' added to pool '%s' on node '%s'\n", device, pool, node)
				}
			}

			if len(failedOps) > 0 {
				fmt.Fprintf(os.Stderr, "\nFailed to add %d device(s):\n", len(failedOps))
				for _, fail := range failedOps {
					fmt.Fprintf(os.Stderr, "  - %s\n", fail)
				}
			}

			if successCount == 0 {
				return fmt.Errorf("failed to add any devices")
			}
			return nil
		},
	}

	cmd.Flags().StringVar(&pool, "pool", "", "Pool name")
	cmd.Flags().StringVar(&devices, "devices", "", "Comma-separated devices to add")
	cmd.Flags().StringVar(&nodes, "nodes", "", "Comma-separated nodes")

	return cmd
}

// poolConvertThin rebuilds one node's pool as an LVM thin pool, in place.
func poolConvertThin() *cobra.Command {
	var node, pool string

	cmd := &cobra.Command{
		Use:   "convert-thin --node <node> --pool <pool>",
		Short: "Rebuild a node's LVM pool as a thin pool, in place",
		Long: `Rebuild one node's LVM pool as a thin pool without recreating its resources.

A thick pool cannot hold a snapshot history. LVM makes every snapshot reserve a
copy-on-write area up front — SDS reserves 20% of the origin — so a 10 GiB pool
backing a 6 GiB volume fits two snapshots whether or not anything ever changes.
A thin snapshot costs only the blocks that diverge.

This destroys the node's backing volumes and resyncs them in full from the
peers, so run it on ONE node at a time and let each resync finish first. The
resource keeps serving throughout: the node goes diskless for the duration and
its peers answer. The command refuses to start when the node holds a Primary,
when any peer is not UpToDate, when a resync is already running, or when the
node's copy is one of only two.

Note that the rebuilt volume ends up fully allocated — DRBD's resync writes
every block, zeroes included — so the pool is sized for the whole origin plus
headroom, not for the live data.

  sds pool convert-thin --node node-e --pool sds_sdspool`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if node == "" || pool == "" {
				return fmt.Errorf("--node and --pool are both required")
			}

			// A full resync of every volume on the node, from scratch.
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
			defer cancel()

			sdsClient, err := newSDSClient()
			if err != nil {
				return fmt.Errorf("failed to connect to controller: %w", err)
			}
			defer sdsClient.Close()

			if err := sdsClient.ConvertPoolToThin(ctx, node, pool); err != nil {
				return fmt.Errorf("failed to convert pool: %w", err)
			}

			fmt.Printf("Pool %s on %s rebuilt as thin.\n", pool, node)
			fmt.Printf("Its volumes are resyncing from their peers; watch with:\n")
			fmt.Printf("  sds resource status <resource>\n")
			fmt.Printf("Wait for every volume to read UpToDate before converting the next node.\n")
			return nil
		},
	}

	cmd.Flags().StringVar(&node, "node", "", "Node whose pool will be rebuilt")
	cmd.Flags().StringVar(&pool, "pool", "", "Pool (volume group) to rebuild as thin")
	return cmd
}
