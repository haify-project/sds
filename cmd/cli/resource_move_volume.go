package main

import (
	"context"
	"fmt"
	"time"

	haifypb "github.com/haify-project/haify/api/proto/v1"
	"github.com/spf13/cobra"
)

func resourceMoveVolumeCommand() *cobra.Command {
	var pool string
	var volume int32
	cmd := &cobra.Command{
		Use:   "move-volume <resource>",
		Short: "Move a volume to another pool, one node at a time, while it keeps serving",
		Long: `Each node's copy in turn is detached, recreated in the new pool and filled
again from a peer; the resource stays online and is never more than one copy
short. The new pool must exist on every node holding a copy. LVM snapshots of
the old volume cannot move with it and are deleted, so a resource whose
snapshots are locked is refused. A move that stops part-way carries on from
where it stopped when asked again. Follow it with: haify pool jobs`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if pool == "" {
				return fmt.Errorf("--pool is required")
			}
			return withController(time.Minute, func(ctx context.Context, c haifypb.HaifyControllerClient) error {
				r, err := c.MoveVolume(ctx, &haifypb.MoveVolumeRequest{Resource: args[0], VolumeId: volume, Pool: pool})
				if err != nil {
					return err
				}
				if r.Success {
					fmt.Printf("job %s: ", r.JobId)
				}
				return resultLine(r.Success, r.Message)
			})
		},
	}
	cmd.Flags().StringVar(&pool, "pool", "", "Destination pool")
	cmd.Flags().Int32Var(&volume, "volume", 0, "Volume number")
	return cmd
}
