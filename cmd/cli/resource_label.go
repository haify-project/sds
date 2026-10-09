package main

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/spf13/cobra"
)

// resourceLabel sets and removes a resource's labels, or shows them, the way
// `node label` does for nodes.
func resourceLabel() *cobra.Command {
	return &cobra.Command{
		Use:   "label <resource> [<key=value>...]",
		Short: "Show or set a resource's labels (e.g. team=web); key= deletes a label",
		Args:  cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			labels := make(map[string]string, len(args)-1)
			for _, kv := range args[1:] {
				k, v, ok := strings.Cut(kv, "=")
				k = strings.TrimSpace(k)
				if !ok || k == "" {
					return fmt.Errorf("invalid label %q (want key=value)", kv)
				}
				labels[k] = strings.TrimSpace(v)
			}

			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			c, err := newHaifyClient()
			if err != nil {
				return fmt.Errorf("failed to connect to controller: %w", err)
			}
			defer closeClient(c)

			after, err := c.SetResourceLabels(ctx, args[0], labels, nil)
			if err != nil {
				return fmt.Errorf("failed to set resource labels: %w", err)
			}
			fmt.Printf("Resource '%s' labels: %s\n", args[0], formatLabels(after))
			return nil
		},
	}
}
