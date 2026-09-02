package main

import (
	"context"
	"fmt"
	"time"

	"github.com/spf13/cobra"
)

// backupLimits is repeated in every long help below, because the single most
// expensive way to learn it is from a saturated uplink at 03:00.
const backupLimits = `LIMITATIONS (read before relying on this):
  * Every backup is a FULL image of the volume. There is NO incremental mode:
    a nightly backup of a 2 TiB volume transfers 2 TiB every night. Size the
    schedule and the link accordingly.
  * The stream is not compressed. That is what allows the byte-for-byte size
    check against the target after each upload.
  * LVM-backed volumes only. A ZFS zvol has no snapshot block device to read,
    so those resources are refused rather than half-supported.`

func backupCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "backup",
		Short: "Ship point-in-time copies off the cluster (S3 / SMB / WebDAV)",
		Long: `Back up DRBD resources to storage that is not part of the cluster.

This is the layer snapshots and WAN DR are not. A snapshot lives in the same
pool as its origin, so losing the machine loses both. WAN DR is a replica, so a
deletion replicates. A backup is a copy nothing in the cluster can reach.

Backups are taken from a storage-native snapshot, never from the live volume,
so the image is crash-consistent.

` + backupLimits,
	}
	cmd.AddCommand(backupTargetCommand())
	cmd.AddCommand(backupCreateCommand())
	cmd.AddCommand(backupListCommand())
	cmd.AddCommand(backupRestoreCommand())
	cmd.AddCommand(backupDeleteCommand())
	return cmd
}

func backupCreateCommand() *cobra.Command {
	var resource, target, node string
	var timeout time.Duration

	cmd := &cobra.Command{
		Use:   "create",
		Short: "Take a full, crash-consistent backup of a resource",
		Long: `Snapshot every volume of a resource and ship the images to a target.

The backup is only recorded as completed once every image has been uploaded and
the target has confirmed it holds exactly as many bytes as were sent. A run that
fails part-way is recorded as failed and its objects are removed; it is never
offered for restore.

` + backupLimits,
		RunE: func(cmd *cobra.Command, args []string) error {
			if resource == "" {
				return fmt.Errorf("--resource is required")
			}
			if target == "" {
				return fmt.Errorf("--target is required (see `sds backup target list`)")
			}

			ctx, cancel := context.WithTimeout(context.Background(), timeout)
			defer cancel()
			c, err := newSDSClient()
			if err != nil {
				return fmt.Errorf("failed to connect to controller: %w", err)
			}
			defer closeClient(c)

			info, err := c.CreateBackup(ctx, resource, target, node)
			if err != nil {
				return fmt.Errorf("backup failed: %w", err)
			}
			out := cmd.OutOrStdout()
			// Writes to the command's own output stream are best-effort. The only ways
			// they fail are a closed pipe (`sds ... | head`) or a full disk, neither of
			// which this command can report anywhere the operator is still looking, and
			// treating them as errors would report a successful operation as failed.
			_, _ = fmt.Fprintf(out, "Backup %s completed\n", info.Id)
			_, _ = fmt.Fprintf(out, "  resource: %s (from node %s)\n", info.Resource, info.Node)
			_, _ = fmt.Fprintf(out, "  target:   %s/%s\n", info.Target, info.Prefix)
			_, _ = fmt.Fprintf(out, "  size:     %s across %d volume(s)\n", humanBytes(info.TotalBytes), len(info.Volumes))
			return nil
		},
	}
	cmd.Flags().StringVar(&resource, "resource", "", "Resource to back up (required)")
	cmd.Flags().StringVar(&target, "target", "", "Backup target name (required)")
	cmd.Flags().StringVar(&node, "node", "", "Read from this replica (default: an UpToDate Secondary)")
	// A full image of a real volume takes hours, not seconds; the default RPC
	// timeout used elsewhere in this CLI would abort the transfer.
	cmd.Flags().DurationVar(&timeout, "timeout", 24*time.Hour, "Give up if the backup takes longer than this")
	return cmd
}

