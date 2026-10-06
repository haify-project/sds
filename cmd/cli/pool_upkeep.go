package main

import (
	"context"
	"fmt"
	"os"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/haify-project/sds/pkg/client"
	"github.com/haify-project/sds/pkg/util"
	"github.com/spf13/cobra"
)

// Pool upkeep: trimming, the disks under the pools, and taking disks out.

func addPoolUpkeepCommands(cmd *cobra.Command) {
	cmd.AddCommand(poolTrim(), poolDisks(), poolRemoveDisk(), poolReplaceDisk(), poolJobs())
}

// zfsVdevs lays disks out as the vdev list for a RAID level: raid1 is one
// mirror of all of them, raid10 mirrors of pairs, raid5/raid6 one raidz/raidz2.
func zfsVdevs(raid string, disks []string) ([]string, error) {
	need := map[string]int{"": 1, "raid1": 2, "raid10": 4, "raid5": 3, "raid6": 4}[raid]
	switch {
	case need == 0:
		return nil, fmt.Errorf("unknown raid level %q: use raid1, raid10, raid5 or raid6", raid)
	case len(disks) < need:
		return nil, fmt.Errorf("%s needs at least %d disks, %d given", raid, need, len(disks))
	case raid == "raid10" && len(disks)%2 != 0:
		return nil, fmt.Errorf("raid10 pairs disks into mirrors: give an even number, not %d", len(disks))
	}
	switch raid {
	case "raid1":
		return append([]string{"mirror"}, disks...), nil
	case "raid10":
		var out []string
		for i := 0; i+1 < len(disks); i += 2 {
			out = append(out, "mirror", disks[i], disks[i+1])
		}
		return out, nil
	case "raid5":
		return append([]string{"raidz"}, disks...), nil
	case "raid6":
		return append([]string{"raidz2"}, disks...), nil
	}
	return disks, nil
}

func poolTrim() *cobra.Command {
	var node string
	cmd := &cobra.Command{
		Use:   "trim",
		Short: "Return the space filesystems no longer use to the thin pools, now",
		Long: `Trims every mounted filesystem on a DRBD device, on the node serving it. The
discards reach every replica, so each node's thin pool gets back the blocks the
filesystem freed — and the zeros a full resync wrote. The controller also does
this on [storage.thin] trim_schedule (daily by default).`,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Minute)
			defer cancel()
			c, err := newSDSClient()
			if err != nil {
				return err
			}
			defer closeClient(c)
			resp, err := c.TrimPools(ctx, node)
			if err != nil {
				return err
			}
			w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
			_, _ = fmt.Fprintln(w, "NODE\tFILESYSTEM\tTRIMMED")
			for _, r := range resp.Results {
				got := util.FormatBytes(r.Bytes)
				if r.Error != "" {
					got = "failed: " + r.Error
				}
				_, _ = fmt.Fprintf(w, "%s\t%s\t%s\n", r.Node, r.Mount, got)
			}
			_ = w.Flush()
			fmt.Println(resp.Message)
			return nil
		},
	}
	cmd.Flags().StringVar(&node, "node", "", "Only this node (default: every online node)")
	return cmd
}

func poolDisks() *cobra.Command {
	var pool, node string
	cmd := &cobra.Command{
		Use:   "disks",
		Short: "List the disks under the pools and their health (SMART / NVMe)",
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
			defer cancel()
			c, err := newSDSClient()
			if err != nil {
				return err
			}
			defer closeClient(c)
			disks, err := c.ListPoolDisks(ctx, pool, node)
			if err != nil {
				return err
			}
			w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
			_, _ = fmt.Fprintln(w, "NODE\tPOOL\tDISK\tSIZE\tUSED\tHEALTH\tDETAIL")
			for _, d := range disks {
				_, _ = fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\t%s\n", d.Node, d.Pool, d.Device, util.FormatBytes(d.SizeBytes),
					util.FormatBytes(d.UsedBytes), d.Health, strings.TrimSpace(d.HealthDetail+" "+d.Model))
			}
			return w.Flush()
		},
	}
	cmd.Flags().StringVar(&pool, "pool", "", "Only this pool")
	cmd.Flags().StringVar(&node, "node", "", "Only this node")
	return cmd
}

