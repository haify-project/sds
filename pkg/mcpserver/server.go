// Package mcpserver exposes the SDS controller as a Model Context Protocol
// (MCP) server so AI assistants can inspect and manage storage through the
// same gRPC API used by sds-cli.
//
// Tools are grouped by domain (cluster, resource, snapshot, gateway, HA) and
// carry MCP annotations: read-only tools are always registered, mutating
// tools are skipped entirely in read-only mode, and destructive tools are
// hinted so clients can require user confirmation.
package mcpserver

import (
	"context"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"go.uber.org/zap"
)

const (
	// readTimeout bounds list/status calls.
	readTimeout = 30 * time.Second
	// writeTimeout bounds mutating calls; resource and gateway creation
	// run SSH commands on every node and can take minutes.
	writeTimeout = 5 * time.Minute
)

// Options configures the MCP server.
type Options struct {
	// ReadOnly registers only read-only tools (list/status/health).
	ReadOnly bool
	// Version reported in the MCP initialize handshake.
	Version string
}

// Server bridges MCP tool calls to the SDS controller client.
type Server struct {
	client   ControllerClient
	logger   *zap.Logger
	readOnly bool
	version  string
}

// New creates a Server. The client is typically *client.SDSClient; tests
// pass a mock. The logger must write to stderr only — stdout carries the
// MCP stdio protocol.
func New(c ControllerClient, logger *zap.Logger, opts Options) *Server {
	version := opts.Version
	if version == "" {
		version = "dev"
	}
	return &Server{
		client:   c,
		logger:   logger,
		readOnly: opts.ReadOnly,
		version:  version,
	}
}

// MCPServer builds the underlying MCP server with all tools registered.
func (s *Server) MCPServer() *mcp.Server {
	srv := mcp.NewServer(&mcp.Implementation{
		Name:    "sds",
		Title:   "SDS Software Defined Storage",
		Version: s.version,
	}, nil)
	s.registerClusterTools(srv)
	s.registerResourceTools(srv)
	s.registerSnapshotTools(srv)
	s.registerGatewayTools(srv)
	s.registerHATools(srv)
	return srv
}

// Run serves MCP over stdio until the client disconnects or ctx is done.
func (s *Server) Run(ctx context.Context) error {
	srv := s.MCPServer()
	s.logger.Info("starting SDS MCP server on stdio",
		zap.Bool("read_only", s.readOnly),
		zap.String("version", s.version),
	)
	return srv.Run(ctx, &mcp.StdioTransport{})
}

// opResult is the structured output for mutating tools that return no data.
type opResult struct {
	Status string `json:"status" jsonschema:"operation outcome, always \"ok\" on success"`
	Detail string `json:"detail,omitempty" jsonschema:"human-readable summary of what was done"`
}

func ok(detail string) opResult {
	return opResult{Status: "ok", Detail: detail}
}

// Annotation helpers. Hints follow the MCP spec defaults: DestructiveHint
// defaults to true, so additive tools must set it to false explicitly.

func boolPtr(b bool) *bool { return &b }

func readOnlyTool(name, title, description string) *mcp.Tool {
	return &mcp.Tool{
		Name:        name,
		Description: description,
		Annotations: &mcp.ToolAnnotations{
			Title:         title,
			ReadOnlyHint:  true,
			OpenWorldHint: boolPtr(false),
		},
	}
}

// writeTool marks additive/idempotent mutations (create, start, mount).
func writeTool(name, title, description string) *mcp.Tool {
	return &mcp.Tool{
		Name:        name,
		Description: description,
		Annotations: &mcp.ToolAnnotations{
			Title:           title,
			DestructiveHint: boolPtr(false),
			OpenWorldHint:   boolPtr(false),
		},
	}
}

// destructiveTool marks mutations that delete data or interrupt service
// (delete, restore, evict). Clients should ask the user before calling.
func destructiveTool(name, title, description string) *mcp.Tool {
	return &mcp.Tool{
		Name:        name,
		Description: description,
		Annotations: &mcp.ToolAnnotations{
			Title:           title,
			DestructiveHint: boolPtr(true),
			OpenWorldHint:   boolPtr(false),
		},
	}
}

// addRead registers a read-only tool (available in every mode).
func addRead[In, Out any](s *Server, srv *mcp.Server, t *mcp.Tool, h mcp.ToolHandlerFor[In, Out]) {
	mcp.AddTool(srv, t, instrument(s, t.Name, readTimeout, h))
}

// addWrite registers a mutating tool unless the server is read-only.
func addWrite[In, Out any](s *Server, srv *mcp.Server, t *mcp.Tool, h mcp.ToolHandlerFor[In, Out]) {
	if s.readOnly {
		return
	}
	mcp.AddTool(srv, t, instrument(s, t.Name, writeTimeout, h))
}

// instrument wraps a handler with a timeout and structured logging.
func instrument[In, Out any](s *Server, name string, timeout time.Duration, h mcp.ToolHandlerFor[In, Out]) mcp.ToolHandlerFor[In, Out] {
	return func(ctx context.Context, req *mcp.CallToolRequest, in In) (*mcp.CallToolResult, Out, error) {
		ctx, cancel := context.WithTimeout(ctx, timeout)
		defer cancel()
		start := time.Now()
		res, out, err := h(ctx, req, in)
		if err != nil {
			s.logger.Warn("tool call failed",
				zap.String("tool", name),
				zap.Duration("duration", time.Since(start)),
				zap.Error(err),
			)
		} else {
			s.logger.Info("tool call ok",
				zap.String("tool", name),
				zap.Duration("duration", time.Since(start)),
			)
		}
		return res, out, err
	}
}
