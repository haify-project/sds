// Package controller provides the SDS controller
package controller

import (
	"context"
	"fmt"
	"net/http"

	"github.com/grpc-ecosystem/grpc-gateway/v2/runtime"
	sdspb "github.com/liliang-cn/sds/api/proto/v1"
	"go.uber.org/zap"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

// GatewayServer wraps the gRPC-Gateway HTTP server
type GatewayServer struct {
	server     *http.Server
	grpcAddr   string
	httpAddr   string
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
