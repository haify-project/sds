// Package controller provides the SDS controller
package controller

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/grpc-ecosystem/grpc-gateway/v2/runtime"
	sdspb "github.com/liliang-cn/sds/api/proto/v1"
	"github.com/liliang-cn/sds/pkg/config"
	"go.uber.org/zap"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/keepalive"
)

// loopbackTarget is the address the REST gateway dials to reach this process's
// own gRPC server.
//
// It is deliberately NOT the listen address. Listening and dialling want
// opposite things: `listen_address` is usually "0.0.0.0", meaning "every
// interface", which is not a destination. Dialling "0.0.0.0:3374" happens to
// work on Linux (the kernel rewrites it to loopback) and that accident held
// until a node gained a VM-wide HTTP_PROXY: grpc-go saw a target that was not
// in NO_PROXY and sent the call to the proxy, which hung up. The gateway then
// answered every /v1 request with 503 "error reading server preface: EOF".
func loopbackTarget(cfg *config.Config) string {
	port := cfg.Server.Port
	if port == 0 {
		port = defaultGRPCPort
	}
	return fmt.Sprintf("127.0.0.1:%d", port)
}

// loopbackDialOptions are the dial options for that in-process hop.
//
// grpc.WithNoProxy is the load-bearing one: a call from the controller to
// itself must never traverse an HTTP proxy, no matter how the operator
// configured the machine. Relying on NO_PROXY listing every spelling of
// "local" is how this broke in the first place.
func loopbackDialOptions() []grpc.DialOption {
	return []grpc.DialOption{
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithNoProxy(),
		grpc.WithDefaultCallOptions(grpc.MaxCallRecvMsgSize(4 * 1024 * 1024)),
		grpc.WithKeepaliveParams(keepalive.ClientParameters{
			Time:                10 * time.Second,
			Timeout:             time.Second,
			PermitWithoutStream: true,
		}),
	}
}

// GatewayServer wraps the gRPC-Gateway HTTP server
type GatewayServer struct {
	grpcAddr   string
	port       int
	grpcServer *grpc.Server
	logger     *zap.Logger
}

// NewGatewayServer creates a new gRPC-Gateway HTTP server
func NewGatewayServer(grpcServer *grpc.Server, grpcAddr string, port int, logger *zap.Logger) *GatewayServer {
	mux := runtime.NewServeMux(
		runtime.WithIncomingHeaderMatcher(func(key string) (string, bool) {
			// Allow all headers to pass through
			return key, true
		}),
	)

	opts := []grpc.DialOption{
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	}

	// Register all services from the grpc server
	// Note: The gateway will connect to the gRPC server via the local address
	if err := sdspb.RegisterSDSControllerHandlerFromEndpoint(context.Background(), mux, grpcAddr, opts); err != nil {
		logger.Error("Failed to register gateway handler", zap.Error(err))
		return nil
	}

	return &GatewayServer{
		grpcServer: grpcServer,
		grpcAddr:   grpcAddr,
		port:       port,
		logger:     logger,
	}
}

// Start starts the gateway HTTP server
func (g *GatewayServer) Start() error {
	// The gateway is started by the main controller using cmux
	return nil
}

// Handler returns the HTTP handler for the gateway
func (g *GatewayServer) Handler() http.Handler {
	mux := runtime.NewServeMux(
		runtime.WithIncomingHeaderMatcher(func(key string) (string, bool) {
			return key, true
		}),
	)

	opts := []grpc.DialOption{
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	}

	if err := sdspb.RegisterSDSControllerHandlerFromEndpoint(context.Background(), mux, g.grpcAddr, opts); err != nil {
		g.logger.Error("Failed to register gateway handler", zap.Error(err))
		return nil
	}

	return mux
}

// RegisterGatewayHandler registers the gRPC-Gateway handler with the given HTTP serve mux
func RegisterGatewayHandler(ctx context.Context, mux *runtime.ServeMux, grpcAddr string, logger *zap.Logger) error {
	opts := []grpc.DialOption{
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	}

	if err := sdspb.RegisterSDSControllerHandlerFromEndpoint(ctx, mux, grpcAddr, opts); err != nil {
		return fmt.Errorf("failed to register gateway handler: %w", err)
	}

	logger.Info("Registered gRPC-Gateway handler", zap.String("grpc_addr", grpcAddr))
	return nil
}
