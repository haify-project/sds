package main

import (
	"context"
	"fmt"
	"time"

	"github.com/spf13/cobra"

	haifypb "github.com/haify-project/haify/api/proto/v1"
)

// Resource snapshots: every volume on every diskful replica at the same
// instant, with I/O suspended across them, so a rollback brings all replicas
// back together and resyncs nothing. `haify resource snapshot create` takes one
// on one node; this takes one of the whole resource.

func resourceSnapshotReplicated() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "replicated",
		Short: "Snapshots of a whole resource on every replica (rollback without resync)",
	}
	var resource, name string
	run := func(use, short string, needName bool, fn func(ctx context.Context, c haifypb.HaifyControllerClient) error) *cobra.Command {
		sub := &cobra.Command{
			Use:   use,
			Short: short,
			RunE: func(cmd *cobra.Command, args []string) error {
				if resource == "" || (needName && name == "") {
					return fmt.Errorf("--resource and --name are required")
				}
				c, conn, err := newResourceGRPCClient()
				if err != nil {
					return err
				}
				defer func() { _ = conn.Close() }()
				ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
				defer cancel()
				return fn(ctx, c)
			},
		}
		sub.Flags().StringVar(&resource, "resource", "", "Resource")
		if needName {
			sub.Flags().StringVar(&name, "name", "", "Snapshot name")
		}
		return sub
	}
	result := func(ok bool, msg string) error {
		if !ok {
			return fmt.Errorf("%s", msg)
		}
		fmt.Println(msg)
		return nil
	}
	cmd.AddCommand(
		run("create", "Snapshot every volume on every replica", true, func(ctx context.Context, c haifypb.HaifyControllerClient) error {
			r, err := c.CreateResourceSnapshot(ctx, &haifypb.CreateResourceSnapshotRequest{Resource: resource, Name: name})
			if err != nil {
				return err
			}
			return result(r.Success, r.Message)
		}),
		run("list", "List a resource's snapshots", false, func(ctx context.Context, c haifypb.HaifyControllerClient) error {
			r, err := c.ListResourceSnapshots(ctx, &haifypb.ListResourceSnapshotsRequest{Resource: resource})
			if err != nil {
				return err
			}
			if !r.Success {
				return fmt.Errorf("%s", r.Message)
			}
			for _, n := range r.Names {
				fmt.Println(n)
			}
			return nil
		}),
		run("rollback", "Roll every replica back (the resource must not be Primary anywhere)", true, func(ctx context.Context, c haifypb.HaifyControllerClient) error {
			r, err := c.RollbackResourceSnapshot(ctx, &haifypb.RollbackResourceSnapshotRequest{Resource: resource, Name: name})
			if err != nil {
				return err
			}
			return result(r.Success, r.Message)
		}),
		run("delete", "Delete a snapshot from every replica", true, func(ctx context.Context, c haifypb.HaifyControllerClient) error {
			r, err := c.DeleteResourceSnapshot(ctx, &haifypb.DeleteResourceSnapshotRequest{Resource: resource, Name: name})
			if err != nil {
				return err
			}
			return result(r.Success, r.Message)
		}),
	)
	return cmd
}
