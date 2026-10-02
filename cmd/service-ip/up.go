//go:build linux

package main

import (
	"log/slog"
	"os"

	"github.com/haify-project/sds/pkg/serviceip/announce"
	"github.com/haify-project/sds/pkg/serviceip/ip"
	"github.com/spf13/cobra"
)

var arpCount int

var upCmd = &cobra.Command{
	Use:   "up",
	Short: "Bring up the virtual IP",
	Run: func(cmd *cobra.Command, args []string) {
		// Handle Instance ID parsing
		if targetInstance != "" {
			parsedIP, parsedDev := ParseInstanceID(targetInstance)
			if targetIP == "" {
				targetIP = parsedIP
			}
			if targetDev == "" {
				targetDev = parsedDev
			}
			slog.Debug("Parsed instance", "instance", targetInstance, "ip", targetIP, "dev", targetDev)
		}

		if targetIP == "" {
			slog.Error("--ip or --instance is required")
			os.Exit(1)
		}

		slog.Info("Bringing up IP", "ip", targetIP, "dev", targetDev)

		// TODO: Pre-check if interface is UP (Improvement #4)
		// TODO: ARP Conflict Check (Improvement #2)

		actualDev, err := ip.EnsureIP(targetDev, targetIP)
		if err != nil {
			slog.Error("Failed to ensure IP", "error", err)
			os.Exit(1)
		}

		if err := announce.Announce(actualDev, targetIP, arpCount); err != nil {
			slog.Error("Failed to announce IP", "error", err)
			os.Exit(1)
		}

		slog.Info("IP is up and announced", "ip", targetIP, "dev", actualDev)
	},
}

func init() {
	RootCmd.AddCommand(upCmd)
	upCmd.Flags().IntVar(&arpCount, "arp-count", 5, "Number of announcements to send")
}
