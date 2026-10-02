package main

import (
	"context"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"google.golang.org/protobuf/encoding/protojson"

	sdspb "github.com/liliang-cn/sds/api/proto/v1"
)

// inspectAreas is the report order of the areas.
var inspectAreas = []string{"resources", "gateways", "nodes", "pools", "backups", "alerts", "selfha", "tls", "hygiene"}

func inspectCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:     "inspect",
		Aliases: []string{"inspection"},
		Short:   "Inspect the cluster: deterministic checks with evidence and the command that fixes each finding",
		Long: "An inspection checks what the alert detector does not: replicas stuck mid-handshake,\n" +
			"alerts that reached no channel, node clocks, disks and addresses, pool growth, late\n" +
			"backups, Self-HA readiness, certificate expiry and leftovers of deleted resources.\n" +
			"It changes nothing. The controller runs one on [inspect] schedule and keeps the reports.",
	}
	cmd.AddCommand(inspectRunCommand(), inspectListCommand(), inspectShowCommand())
	return cmd
}

func inspectRunCommand() *cobra.Command {
	var areas []string
	var jsonOut bool
	cmd := &cobra.Command{
		Use:   "run",
		Short: "Inspect the cluster now and print the report",
		RunE: func(cmd *cobra.Command, _ []string) error {
			for _, a := range areas {
				if !containsStr(inspectAreas, a) {
					return fmt.Errorf("unknown area %q (want one of %s)", a, strings.Join(inspectAreas, ", "))
				}
			}
			ctx, cancel := context.WithTimeout(context.Background(), nodeOpTimeout)
			defer cancel()
			c, err := newSDSClient()
			if err != nil {
				return fmt.Errorf("failed to connect to controller: %w", err)
			}
			defer closeClient(c)
			r, err := c.RunInspection(ctx, areas)
			if err != nil {
				return err
			}
			return printReport(cmd.OutOrStdout(), r, jsonOut)
		},
	}
	cmd.Flags().StringSliceVar(&areas, "area", nil, "only these areas: "+strings.Join(inspectAreas, ","))
	cmd.Flags().BoolVar(&jsonOut, "json", false, "print the report as JSON")
	return cmd
}

func inspectListCommand() *cobra.Command {
	var limit int32
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List stored inspection reports, newest first",
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			c, err := newSDSClient()
			if err != nil {
				return fmt.Errorf("failed to connect to controller: %w", err)
			}
			defer closeClient(c)
			reports, err := c.ListInspections(ctx, limit)
			if err != nil {
				return err
			}
			w := cmd.OutOrStdout()
			if len(reports) == 0 {
				_, _ = fmt.Fprintln(w, "No inspections yet. Run one with: sds inspect run")
				return nil
			}
			_, _ = fmt.Fprintf(w, "%-6s %-20s %-9s %5s %5s %5s %5s %s\n", "ID", "STARTED", "TRIGGER", "FAIL", "WARN", "ERROR", "PASS", "AREAS")
			for _, r := range reports {
				s := r.GetSummary()
				areas := strings.Join(r.Areas, ",")
				if areas == "" {
					areas = "all"
				}
				_, _ = fmt.Fprintf(w, "%-6s %-20s %-9s %5d %5d %5d %5d %s\n", r.Id, msTime(r.StartedAtUnixMs), r.Trigger,
					s.GetFail(), s.GetWarn(), s.GetError(), s.GetPass(), areas)
			}
			return nil
		},
	}
	cmd.Flags().Int32Var(&limit, "limit", 0, "show at most this many (0 = all stored)")
	return cmd
}

func inspectShowCommand() *cobra.Command {
	var jsonOut bool
	cmd := &cobra.Command{
		Use:   "show [<id>|latest]",
		Short: "Show an inspection report (the newest by default)",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			id := "latest"
			if len(args) == 1 {
				id = args[0]
			}
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			c, err := newSDSClient()
			if err != nil {
				return fmt.Errorf("failed to connect to controller: %w", err)
			}
			defer closeClient(c)
			r, err := c.GetInspection(ctx, id)
			if err != nil {
				return err
			}
			return printReport(cmd.OutOrStdout(), r, jsonOut)
		},
	}
	cmd.Flags().BoolVar(&jsonOut, "json", false, "print the report as JSON")
	return cmd
}

// printReport writes a report grouped by area, failures first, each finding
// followed by its evidence and the command that fixes it.
func printReport(w io.Writer, r *sdspb.InspectionReport, jsonOut bool) error {
	if jsonOut {
		b, err := protojson.MarshalOptions{Multiline: true, UseProtoNames: true, EmitUnpopulated: true}.Marshal(r)
		if err != nil {
			return err
		}
		_, _ = fmt.Fprintln(w, string(b))
		return nil
	}
	s := r.GetSummary()
	took := time.Duration(r.FinishedAtUnixMs-r.StartedAtUnixMs) * time.Millisecond
	_, _ = fmt.Fprintf(w, "Inspection %s (%s, %s, took %s): %d fail, %d warn, %d error, %d pass\n",
		r.Id, r.Trigger, msTime(r.StartedAtUnixMs), took.Round(100*time.Millisecond), s.GetFail(), s.GetWarn(), s.GetError(), s.GetPass())
	for _, area := range inspectAreas {
		var checks []*sdspb.InspectionCheck
		for _, c := range r.Checks {
			if c.Area == area {
				checks = append(checks, c)
			}
		}
		if len(checks) == 0 {
			continue
		}
		_, _ = fmt.Fprintf(w, "\n%s\n", strings.ToUpper(area))
		for _, st := range []string{"fail", "error", "warn", "pass"} {
			for _, c := range checks {
				if c.Status == st {
					printCheck(w, c)
				}
			}
		}
	}
	return nil
}

func printCheck(w io.Writer, c *sdspb.InspectionCheck) {
	subject := ""
	if c.Subject != "" {
		subject = " " + c.Subject
	}
	_, _ = fmt.Fprintf(w, "  %-5s %s%s: %s\n", strings.ToUpper(c.Status), c.Id, subject, c.Message)
	for _, e := range c.Evidence {
		_, _ = fmt.Fprintf(w, "        | %s\n", e)
	}
	if c.Fix != "" && c.Status != "pass" {
		_, _ = fmt.Fprintf(w, "        fix: %s\n", c.Fix)
	}
	if c.Runbook != "" {
		_, _ = fmt.Fprintf(w, "        runbook: %s\n", c.Runbook)
	}
}

func msTime(ms int64) string {
	if ms == 0 {
		return "-"
	}
	return time.UnixMilli(ms).Local().Format("2006-01-02 15:04:05")
}

func containsStr(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}
