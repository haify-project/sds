package main

import (
	"context"
	"fmt"
	"time"

	"github.com/spf13/cobra"
)

// A frozen schedule keeps snapshotting, prunes nothing, and locks every
// scheduled snapshot of the resource until the freeze ends. The write-anomaly
// detector freezes one when a volume is suddenly rewritten; these freeze one
// by hand, or lift a freeze once the cause is known.

func resourceSnapshotScheduleFreeze() *cobra.Command {
	var resource, reason string
	var hours uint32
	cmd := &cobra.Command{
		Use:   "freeze",
		Short: "Stop all pruning and lock every scheduled snapshot of a resource for a while",
		RunE: func(cmd *cobra.Command, args []string) error {
			if resource == "" {
				return fmt.Errorf("--resource is required")
			}
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			haifyClient, err := newHaifyClient()
			if err != nil {
				return fmt.Errorf("failed to connect to controller: %w", err)
			}
			defer closeClient(haifyClient)
			until, err := haifyClient.FreezeSnapshotSchedule(ctx, resource, hours, reason)
			if err != nil {
				return fmt.Errorf("failed to freeze snapshot schedule: %w", err)
			}
			fmt.Printf("Snapshot schedule of %q frozen until %s\n", resource, until)
			return nil
		},
	}
	cmd.Flags().StringVar(&resource, "resource", "", "Resource whose schedule to freeze")
	cmd.Flags().Uint32Var(&hours, "hours", 0, "How long (default 168, a week); an existing longer freeze is kept")
	cmd.Flags().StringVar(&reason, "reason", "", "Why, shown in schedule list")
	return cmd
}

func resourceSnapshotScheduleUnfreeze() *cobra.Command {
	var resource string
	cmd := &cobra.Command{
		Use:   "unfreeze",
		Short: "End a freeze early (needs a second person's approval when [rbac.approval] is on)",
		RunE: func(cmd *cobra.Command, args []string) error {
			if resource == "" {
				return fmt.Errorf("--resource is required")
			}
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			haifyClient, err := newHaifyClient()
			if err != nil {
				return fmt.Errorf("failed to connect to controller: %w", err)
			}
			defer closeClient(haifyClient)
			if err := haifyClient.UnfreezeSnapshotSchedule(ctx, resource); err != nil {
				return fmt.Errorf("failed to unfreeze snapshot schedule: %w", err)
			}
			fmt.Printf("Snapshot schedule of %q unfrozen\n", resource)
			return nil
		},
	}
	cmd.Flags().StringVar(&resource, "resource", "", "Resource whose schedule to unfreeze")
	return cmd
}
