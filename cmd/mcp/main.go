// Command sds-mcp exposes the SDS controller as a Model Context Protocol
// (MCP) server over stdio, so AI assistants (Claude Code, Claude Desktop,
// and other MCP clients) can inspect and manage storage.
//
// Register with an MCP client, e.g.:
//
//	claude mcp add sds -- sds-mcp --controller orange1:3374
//
// All logs go to stderr; stdout carries the MCP protocol.
package main

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"

	"github.com/liliang-cn/sds/pkg/client"
	"github.com/liliang-cn/sds/pkg/mcpserver"
)

// version is set at build time via -ldflags "-X main.version=...".
var version = "dev"

func main() {
	var (
		controllerAddr string
		tokenFlag      string
		readOnly       bool
		allowWrite     []string
		debug          bool
	)

	rootCmd := &cobra.Command{
		Use:           "sds-mcp",
		Short:         "MCP server for the SDS storage controller",
		Long:          "Serves SDS storage management tools over the Model Context Protocol (stdio transport).",
		Version:       version,
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			logger, err := newStderrLogger(debug)
			if err != nil {
				return fmt.Errorf("init logger: %w", err)
			}
			defer func() { _ = logger.Sync() }()

			opts := []client.Option{}
			if token := client.ResolveToken(tokenFlag); token != "" {
				opts = append(opts, client.WithToken(token))
			}
			sdsClient, err := client.NewSDSClient(controllerAddr, opts...)
			if err != nil {
				return fmt.Errorf("connect to controller %s: %w", controllerAddr, err)
			}
			defer func() { _ = sdsClient.Close() }()

			srv := mcpserver.New(sdsClient, logger, mcpserver.Options{
				ReadOnly:   readOnly,
				AllowWrite: allowWrite,
				Version:    version,
			})
			return srv.Run(cmd.Context())
		},
	}

	rootCmd.Flags().StringVarP(&controllerAddr, "controller", "c", "127.0.0.1:3374", "SDS controller address")
	rootCmd.Flags().StringVar(&tokenFlag, "token", "", "API token (default: SDS_TOKEN env, ~/.sds/token, /etc/sds/token)")
	rootCmd.Flags().BoolVar(&readOnly, "read-only", false, "register only read-only tools (list/status/health)")
	rootCmd.Flags().StringSliceVar(&allowWrite, "allow", nil,
		"mutating tools to register by name despite --read-only, e.g. --allow sds_ha_evict. "+
			"Implies --read-only. Refuses to start on a name no tool answers to")
	rootCmd.Flags().BoolVar(&debug, "debug", false, "enable debug logging on stderr")

	if err := rootCmd.Execute(); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
}

// newStderrLogger builds a console logger writing to stderr only.
// stdout must stay clean: it carries the MCP stdio protocol.
func newStderrLogger(debug bool) (*zap.Logger, error) {
	cfg := zap.NewProductionConfig()
	cfg.OutputPaths = []string{"stderr"}
	cfg.ErrorOutputPaths = []string{"stderr"}
	cfg.Encoding = "console"
	cfg.EncoderConfig.EncodeTime = zapcore.ISO8601TimeEncoder
	if debug {
		cfg.Level = zap.NewAtomicLevelAt(zap.DebugLevel)
	}
	return cfg.Build()
}
