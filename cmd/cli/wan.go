package main

import (
	"context"
	"fmt"
	"time"

	"github.com/spf13/cobra"
)

func wanCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "wan",
		Short: "Cross-site (WAN) replication maintenance",
	}
	cmd.AddCommand(wanRepairCommand())
	return cmd
}

func wanRepairCommand() *cobra.Command {
	var dryRun bool

	cmd := &cobra.Command{
		Use:   "repair <resource>",
		Short: "Reconcile a WAN resource's replication tunnels with its current nodes",
		Long: "Re-provisions the WAN legs a resource should have and removes instances\n" +
			"left behind by a node that was renumbered or removed.\n\n" +
			"Leg names used to embed a node's address, so changing that address\n" +
			"orphaned the tunnel: it kept replicating under the old name while status\n" +
			"reported a phantom outage. Repair converges the two.\n\n" +
			"This restarts the tunnels, so run it with --dry-run first.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
			defer cancel()

			c, err := newSDSClient()
			if err != nil {
				return fmt.Errorf("failed to connect to controller: %w", err)
			}
			defer closeClient(c)

			resp, err := c.RepairWanProxy(ctx, args[0], dryRun)
			if err != nil {
				return err
			}

			out := cmd.OutOrStdout()
			// Writes to the command's own output stream are best-effort. The only ways
			// they fail are a closed pipe (`sds ... | head`) or a full disk, neither of
			// which this command can report anywhere the operator is still looking, and
			// treating them as errors would report a successful operation as failed.
			_, _ = fmt.Fprintln(out, "Expected legs:")
			for _, l := range resp.ExpectedLegs {
				_, _ = fmt.Fprintf(out, "  %s\n", l)
			}
			if len(resp.RemovedLegs) == 0 {
				_, _ = fmt.Fprintln(out, "\nNo stale instances.")
			} else {
				verb := "Removed"
				if dryRun {
					verb = "Would remove"
				}
				_, _ = fmt.Fprintf(out, "\n%s:\n", verb)
				for _, l := range resp.RemovedLegs {
					_, _ = fmt.Fprintf(out, "  %s\n", l)
				}
			}
			_, _ = fmt.Fprintf(out, "\n%s\n", resp.Message)
			return nil
		},
	}
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "report what would change without touching anything")
	return cmd
}
