package main

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	sdspb "github.com/haify-project/sds/api/proto/v1"

	"github.com/haify-project/sds/pkg/util"
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
	cmd.AddCommand(poolAddCache())
	cmd.AddCommand(poolRemoveCache())
	addPoolUpkeepCommands(cmd)

	return cmd
}

func poolCreate() *cobra.Command {
	var name string
	var poolType string
	var nodes string
	var devices string
	var size string
	var compression string
	var dedup bool

	cmd := &cobra.Command{
		Use:   "create",
		Short: "Create a new storage pool",
		RunE: func(cmd *cobra.Command, args []string) error {
			if name == "" {
				return fmt.Errorf("pool name is required")
			}
			// An omitted type is left empty on purpose: the controller fills it
			// from storage.default_pool_type. Substituting a default here is
			// what made sds and every other client disagree about what an
			// unspecified pool is — see StorageManager.defaultedPoolType.
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

			ctx, cancel := context.WithTimeout(context.Background(), nodeOpTimeout)
			defer cancel()

			sdsClient, err := newSDSClient()
			if err != nil {
				return fmt.Errorf("failed to connect to controller: %w", err)
			}
			defer closeClient(sdsClient)

			// Create pool on each node
			successCount := 0
			var failedNodes []string
			for _, n := range nodeList {
				var err error
				switch poolType {
				case "zfs":
					// For ZFS, 'disks' are vdevs. A zpool has no thin/thick mode;
					// thin provisioning is a per-zvol property set at volume creation.
					err = sdsClient.CreateZFSPoolOptions(ctx, name, n, diskList, compression, dedup)
				// "" reaches the LVM path deliberately: an unspecified type is
				// resolved by the controller from storage.default_pool_type, and
				// that setting can only name an LVM type — ZFS pools are built by
				// a different RPC with vdevs rather than disks, so there is no
				// empty-type ZFS case to route.
				case "", "vg", "lvm", "lvm-thin", "thin-pool", "thin_pool", "lvm-thin-vdo", "thin_vdo":
					// normalize type for backend if needed, but backend supports "vg" and "thin_pool"
					// map lvm -> vg, lvm-thin -> thin_pool
					backendType := poolType
					switch poolType {
					case "lvm":
						backendType = "vg"
					case "lvm-thin":
						backendType = "thin_pool"
					case "lvm-thin-vdo":
						backendType = "thin_vdo"
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
					shown := poolType
					if shown == "" {
						shown = "controller default"
					}
					fmt.Printf("Pool '%s' created successfully on node '%s' (type: %s)\n", name, n, shown)
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
	cmd.Flags().StringVar(&poolType, "type", "", "Pool type: lvm-thin (snapshot-capable), lvm-thin-vdo (thin on VDO: dedup+compression), lvm, zfs (default: the controller's storage.default_pool_type)")
	cmd.Flags().StringVar(&nodes, "nodes", "", "Comma-separated nodes where to create the pool")
	cmd.Flags().StringVar(&devices, "devices", "", "Comma-separated list of devices")
	cmd.Flags().StringVar(&size, "size", "", "Pool size (e.g., 10G, 10GB, 10GiB, 1T, 1TB)")
	cmd.Flags().StringVar(&compression, "compression", "", "ZFS only: compression algorithm (lz4, zstd, zstd-N, gzip-N, off, ...); default: OpenZFS's (lz4 from 2.2)")
	cmd.Flags().BoolVar(&dedup, "dedup", false, "ZFS only: deduplicate (costs RAM on every write; for data known to repeat)")
	cmd.PreRunE = func(cmd *cobra.Command, _ []string) error {
		if (compression != "" || dedup) && poolType != "zfs" {
			return fmt.Errorf("--compression and --dedup apply to --type zfs only")
		}
		return nil
	}

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

			ctx, cancel := context.WithTimeout(context.Background(), nodeOpTimeout)
			defer cancel()

			sdsClient, err := newSDSClient()
			if err != nil {
				return fmt.Errorf("failed to connect to controller: %w", err)
			}
			defer closeClient(sdsClient)

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
			defer closeClient(sdsClient)

			pool, err := sdsClient.GetPool(ctx, name, node)
			if err != nil {
				return fmt.Errorf("failed to get pool: %w", err)
			}

			fmt.Printf("Pool: %s\n", pool.Name)
			kind, free, total := poolSpace(pool)
			fmt.Printf("  Type: %s\n", kind)
			fmt.Printf("  Node: %s\n", pool.Node)
			fmt.Printf("  Total: %s\n", util.FormatBytes(total))
			fmt.Printf("  Free: %s\n", util.FormatBytes(free))
			if pool.Compression != "" {
				ratio := ""
				if pool.CompressRatio > 0 {
					ratio = fmt.Sprintf(", %.2fx achieved", pool.CompressRatio)
				}
				fmt.Printf("  Compression: %s%s\n", pool.Compression, ratio)
			}
			// For a thin pool Total and Free are the thin pool's own. The
			// volume group around it is left with only the extents SDS did not
			// give the thin pool, which is worth knowing when growing it and
			// misleading as a measure of how full the pool is.
			if pool.ThinPoolLv != "" {
				fmt.Printf("  Volume group: %d GB, %d GB unallocated\n", pool.TotalGb, pool.FreeGb)
				fmt.Printf("  Thin pool: %s (%s)\n", pool.ThinPoolLv, util.FormatBytes(pool.ThinSizeBytes))
				fmt.Printf("    Data: %.2f%%  Metadata: %.2f%%\n",
					pool.ThinDataPercent, pool.ThinMetadataPercent)
				if pool.ThinOutOfSpace {
					fmt.Fprintf(os.Stderr, "    WARNING: LVM reports this pool out of data space; "+
						"writes are failing and any DRBD replica on it will drop to Diskless\n")
				}
			}
			if pool.HasVdo {
				fmt.Printf("  VDO: %.2f%% physical used, %.2f%% saved by dedup/compression\n",
					pool.VdoPhysicalPercent, pool.VdoSavingPercent)
			}
			if pool.Cached {
				fmt.Printf("  Cache: %s %s on %s\n",
					util.FormatBytes(pool.CacheSizeBytes), pool.CacheMode, pool.CacheDevice)
				fmt.Printf("    Used: %d%%  Hit rate: %d%%  Dirty: %d%%\n",
					pool.CacheUsedPercent, pool.CacheHitPercent, pool.CacheDirtyPercent)
				if pool.CacheMode == "writeback" && pool.CacheDirtyPercent > 0 {
					fmt.Printf("    %d%% of the cache is not on the slow disk yet; losing %s loses it\n",
						pool.CacheDirtyPercent, pool.CacheDevice)
				}
				if pool.CacheDegraded {
					fmt.Fprintf(os.Stderr, "    WARNING: the cache is missing a device and cannot be flushed\n")
				}
			}

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
			defer closeClient(sdsClient)

			pools, err := sdsClient.ListPools(ctx)
			if err != nil {
				return fmt.Errorf("failed to list pools: %w", err)
			}

			if len(pools) == 0 {
				fmt.Println("No pools found")
				return nil
			}

			// Pools report their node by address; the operator knows nodes by
			// name, which is what every other command takes.
			nodeNames := map[string]string{}
			if nodes, err := sdsClient.ListNodes(ctx); err == nil {
				for _, n := range nodes {
					nodeNames[n.GetAddress()] = n.GetName()
				}
			}

			fmt.Println("Pools:")
			for _, p := range pools {
				// A tiered pool has to be recognisable here, not only in `pool
				// get`: the mode is the difference between an SSD that is a
				// pure optimisation and one that is in the durability path.
				tier := ""
				if p.Cached {
					tier = fmt.Sprintf(" [cache: %s %s, %d%% hit]",
						util.FormatBytes(p.CacheSizeBytes), p.CacheMode, p.CacheHitPercent)
					if p.CacheDegraded {
						tier += " [CACHE DEGRADED]"
					}
				}
				// Likewise the pool's own fullness: "0/19 GB free" is true of
				// the volume group and true of every SDS pool ever created, so
				// on its own it tells an operator nothing.
				usage := ""
				if p.ThinPoolLv != "" {
					usage = fmt.Sprintf(", %.1f%% used", p.ThinDataPercent)
					if p.ThinOutOfSpace {
						usage += ", OUT OF SPACE"
					}
				}
				if p.HasVdo {
					usage += fmt.Sprintf(", VDO %.1f%% physical", p.VdoPhysicalPercent)
				}
				node := p.Node
				if name := nodeNames[p.Node]; name != "" {
					node = name
				}
				kind, free, total := poolSpace(p)
				fmt.Printf("  - %s (type=%s, node=%s, %s free of %s%s)%s\n",
					p.Name, kind, node, util.FormatBytes(free), util.FormatBytes(total), usage, tier)
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

			ctx, cancel := context.WithTimeout(context.Background(), nodeOpTimeout)
			defer cancel()

			sdsClient, err := newSDSClient()
			if err != nil {
				return fmt.Errorf("failed to connect to controller: %w", err)
			}
			defer closeClient(sdsClient)

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

  sds pool convert-thin --node node2 --pool sds_pool0`,
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
			defer closeClient(sdsClient)

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

// poolSpace is what a pool can still hold, in the pool's own terms. A thin
// pool lives inside a volume group whose free extents are what is left after
// the thin pool was carved out — a full-looking "1 of 10 GB" on a pool that is
// empty. Its capacity is the thin pool's size less the data it holds.
func poolSpace(p *sdspb.PoolInfo) (kind string, free, total uint64) {
	if p.GetThinPoolLv() != "" && p.GetThinSizeBytes() > 0 {
		used := uint64(float64(p.GetThinSizeBytes()) * p.GetThinDataPercent() / 100)
		if used > p.GetThinSizeBytes() {
			used = p.GetThinSizeBytes()
		}
		return "thin", p.GetThinSizeBytes() - used, p.GetThinSizeBytes()
	}
	kind = p.GetType()
	if kind == "vg" {
		kind = "lvm"
	}
	free, total = p.GetFreeBytes(), p.GetTotalBytes()
	if total == 0 {
		free, total = p.GetFreeGb()*1000*1000*1000, p.GetTotalGb()*1000*1000*1000
	}
	return kind, free, total
}
