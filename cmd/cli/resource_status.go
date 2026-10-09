package main

import (
	"context"
	"fmt"
	"time"

	haifypb "github.com/haify-project/haify/api/proto/v1"
	"github.com/spf13/cobra"
)

// printWANMetrics renders the proxy counters under a WAN resource's status.
//
// The headline is the un-replicated backlog. Under protocol A those are writes
// the primary already acknowledged that the DR site has not seen, so it is the
// amount a DR failover would lose — the question status could not answer before
// the proxy published these. When the snapshot is absent we say so rather than
// print zeros, because a confident "0 lost" would be the worst possible lie here.
func printWANMetrics(m *haifypb.WANMetrics) {
	if m == nil {
		fmt.Printf("    Replication lag: unknown (proxy published no metrics)\n")
		return
	}

	fmt.Printf("    Un-replicated:   %s", humanBytes(m.GetBufferUsedBytes()))
	if cap := m.GetBufferCapBytes(); cap > 0 {
		fmt.Printf(" of %s buffer (%.1f%%)", humanBytes(cap), m.GetBufferFillPercent())
	}
	fmt.Printf("\n")
	if m.GetBufferUsedBytes() > 0 {
		fmt.Printf("      ⚠ a DR failover right now would lose up to this much\n")
	}

	fmt.Printf("    Replicated:      %s sent as %s on the wire",
		humanBytes(m.GetDrbdToWanBytes()), humanBytes(m.GetWanWireBytes()))
	if r := m.GetCompressionRatio(); r > 0 {
		fmt.Printf(" (%.2fx compression)", r)
	}
	fmt.Printf("\n")

	// Only worth the operator's attention when non-zero.
	if n := m.GetReconnects(); n > 0 {
		fmt.Printf("    WAN reconnects:  %d (a climbing count means a flapping link)\n", n)
	}
	if n := m.GetRingFullEvents(); n > 0 {
		fmt.Printf("    Buffer full:     %d times (the WAN could not keep up; DRBD went Ahead)\n", n)
	}
}

// humanBytes renders a byte count for operator eyes rather than exact accounting.
func humanBytes(b uint64) string {
	const unit = 1024
	if b < unit {
		return fmt.Sprintf("%d B", b)
	}
	units := []string{"KiB", "MiB", "GiB", "TiB", "PiB"}
	v := float64(b)
	for _, u := range units {
		v /= unit
		if v < unit {
			return fmt.Sprintf("%.1f %s", v, u)
		}
	}
	return fmt.Sprintf("%.1f EiB", v/unit)
}

func resourceStatus() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "status <resource>",
		Short: "Show detailed resource status",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			resource := args[0]

			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()

			haifyClient, err := newHaifyClient()
			if err != nil {
				return fmt.Errorf("failed to connect to controller: %w", err)
			}
			defer closeClient(haifyClient)

			status, err := haifyClient.ResourceStatus(ctx, resource)
			if err != nil {
				return fmt.Errorf("failed to get resource status: %w", err)
			}

			fmt.Printf("Resource Status: %s\n", status.GetName())
			fmt.Printf("  Role:  %s\n", status.GetRole())
			fmt.Printf("  Nodes: %v\n", status.GetNodes())
			if status.GetEncrypted() {
				// Spelled out rather than a bare "yes": an operator reading a
				// status page is exactly who might otherwise conclude that the
				// replication link is protected too.
				fmt.Printf("  Encryption: LUKS2 on each replica's backing volume (at rest).\n")
				fmt.Printf("              Replication between nodes is NOT encrypted.\n")
			}

			volumes := status.GetVolumes()
			if len(volumes) > 0 {
				fmt.Printf("\n  Volumes:\n")
				for _, vol := range volumes {
					enc := ""
					if vol.GetEncrypted() {
						enc = " [encrypted]"
					}
					fmt.Printf("    %d: %s (%d GB)%s\n",
						vol.GetVolumeId(), vol.GetDevice(), vol.GetSizeGb(), enc)
				}
			}

			if ns := status.GetNodeStates(); len(ns) > 0 {
				fmt.Printf("\n  Node states:\n")
				for node, st := range ns {
					tls := ""
					if st.GetTls() {
						tls = " tls"
					}
					fmt.Printf("    %s: role=%s disk=%s repl=%s%s\n",
						node, st.GetRole(), st.GetDiskState(), st.GetReplicationState(), tls)
				}
			}

			if status.GetWan() {
				fmt.Printf("\n  WAN replication (protocol A / async):\n")
				fmt.Printf("    DR node:     %s\n", status.GetDrNode())
				fmt.Printf("    DR endpoint: %s\n", status.GetDrEndpoint())
				if p := status.GetWanPort(); p != 0 {
					fmt.Printf("    WAN port:    %d\n", p)
				}
				reach := "unreachable ⚠"
				if status.GetWanReachable() {
					reach = "reachable"
				}
				fmt.Printf("    DR link:     %s (primary → %s)\n", reach, status.GetDrEndpoint())
				for node, st := range status.GetWanProxy() {
					fmt.Printf("    haify-proxy@%s: %s\n", node, st)
				}
				printWANMetrics(status.GetWanMetrics())
				fmt.Printf("    NOTE: the DR peer can lag (async). Failover is a manual DR action:\n")
				fmt.Printf("          haify resource dr-failover %s\n", status.GetName())
			}

			return nil
		},
	}

	return cmd
}

func resourceVerify() *cobra.Command {
	var node string
	var wait time.Duration
	var resync bool
	cmd := &cobra.Command{
		Use:   "verify <resource>",
		Short: "Compare a resource's replicas block by block, and repair what differs",
		Long: "Reads every replica and compares it with the one on --node (default: the Primary).\n" +
			"Run it again while it says running to follow it. When it finds blocks that differ,\n" +
			"--resync copies --node's data over them on the other replicas.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, cancel := context.WithTimeout(context.Background(), wait+3*time.Minute)
			defer cancel()
			haifyClient, err := newHaifyClient()
			if err != nil {
				return fmt.Errorf("failed to connect to controller: %w", err)
			}
			defer closeClient(haifyClient)
			resp, err := haifyClient.VerifyResource(ctx, &haifypb.VerifyResourceRequest{
				Name: args[0], Node: node, WaitSeconds: uint32(wait.Seconds()), Resync: resync,
			})
			if err != nil {
				return err
			}
			for _, s := range resp.Steps {
				fmt.Printf("  %s\n", s)
			}
			for _, p := range resp.Peers {
				line := fmt.Sprintf("  %s ↔ %s: %s", resp.Source, p.Node, p.State)
				if p.State == "verifying" {
					line += fmt.Sprintf(" %.1f%%", p.PercentDone)
				}
				if p.OutOfSyncKib > 0 {
					line += fmt.Sprintf(", %d KiB marked out of sync", p.OutOfSyncKib)
					if p.BaselineKnown {
						line += fmt.Sprintf(" (%d KiB found by this verify)", p.FoundKib)
					}
				}
				fmt.Println(line)
			}
			fmt.Println(resp.Message)
			if !resp.Success {
				return fmt.Errorf("verify failed")
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&node, "node", "", "Node to verify from, and whose data --resync keeps (default: the Primary)")
	cmd.Flags().DurationVar(&wait, "wait", 0, "How long to wait for the verify to finish (e.g. 10m)")
	cmd.Flags().BoolVar(&resync, "resync", false, "Copy --node's data over the blocks a finished verify found different")
	return cmd
}
