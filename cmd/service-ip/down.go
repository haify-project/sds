//go:build linux

package main

import (
	"log/slog"
	"os"

	"github.com/liliang-cn/sds/pkg/serviceip/ip"
	"github.com/spf13/cobra"
)

var downCmd = &cobra.Command{
	Use:   "down",
	Short: "Take down the virtual IP",
	Run: func(cmd *cobra.Command, args []string) {
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

		slog.Info("Removing IP", "ip", targetIP, "dev", targetDev)
		if err := ip.RemoveIP(targetDev, targetIP); err != nil {
			slog.Error("Failed to remove IP", "error", err)
			os.Exit(1)
		}

		slog.Info("IP removed")
	},
}

func init() {
	RootCmd.AddCommand(downCmd)
}
