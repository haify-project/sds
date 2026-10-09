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

func replicationTLSCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "replication-tls",
		Short: "Encrypt DRBD replication between nodes (kernel TLS via tlshd)",
		Long: `Encrypt the DRBD connections between nodes with kernel TLS.

Each node needs DRBD 9.2 or later, the kernel tls module and tlshd (package
ktls-utils). "setup" gives every node a key made on the node, a certificate
from this controller's replication CA, that CA in the system trust store, and a
tlshd configured to use them. Then "haify resource tls <resource> on" moves a
resource's connections over, one link at a time so the Primary keeps quorum.

The replication CA joins each node's system trust store, so anything on the
node that validates against that store will accept a certificate it issued. It
only ever issues replication certificates; its key stays with the controller.`,
	}
	cmd.AddCommand(replicationTLSSetupCommand(), replicationTLSStatusCommand())
	return cmd
}

func replicationTLSSetupCommand() *cobra.Command {
	var nodes []string
	cmd := &cobra.Command{
		Use:   "setup",
		Short: "Prepare nodes for encrypted replication (all nodes unless --nodes)",
		RunE: func(cmd *cobra.Command, args []string) error {
			return runTLSNodes(func(ctx context.Context, c tlsNodeClient) ([]*haifypb.NodeTLSInfo, error) {
				return c.SetupReplicationTLS(ctx, nodes)
			})
		},
	}
	cmd.Flags().StringSliceVar(&nodes, "nodes", nil, "Nodes to prepare (default: every registered node)")
	return cmd
}

func replicationTLSStatusCommand() *cobra.Command {
	var nodes []string
	cmd := &cobra.Command{
		Use:   "status",
		Short: "Show which nodes can carry an encrypted connection",
		RunE: func(cmd *cobra.Command, args []string) error {
			return runTLSNodes(func(ctx context.Context, c tlsNodeClient) ([]*haifypb.NodeTLSInfo, error) {
				return c.ReplicationTLSStatus(ctx, nodes)
			})
		},
	}
	cmd.Flags().StringSliceVar(&nodes, "nodes", nil, "Nodes to check (default: every registered node)")
	return cmd
}

type tlsNodeClient interface {
	SetupReplicationTLS(ctx context.Context, nodes []string) ([]*haifypb.NodeTLSInfo, error)
	ReplicationTLSStatus(ctx context.Context, nodes []string) ([]*haifypb.NodeTLSInfo, error)
}

func runTLSNodes(call func(context.Context, tlsNodeClient) ([]*haifypb.NodeTLSInfo, error)) error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	c, err := newHaifyClient()
	if err != nil {
		return fmt.Errorf("failed to connect to controller: %w", err)
	}
	defer closeClient(c)
	states, err := call(ctx, c)
	if err != nil {
		return err
	}
	w := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
	_, _ = fmt.Fprintln(w, "NODE\tREADY\tCERT EXPIRES\tPROBLEM")
	notReady := 0
	for _, s := range states {
		ready := "yes"
		if !s.Ready {
			ready = "no"
			notReady++
		}
		_, _ = fmt.Fprintf(w, "%s\t%s\t%s\t%s\n", s.Node, ready, orDash(s.Expires), orDash(s.Problem))
	}
	_ = w.Flush()
	if notReady > 0 {
		return fmt.Errorf("%d node(s) not ready", notReady)
	}
	return nil
}

func resourceTLSCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "tls <resource> on|off",
		Short: "Switch a resource's DRBD connections to or from TLS, one link at a time",
		Long: `Switch every DRBD connection of a resource to TLS (on) or back (off).

DRBD cannot change a live connection's transport, so each link is taken down
and brought back on its own while the others keep quorum. Every node of the
resource must be ready first (see "haify replication-tls status"). A link
whose handshake fails is left StandAlone by DRBD and the switch stops there.`,
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			var on bool
			switch args[1] {
			case "on":
				on = true
			case "off":
			default:
				return fmt.Errorf("want on or off, got %q", args[1])
			}
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
			defer cancel()
			c, err := newHaifyClient()
			if err != nil {
				return fmt.Errorf("failed to connect to controller: %w", err)
			}
			defer closeClient(c)
			msg, err := c.SetResourceTLS(ctx, args[0], on)
			if err != nil {
				return err
			}
			fmt.Println(msg)
			return nil
		},
	}
}
