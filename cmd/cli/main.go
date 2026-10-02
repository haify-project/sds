package main

import (
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/liliang-cn/sds/pkg/client"
)

var (
	controllerAddr string
	tokenFlag      string
	tlsFlags       client.TLSOptions
)

func main() {
	rootCmd := &cobra.Command{
		Use:   "sds",
		Short: "HA-SDS CLI - Software Defined Storage Management",
		// main prints the error, once.
		SilenceErrors: true,
		// Usage is for a command line that did not parse. Once a command runs,
		// its error comes from the cluster, and forty lines of flags printed
		// after it bury the one line that matters. Cobra validates arguments
		// before this hook and required flags after it; main covers those.
		PersistentPreRun: func(cmd *cobra.Command, _ []string) { cmd.SilenceUsage = true },
	}

	rootCmd.PersistentFlags().StringVarP(&controllerAddr, "controller", "c", "127.0.0.1:3374", "Controller address")
	rootCmd.PersistentFlags().StringVar(&tokenFlag, "token", "", "API token (default: SDS_TOKEN env, ~/.sds/token, /etc/sds/token)")

	// Transport security, matching the controller's [tls] section. Any of the
	// material flags implies --tls, so an operator who points at a CA cannot
	// end up talking plaintext to a TLS controller and reading the handshake
	// error as an outage.
	rootCmd.PersistentFlags().BoolVar(&tlsFlags.Enabled, "tls", false, "Connect over TLS (implied by --tls-ca/--tls-cert; env SDS_TLS)")
	rootCmd.PersistentFlags().StringVar(&tlsFlags.CACert, "tls-ca", "", "CA bundle that signed the controller certificate (env SDS_TLS_CA; default: system trust store)")
	rootCmd.PersistentFlags().StringVar(&tlsFlags.ClientCert, "tls-cert", "", "Client certificate, for a controller requiring mutual TLS (env SDS_TLS_CERT)")
	rootCmd.PersistentFlags().StringVar(&tlsFlags.ClientKey, "tls-key", "", "Client private key (env SDS_TLS_KEY)")
	rootCmd.PersistentFlags().StringVar(&tlsFlags.ServerName, "tls-server-name", "", "Name to verify against the controller certificate (env SDS_TLS_SERVER_NAME)")
	rootCmd.PersistentFlags().BoolVar(&tlsFlags.Insecure, "tls-insecure", false, "Encrypt but do NOT verify the controller — accepts any certificate (env SDS_TLS_INSECURE)")

	rootCmd.AddCommand(poolCommand())
	rootCmd.AddCommand(nodeCommand())
	rootCmd.AddCommand(resourceCommand())
	rootCmd.AddCommand(haCommand())
	rootCmd.AddCommand(gatewayCommand())
	rootCmd.AddCommand(healthCommand())
	rootCmd.AddCommand(rbacCommand())
	rootCmd.AddCommand(eventCommand())
	rootCmd.AddCommand(inspectCommand())
	rootCmd.AddCommand(notifyCommand())
	rootCmd.AddCommand(wanCommand())
	rootCmd.AddCommand(backupCommand())
	rootCmd.AddCommand(replicationTLSCommand())

	if cmd, err := rootCmd.ExecuteC(); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		if strings.HasPrefix(err.Error(), "required flag") {
			fmt.Fprint(os.Stderr, "\n"+cmd.UsageString())
		}
		os.Exit(1)
	}
}