func backupListCommand() *cobra.Command {
	var resource, target string

	cmd := &cobra.Command{
		Use:   "list",
		Short: "List backups, newest first",
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			c, err := newSDSClient()
			if err != nil {
				return fmt.Errorf("failed to connect to controller: %w", err)
			}
			defer closeClient(c)

			backups, err := c.ListBackups(ctx, resource, target)
			if err != nil {
				return fmt.Errorf("failed to list backups: %w", err)
			}
			out := cmd.OutOrStdout()
			if len(backups) == 0 {
				_, _ = fmt.Fprintln(out, "No backups found")
				return nil
			}
			for _, b := range backups {
				_, _ = fmt.Fprintf(out, "%s  [%s]\n", b.Id, b.State)
				_, _ = fmt.Fprintf(out, "  resource=%s target=%s node=%s size=%s\n",
					b.Resource, b.Target, b.Node, humanBytes(b.TotalBytes))
				if b.StartedAt != "" {
					_, _ = fmt.Fprintf(out, "  started=%s finished=%s\n", b.StartedAt, orDash(b.FinishedAt))
				}
				if b.Error != "" {
					_, _ = fmt.Fprintf(out, "  error: %s\n", b.Error)
				}
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&resource, "resource", "", "Only backups of this resource")
	cmd.Flags().StringVar(&target, "target", "", "Only backups on this target")
	return cmd
}

func backupRestoreCommand() *cobra.Command {
	var resource, node string
	var timeout time.Duration

	cmd := &cobra.Command{
		Use:   "restore <backup-id>",
		Short: "Restore a completed backup into a resource",
		Long: `Write a backup's images back onto a resource, in place.

The destination resource must already exist, must be at least as large as the
backup, and must NOT be in use: the restore is refused while the resource is
Primary anywhere, or while a gateway exports it (a gateway can promote it in the
middle of the write). Stop the workload first.

Only a backup in the "completed" state can be restored. A run that was
interrupted or that failed verification is never restorable, by design.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, cancel := context.WithTimeout(context.Background(), timeout)
			defer cancel()
			c, err := newSDSClient()
			if err != nil {
				return fmt.Errorf("failed to connect to controller: %w", err)
			}
			defer closeClient(c)

			info, err := c.RestoreBackup(ctx, args[0], resource, node)
			if err != nil {
				return fmt.Errorf("restore failed: %w", err)
			}
			dest := resource
			if dest == "" {
				dest = info.Resource
			}
			_, _ = fmt.Fprintf(cmd.OutOrStdout(), "Backup %s restored into %q (%s across %d volume(s))\n",
				info.Id, dest, humanBytes(info.TotalBytes), len(info.Volumes))
			return nil
		},
	}
	cmd.Flags().StringVar(&resource, "resource", "", "Restore into this resource (default: the one it came from)")
	cmd.Flags().StringVar(&node, "node", "", "Write through this replica (default: any UpToDate one)")
	cmd.Flags().DurationVar(&timeout, "timeout", 24*time.Hour, "Give up if the restore takes longer than this")
	return cmd
}

func backupDeleteCommand() *cobra.Command {
	var node string
	var force bool

	cmd := &cobra.Command{
		Use:   "delete <backup-id>",
		Short: "Delete a backup's objects and its record",
		Long: `Remove a backup from its target.

The record is only dropped once the objects are gone, so a failure leaves the
backup listed and the deletion retryable rather than turning it into storage
nobody knows about. --force drops the record regardless, which is what to use
when the target itself no longer exists.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
			defer cancel()
			c, err := newSDSClient()
			if err != nil {
				return fmt.Errorf("failed to connect to controller: %w", err)
			}
			defer closeClient(c)

			if err := c.DeleteBackup(ctx, args[0], node, force); err != nil {
				return fmt.Errorf("failed to delete backup: %w", err)
			}
			_, _ = fmt.Fprintf(cmd.OutOrStdout(), "Backup %s deleted\n", args[0])
			return nil
		},
	}
	cmd.Flags().StringVar(&node, "node", "", "Reach the target from this node (default: the node that wrote it)")
	cmd.Flags().BoolVar(&force, "force", false, "Drop the record even if the objects could not be removed")
	return cmd
}

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}