func poolRemoveDisk() *cobra.Command {
	var pool, node, disk string
	cmd := &cobra.Command{
		Use:   "remove-disk",
		Short: "Move a disk's data onto the pool's other disks, then take it out",
		Long: `The data moves first (pvmove), while the pool stays in use; the disk leaves the
pool only when that is done. The other disks need room for what it holds. A
RAID pool cannot give up a disk without losing its redundancy: use replace-disk.
Follow it with: sds pool jobs`,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runJob(func(ctx context.Context, c *client.SDSClient) (string, error) {
				resp, err := c.RemovePoolDisk(ctx, pool, node, disk)
				if err != nil {
					return "", err
				}
				return fmt.Sprintf("job %s: %s", resp.JobId, resp.Message), nil
			})
		},
	}
	cmd.Flags().StringVar(&pool, "pool", "", "Pool name (required)")
	cmd.Flags().StringVar(&node, "node", "", "Node (required)")
	cmd.Flags().StringVar(&disk, "disk", "", "Disk to take out, as the pool knows it (sds pool disks)")
	return cmd
}

func poolReplaceDisk() *cobra.Command {
	var pool, node, disk, newDisk string
	cmd := &cobra.Command{
		Use:   "replace-disk",
		Short: "Move a disk's data to a new disk, then take the old one out",
		Long: `For a disk that is failing or too small. The new disk joins the pool, the old
one's data moves to it (on a RAID pool, each mirror leg is rebuilt on it), and
the old disk leaves the pool. The pool stays in use throughout.
Follow it with: sds pool jobs`,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runJob(func(ctx context.Context, c *client.SDSClient) (string, error) {
				resp, err := c.ReplacePoolDisk(ctx, pool, node, disk, newDisk)
				if err != nil {
					return "", err
				}
				return fmt.Sprintf("job %s: %s", resp.JobId, resp.Message), nil
			})
		},
	}
	cmd.Flags().StringVar(&pool, "pool", "", "Pool name (required)")
	cmd.Flags().StringVar(&node, "node", "", "Node (required)")
	cmd.Flags().StringVar(&disk, "disk", "", "Disk to replace")
	cmd.Flags().StringVar(&newDisk, "new-disk", "", "Empty disk to move its data to")
	return cmd
}

func poolJobs() *cobra.Command {
	var all bool
	cmd := &cobra.Command{
		Use:   "jobs",
		Short: "Long storage jobs: disks being emptied or replaced, volumes changing pool",
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, cancel := context.WithTimeout(context.Background(), nodeOpTimeout)
			defer cancel()
			c, err := newSDSClient()
			if err != nil {
				return err
			}
			defer closeClient(c)
			jobs, err := c.ListStorageJobs(ctx, all)
			if err != nil {
				return err
			}
			if len(jobs) == 0 {
				fmt.Println("no storage job running")
				return nil
			}
			w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
			_, _ = fmt.Fprintln(w, "JOB\tKIND\tSTATE\tSUBJECT\tPROGRESS\tSTARTED")
			for _, j := range jobs {
				_, _ = fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\n", j.Id, j.Kind, j.State, j.Subject, j.Progress,
					time.Unix(j.StartedUnix, 0).Format("01-02 15:04"))
			}
			_ = w.Flush()
			for _, j := range jobs {
				if j.Message != "" {
					fmt.Printf("%s: %s\n", j.Id, j.Message)
				}
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&all, "all", false, "Include finished jobs (kept 14 days)")
	return cmd
}

// runJob connects, starts a job and prints what it said.
func runJob(start func(context.Context, *client.SDSClient) (string, error)) error {
	ctx, cancel := context.WithTimeout(context.Background(), nodeOpTimeout)
	defer cancel()
	c, err := newSDSClient()
	if err != nil {
		return err
	}
	defer closeClient(c)
	msg, err := start(ctx, c)
	if err != nil {
		return err
	}
	fmt.Println(msg)
	return nil
}
