package main

import (
	"context"
	"fmt"
	"strings"
	"time"

	sdspb "github.com/haify-project/sds/api/proto/v1"
	"github.com/spf13/cobra"
)

// resourceSnapshot manages snapshots for DRBD resources
func resourceSnapshot() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "snapshot",
		Short: "Snapshot management for DRBD resources",
	}

	cmd.AddCommand(resourceSnapshotCreate())
	cmd.AddCommand(resourceSnapshotList())
	cmd.AddCommand(resourceSnapshotRestore())
	cmd.AddCommand(resourceSnapshotDelete())
	cmd.AddCommand(resourceSnapshotSchedule())

	return cmd
}

// resourceSnapshotSchedule manages cron-driven snapshot schedules with GFS retention.
func resourceSnapshotSchedule() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "schedule",
		Short: "Scheduled snapshots with GFS retention",
	}
	cmd.AddCommand(resourceSnapshotScheduleCreate())
	cmd.AddCommand(resourceSnapshotScheduleList())
	cmd.AddCommand(resourceSnapshotScheduleDelete())
	return cmd
}

func resourceSnapshotScheduleCreate() *cobra.Command {
	var resource, cronExpr string
	var hourly, daily, weekly, monthly, yearly int
	var disabled bool
	var lockDays uint32

	cmd := &cobra.Command{
		Use:   "create",
		Short: "Create or replace a snapshot schedule for a resource",
		Long: `Create or replace a snapshot schedule for a resource.

--lock-days N locks every snapshot the schedule takes for N days: retention, a
full thin pool and API deletes all leave it alone until then, and the schedule
and resource cannot be deleted, nor the lock lowered, while any is locked. It is
what keeps the clean snapshots from before a volume was encrypted: a pool
filling fast used to give up its oldest snapshots first. Size the pool for N
days of change; a pool that fills with locked snapshots raises a critical
pool.snapshots_locked event. Root on a storage node can still remove them —
for that, back up to a locked S3 target. Without the flag a replaced schedule
keeps its lock.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if resource == "" {
				return fmt.Errorf("--resource is required")
			}
			if cronExpr == "" {
				return fmt.Errorf("--cron is required (standard 5-field cron, e.g. \"0 * * * *\")")
			}

			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			sdsClient, err := newSDSClient()
			if err != nil {
				return fmt.Errorf("failed to connect to controller: %w", err)
			}
			defer closeClient(sdsClient)

			keep := &sdspb.GFSRetention{
				Hourly:  int32(hourly),
				Daily:   int32(daily),
				Weekly:  int32(weekly),
				Monthly: int32(monthly),
				Yearly:  int32(yearly),
			}
			var lock *uint32
			if cmd.Flags().Changed("lock-days") {
				lock = &lockDays
			}
			if err := sdsClient.CreateSnapshotScheduleLocked(ctx, resource, cronExpr, keep, !disabled, lock); err != nil {
				return fmt.Errorf("failed to create snapshot schedule: %w", err)
			}
			fmt.Printf("Snapshot schedule for %q created (cron=%q)\n", resource, cronExpr)
			return nil
		},
	}
	cmd.Flags().StringVar(&resource, "resource", "", "Target resource (required)")
	cmd.Flags().StringVar(&cronExpr, "cron", "", "Standard 5-field cron expression (required)")
	cmd.Flags().IntVar(&hourly, "keep-hourly", 0, "Hourly snapshots to retain")
	cmd.Flags().IntVar(&daily, "keep-daily", 0, "Daily snapshots to retain")
	cmd.Flags().IntVar(&weekly, "keep-weekly", 0, "Weekly snapshots to retain")
	cmd.Flags().IntVar(&monthly, "keep-monthly", 0, "Monthly snapshots to retain")
	cmd.Flags().IntVar(&yearly, "keep-yearly", 0, "Yearly snapshots to retain")
	cmd.Flags().BoolVar(&disabled, "disabled", false, "Create the schedule disabled")
	cmd.Flags().Uint32Var(&lockDays, "lock-days", 0, "Lock every snapshot the schedule takes for this many days (see above)")
	return cmd
}

func resourceSnapshotScheduleList() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List snapshot schedules",
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			sdsClient, err := newSDSClient()
			if err != nil {
				return fmt.Errorf("failed to connect to controller: %w", err)
			}
			defer closeClient(sdsClient)

			schedules, err := sdsClient.ListSnapshotSchedules(ctx)
			if err != nil {
				return fmt.Errorf("failed to list snapshot schedules: %w", err)
			}
			if len(schedules) == 0 {
				fmt.Println("No snapshot schedules found")
				return nil
			}
			for _, s := range schedules {
				state := "enabled"
				if !s.Enabled {
					state = "disabled"
				}
				k := s.Keep
				fmt.Printf("%s (resource=%s, cron=%q, %s)\n", s.Name, s.Resource, s.Cron, state)
				fmt.Printf("  keep: hourly=%d daily=%d weekly=%d monthly=%d yearly=%d\n",
					k.GetHourly(), k.GetDaily(), k.GetWeekly(), k.GetMonthly(), k.GetYearly())
				if s.LastRun != "" {
					fmt.Printf("  last run: %s\n", s.LastRun)
				}
				if s.NextRun != "" {
					fmt.Printf("  next run: %s\n", s.NextRun)
				}
				if s.LockDays > 0 {
					fmt.Printf("  snapshots locked for %d days", s.LockDays)
					if s.LockedUntil != "" {
						fmt.Printf("; newest locked until %s", s.LockedUntil)
					}
					fmt.Println()
				}
			}
			return nil
		},
	}
	return cmd
}

func resourceSnapshotScheduleDelete() *cobra.Command {
	var name, resourceFlag string
	cmd := &cobra.Command{
		Use:   "delete",
		Short: "Delete a snapshot schedule (existing snapshots are kept)",
		RunE: func(cmd *cobra.Command, args []string) error {
			if name == "" {
				name = resourceFlag
			}
			if name == "" {
				return fmt.Errorf("--resource is required")
			}
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			sdsClient, err := newSDSClient()
			if err != nil {
				return fmt.Errorf("failed to connect to controller: %w", err)
			}
			defer closeClient(sdsClient)

			if err := sdsClient.DeleteSnapshotSchedule(ctx, name); err != nil {
				return fmt.Errorf("failed to delete snapshot schedule: %w", err)
			}
			fmt.Printf("Snapshot schedule %q deleted\n", name)
			return nil
		},
	}
	// --resource matches schedule create; --name is the older spelling.
	cmd.Flags().StringVar(&resourceFlag, "resource", "", "Resource whose schedule to delete (required)")
	cmd.Flags().StringVar(&name, "name", "", "Same as --resource")
	_ = cmd.Flags().MarkHidden("name")
	return cmd
}

func resourceSnapshotDelete() *cobra.Command {
	var resource string
	var snapshotName string
	var node string
	var storageType string
	var pool string

	cmd := &cobra.Command{
		Use:   "delete",
		Short: "Delete a snapshot",
		RunE: func(cmd *cobra.Command, args []string) error {
			if resource == "" {
				return fmt.Errorf("resource name is required")
			}
			if snapshotName == "" {
				return fmt.Errorf("snapshot name is required")
			}
			if pool == "" || node == "" {
				var err error
				if pool, node, err = snapshotTarget(resource, pool, node); err != nil {
					return err
				}
			}

			ctx, cancel := context.WithTimeout(context.Background(), nodeOpTimeout)
			defer cancel()

			sdsClient, err := newSDSClient()
			if err != nil {
				return fmt.Errorf("failed to connect to controller: %w", err)
			}
			defer closeClient(sdsClient)

			if storageType == "zfs" {
				// ZFS snapshot
				// ZFS snapshot names are unique per dataset, usually passed as snapshot name only to destroy?
				// But DeleteZFSSnapshot in backend takes 'snapshot' arg.
				// Is it "snap1" or "pool/dataset@snap1"?
				// pkg/deployment/deployment.go ZFSDestroySnapshot: "sudo zfs destroy %s"
				// So it needs FULL path.
				snapshotPath := fmt.Sprintf("%s/%s_data@%s", pool, resource, snapshotName)
				err = sdsClient.DeleteZFSSnapshot(ctx, snapshotPath, node)
				if err != nil {
					return fmt.Errorf("failed to delete ZFS snapshot: %w", err)
				}
				fmt.Printf("ZFS snapshot '%s' deleted on node '%s'\n", snapshotName, node)
			} else {
				// LVM snapshot
				// Pass pool as VG name
				err = sdsClient.DeleteLvmSnapshot(ctx, pool, snapshotName, node)
				if err != nil {
					return fmt.Errorf("failed to delete LVM snapshot: %w", err)
				}
				fmt.Printf("LVM snapshot '%s' deleted on node '%s'\n", snapshotName, node)
			}

			return nil
		},
	}

	cmd.Flags().StringVar(&resource, "resource", "", "DRBD resource name")
	cmd.Flags().StringVar(&snapshotName, "name", "", "Snapshot name")
	cmd.Flags().StringVar(&node, "node", "", "Node where resource exists")
	cmd.Flags().StringVar(&storageType, "storage-type", "lvm", "Storage type: lvm or zfs")
	cmd.Flags().StringVar(&pool, "pool", "", "Storage pool name (default: the resource's own pool)")

	_ = cmd.MarkFlagRequired("resource")
	_ = cmd.MarkFlagRequired("name")

	return cmd
}

func resourceSnapshotCreate() *cobra.Command {
	var resource string
	var snapshotName string
	var node string
	var size string
	var storageType string
	var pool string

	cmd := &cobra.Command{
		Use:   "create",
		Short: "Create a snapshot of DRBD resource",
		RunE: func(cmd *cobra.Command, args []string) error {
			if resource == "" {
				return fmt.Errorf("resource name is required")
			}
			if snapshotName == "" {
				return fmt.Errorf("snapshot name is required")
			}
			if pool == "" || node == "" {
				var err error
				if pool, node, err = snapshotTarget(resource, pool, node); err != nil {
					return err
				}
			}

			ctx, cancel := context.WithTimeout(context.Background(), nodeOpTimeout)
			defer cancel()

			sdsClient, err := newSDSClient()
			if err != nil {
				return fmt.Errorf("failed to connect to controller: %w", err)
			}
			defer closeClient(sdsClient)

			if storageType == "zfs" {
				// ZFS snapshot: pool/resource_data@snapshot
				dataset := fmt.Sprintf("%s/%s_data", pool, resource)
				err = sdsClient.CreateZFSSnapshot(ctx, dataset, snapshotName, node)
				if err != nil {
					return fmt.Errorf("failed to create ZFS snapshot: %w", err)
				}
				fmt.Printf("ZFS snapshot '%s' created for resource '%s' on node '%s'\n", snapshotName, resource, node)
			} else {
				// LVM snapshot (default)
				if size == "" {
					size = "1G"
				}
				lvName := fmt.Sprintf("%s_data", resource)
				// Pass pool as the VG name (first argument)
				err = sdsClient.CreateLvmSnapshot(ctx, pool, lvName, snapshotName, node, size)
				if err != nil {
					return fmt.Errorf("failed to create LVM snapshot: %w", err)
				}
				fmt.Printf("LVM snapshot '%s' created for resource '%s' on node '%s'\n", snapshotName, resource, node)
			}

			return nil
		},
	}

	cmd.Flags().StringVar(&resource, "resource", "", "DRBD resource name")
	cmd.Flags().StringVar(&snapshotName, "name", "", "Snapshot name")
	cmd.Flags().StringVar(&node, "node", "", "Node where resource exists")
	cmd.Flags().StringVar(&size, "size", "1G", "Snapshot size for LVM (e.g., 1G)")
	cmd.Flags().StringVar(&storageType, "storage-type", "lvm", "Storage type: lvm or zfs")
	cmd.Flags().StringVar(&pool, "pool", "", "Storage pool name (default: the resource's own pool)")

	_ = cmd.MarkFlagRequired("resource")
	_ = cmd.MarkFlagRequired("name")

	return cmd
}

func resourceSnapshotList() *cobra.Command {
	var resource string
	var node string
	var storageType string
	var pool string

	cmd := &cobra.Command{
		Use:   "list",
		Short: "List snapshots for DRBD resource",
		RunE: func(cmd *cobra.Command, args []string) error {
			if resource == "" {
				return fmt.Errorf("resource name is required")
			}
			if pool == "" || node == "" {
				var err error
				if pool, node, err = snapshotTarget(resource, pool, node); err != nil {
					return err
				}
			}

			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()

			sdsClient, err := newSDSClient()
			if err != nil {
				return fmt.Errorf("failed to connect to controller: %w", err)
			}
			defer closeClient(sdsClient)

			if storageType == "zfs" {
				// ZFS snapshots
				dataset := fmt.Sprintf("%s/%s_data", pool, resource)
				snapshots, err := sdsClient.ListZFSSnapshots(ctx, dataset, node)
				if err != nil {
					return fmt.Errorf("failed to list ZFS snapshots: %w", err)
				}

				if len(snapshots) == 0 {
					fmt.Printf("No ZFS snapshots found for resource '%s'\n", resource)
					return nil
				}

				fmt.Printf("ZFS snapshots for resource '%s':\n", resource)
				for _, snap := range snapshots {
					fmt.Printf("  - %s (created: %s)\n", snap.Name, snap.CreatedAt)
				}
			} else {
				// LVM snapshots. The pool is the volume group; resource is what
				// narrows the group's snapshots down to this one's volumes.
				snapshots, err := sdsClient.ListLvmSnapshots(ctx, pool, node, resource)
				if err != nil {
					return fmt.Errorf("failed to list LVM snapshots: %w", err)
				}

				if len(snapshots) == 0 {
					fmt.Printf("No LVM snapshots found for resource '%s'\n", resource)
					return nil
				}

				fmt.Printf("LVM snapshots for resource '%s':\n", resource)
				fmt.Printf("  %-45s %-8s %-14s %s\n", "Name", "Size", "Origin", "Created")
				fmt.Printf("  %-45s %-8s %-14s %s\n",
					strings.Repeat("-", 45), strings.Repeat("-", 8),
					strings.Repeat("-", 14), strings.Repeat("-", 25))
				for _, snap := range snapshots {
					fmt.Printf("  %-45s %-8s %-14s %s\n",
						snap.Name,
						fmt.Sprintf("%d GB", snap.SizeGb),
						snap.Origin,
						snap.CreatedAt)
				}
			}

			return nil
		},
	}

	cmd.Flags().StringVar(&resource, "resource", "", "DRBD resource name")
	cmd.Flags().StringVar(&node, "node", "", "Node where resource exists")
	cmd.Flags().StringVar(&storageType, "storage-type", "lvm", "Storage type: lvm or zfs")
	cmd.Flags().StringVar(&pool, "pool", "", "Storage pool name (default: the resource's own pool)")

	_ = cmd.MarkFlagRequired("resource")

	return cmd
}

func resourceSnapshotRestore() *cobra.Command {
	var resource string
	var snapshotName string
	var node string
	var storageType string
	var pool string

	cmd := &cobra.Command{
		Use:   "restore",
		Short: "Restore DRBD resource from snapshot",
		RunE: func(cmd *cobra.Command, args []string) error {
			if resource == "" {
				return fmt.Errorf("resource name is required")
			}
			if snapshotName == "" {
				return fmt.Errorf("snapshot name is required")
			}
			if pool == "" || node == "" {
				var err error
				if pool, node, err = snapshotTarget(resource, pool, node); err != nil {
					return err
				}
			}

			ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
			defer cancel()

			sdsClient, err := newSDSClient()
			if err != nil {
				return fmt.Errorf("failed to connect to controller: %w", err)
			}
			defer closeClient(sdsClient)

			if storageType == "zfs" {
				// ZFS rollback
				dataset := fmt.Sprintf("%s/%s_data", pool, resource)
				err = sdsClient.RestoreZFSSnapshot(ctx, dataset, snapshotName, node)
				if err != nil {
					return fmt.Errorf("failed to restore ZFS snapshot: %w", err)
				}
				fmt.Printf("ZFS snapshot '%s' restored for resource '%s' on node '%s'\n", snapshotName, resource, node)
			} else {
				// LVM snapshot restore (merge)
				// Pass pool as VG name
				err = sdsClient.RestoreLvmSnapshot(ctx, pool, snapshotName, node)
				if err != nil {
					return fmt.Errorf("failed to restore LVM snapshot: %w", err)
				}
				fmt.Printf("LVM snapshot '%s' restored for resource '%s' on node '%s'\n", snapshotName, resource, node)
				fmt.Println("Note: The snapshot has been merged back into the original volume. The resource's")
				fmt.Println("other replicas are resyncing from this node; until that finishes it has one")
				fmt.Printf("complete copy. Follow it with: sds resource status %s\n", resource)
			}

			return nil
		},
	}

	cmd.Flags().StringVar(&resource, "resource", "", "DRBD resource name")
	cmd.Flags().StringVar(&snapshotName, "name", "", "Snapshot name")
	cmd.Flags().StringVar(&node, "node", "", "Node where resource exists")
	cmd.Flags().StringVar(&storageType, "storage-type", "lvm", "Storage type: lvm or zfs")
	cmd.Flags().StringVar(&pool, "pool", "", "Storage pool name (default: the resource's own pool)")

	_ = cmd.MarkFlagRequired("resource")
	_ = cmd.MarkFlagRequired("name")

	return cmd
}

// snapshotTarget fills in what a snapshot command left out from the resource
// itself: the pool its volumes live in, and a node holding a replica. The pool
// used to default to "data-pool", a name no cluster is required to have, so
// every snapshot command failed unless --pool was given — although the
// controller has always known which pool a resource is in.
func snapshotTarget(resource, pool, node string) (string, string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), nodeOpTimeout)
	defer cancel()
	c, err := newSDSClient()
	if err != nil {
		return "", "", fmt.Errorf("failed to connect to controller: %w", err)
	}
	defer closeClient(c)
	info, err := c.GetResource(ctx, resource)
	if err != nil {
		return "", "", fmt.Errorf("look up resource %s: %w", resource, err)
	}
	if pool == "" {
		for _, v := range info.GetVolumes() {
			if v.GetPool() != "" {
				pool = strings.TrimPrefix(v.GetPool(), "sds_")
				break
			}
		}
		if pool == "" {
			return "", "", fmt.Errorf("resource %s does not record its pool; pass --pool", resource)
		}
	}
	if node == "" {
		if nodes := info.GetNodes(); len(nodes) > 0 {
			node = nodes[0]
		} else {
			return "", "", fmt.Errorf("resource %s has no replica node; pass --node", resource)
		}
	}
	return pool, node, nil
}
