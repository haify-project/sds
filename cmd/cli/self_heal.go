package main

import (
	"context"
	"fmt"
	"os"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	haifypb "github.com/haify-project/haify/api/proto/v1"
)

// Self-healing commands: moving a replica, rebalancing by plan, and dealing
// with a node after it was evicted (docs/user-guide.md, "Self-healing").

func withController(timeout time.Duration, fn func(ctx context.Context, c haifypb.HaifyControllerClient) error) error {
	c, conn, err := newResourceGRPCClient()
	if err != nil {
		return err
	}
	defer func() { _ = conn.Close() }()
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	return fn(ctx, c)
}

func resultLine(ok bool, msg string) error {
	if !ok {
		return fmt.Errorf("%s", msg)
	}
	fmt.Println(msg)
	return nil
}

func resourceMoveReplicaCommand() *cobra.Command {
	var from, to string
	cmd := &cobra.Command{
		Use:   "move-replica <resource>",
		Short: "Move a replica to another node: the new one syncs before the old one goes",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if from == "" || to == "" {
				return fmt.Errorf("--from and --to are required")
			}
			return withController(30*time.Minute, func(ctx context.Context, c haifypb.HaifyControllerClient) error {
				r, err := c.MoveReplica(ctx, &haifypb.MoveReplicaRequest{Resource: args[0], From: from, To: to})
				if err != nil {
					return err
				}
				return resultLine(r.Success, r.Message)
			})
		},
	}
	cmd.Flags().StringVar(&from, "from", "", "Node whose replica moves")
	cmd.Flags().StringVar(&to, "to", "", "Node that gets it")
	return cmd
}

func rebalanceCommand() *cobra.Command {
	var apply bool
	var maxMoves uint32
	cmd := &cobra.Command{
		Use:   "rebalance",
		Short: "Propose (or with --apply, run) replica moves from the fullest node",
		RunE: func(cmd *cobra.Command, args []string) error {
			return withController(time.Minute, func(ctx context.Context, c haifypb.HaifyControllerClient) error {
				r, err := c.PlanRebalance(ctx, &haifypb.PlanRebalanceRequest{Apply: apply, MaxMoves: maxMoves})
				if err != nil {
					return err
				}
				if len(r.Moves) > 0 {
					w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
					_, _ = fmt.Fprintln(w, "RESOURCE\tFROM\tTO\tSIZE")
					for _, m := range r.Moves {
						_, _ = fmt.Fprintf(w, "%s\t%s\t%s\t%d GiB\n", m.Resource, m.From, m.To, m.SizeGb)
					}
					_ = w.Flush()
				}
				for _, n := range r.Notes {
					fmt.Println("note:", n)
				}
				return resultLine(r.Success, r.Message)
			})
		},
	}
	cmd.Flags().BoolVar(&apply, "apply", false, "Run the moves, one at a time")
	cmd.Flags().Uint32Var(&maxMoves, "max-moves", 0, "At most this many moves (default 10)")
	return cmd
}

func nodeLostCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "lost <node>",
		Short: "Remove every replica of a node that will not come back, and keep it evicted",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return withController(30*time.Minute, func(ctx context.Context, c haifypb.HaifyControllerClient) error {
				r, err := c.MarkNodeLost(ctx, &haifypb.MarkNodeLostRequest{Node: args[0]})
				if err != nil {
					return err
				}
				for _, res := range r.Resources {
					fmt.Println("removed the replica of", res)
				}
				return resultLine(r.Success, r.Message)
			})
		},
	}
}

func nodeRestoreCommand() *cobra.Command {
	var dryRun bool
	cmd := &cobra.Command{
		Use:   "restore <node>",
		Short: "Clean what a returned node holds of resources it left, and let it take replicas again",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return withController(30*time.Minute, func(ctx context.Context, c haifypb.HaifyControllerClient) error {
				r, err := c.RestoreNode(ctx, &haifypb.RestoreNodeRequest{Node: args[0], DryRun: dryRun})
				if err != nil {
					return err
				}
				for _, p := range r.Plan {
					fmt.Println("-", p)
				}
				return resultLine(r.Success, r.Message)
			})
		},
	}
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "Only show what would be deleted")
	return cmd
}

func haSetPreferredCommand() *cobra.Command {
	var nodes []string
	var policy string
	cmd := &cobra.Command{
		Use:   "set-preferred <resource>",
		Short: "Order where drbd-reactor starts the resource (a preference, not a fence)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return withController(time.Minute, func(ctx context.Context, c haifypb.HaifyControllerClient) error {
				r, err := c.SetHaPreferredNodes(ctx, &haifypb.SetHaPreferredNodesRequest{Resource: args[0], Nodes: nodes, Policy: policy})
				if err != nil {
					return err
				}
				return resultLine(r.Success, r.Message)
			})
		},
	}
	cmd.Flags().StringSliceVar(&nodes, "nodes", nil, "Nodes in order of preference; none removes the preference")
	cmd.Flags().StringVar(&policy, "policy", "", "start-only (pick where it starts; drbd-reactor 1.9+) or always (also move it back)")
	return cmd
}
