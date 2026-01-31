package main

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
)

var (
	controllerAddr string
)

func main() {
	rootCmd := &cobra.Command{
		Use:   "sds",
		Short: "HA-SDS CLI - Software Defined Storage Management",
	}

	rootCmd.PersistentFlags().StringVarP(&controllerAddr, "controller", "c", "127.0.0.1:3374", "Controller address")

	rootCmd.AddCommand(poolCommand())
	rootCmd.AddCommand(nodeCommand())
	rootCmd.AddCommand(resourceCommand())
	rootCmd.AddCommand(snapshotCommand())
	rootCmd.AddCommand(haCommand())
	rootCmd.AddCommand(gatewayCommand())

	if err := rootCmd.Execute(); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
}
