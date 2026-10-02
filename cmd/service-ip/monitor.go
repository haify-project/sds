//go:build linux

package main

import (
	"log/slog"
	"os"

	"github.com/haify-project/sds/pkg/serviceip/ip"
	"github.com/spf13/cobra"
)

// OCF Exit Codes
const (
	OCF_SUCCESS     = 0
	OCF_ERR_GENERIC = 1
	OCF_NOT_RUNNING = 7
)

var monitorCmd = &cobra.Command{
	Use:   "monitor",
	Short: "Check if the virtual IP is up",
	Run: func(cmd *cobra.Command, args []string) {
		if targetInstance != "" {
			parsedIP, parsedDev := ParseInstanceID(targetInstance)
			if targetIP == "" {
				targetIP = parsedIP
			}
			if targetDev == "" {
				targetDev = parsedDev
			}
		}

		if targetIP == "" {
			slog.Error("--ip or --instance is required")
			os.Exit(OCF_ERR_GENERIC)
		}

		exists, err := ip.CheckIP(targetDev, targetIP)
		if err != nil {
			slog.Error("Error checking IP", "error", err)
			os.Exit(OCF_ERR_GENERIC)
		}

		if exists {
			slog.Info("IP is present")
			os.Exit(OCF_SUCCESS)
		} else {
			slog.Info("IP is NOT present")
			os.Exit(OCF_NOT_RUNNING)
		}
	},
}

func init() {
	RootCmd.AddCommand(monitorCmd)
}
