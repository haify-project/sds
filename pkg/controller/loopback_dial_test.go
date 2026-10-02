package controller

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/grpc-ecosystem/grpc-gateway/v2/runtime"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
	"google.golang.org/grpc"

	sdspb "github.com/haify-project/sds/api/proto/v1"
	"github.com/haify-project/sds/pkg/config"
)

// The gRPC listen address and the gateway's dial target are opposites:
// listening wants 0.0.0.0 (every interface), dialling wants a concrete address.
// Reusing one string for both produced a gateway that dialled "0.0.0.0:3374",
// which only ever worked by accident.
func TestLoopbackTargetIsNotTheListenAddress(t *testing.T) {
	for _, listen := range []string{"0.0.0.0", "", "::", "192.168.123.227"} {
		got := loopbackTarget(&config.Config{Server: config.ServerConfig{
			ListenAddress: listen, Port: 3374,
		}})
		assert.Equal(t, "127.0.0.1:3374", got,
			"listen address %q must not leak into the dial target", listen)
	}
}

func TestLoopbackTargetHonoursThePort(t *testing.T) {
	got := loopbackTarget(&config.Config{Server: config.ServerConfig{
		ListenAddress: "0.0.0.0", Port: 43517,
	}})
	assert.Equal(t, "127.0.0.1:43517", got)
}

// A controller with no explicit port must still produce a dialable target
// rather than "127.0.0.1:0".
func TestLoopbackTargetFallsBackToTheDefaultPort(t *testing.T) {
	got := loopbackTarget(&config.Config{Server: config.ServerConfig{ListenAddress: "0.0.0.0"}})
	assert.Equal(t, "127.0.0.1:3374", got)
}

// Regression: an HTTP proxy in the environment must not capture the
// controller's call to its own gRPC server. A VM-wide HTTP_PROXY did exactly
// that — grpc-go routed 0.0.0.0:3374 through the proxy, which hung up, and the
// gateway answered every /v1 request with 503 "error reading server preface:
// EOF".
//
// The target here is deliberately "0.0.0.0", not "127.0.0.1". Go's httpproxy
// unconditionally bypasses localhost and loopback literals whatever NO_PROXY
// says, so a 127.0.0.1 target would never reach the proxy and the test would
// pass with or without the option — proving nothing. "0.0.0.0" gets no such
// exemption, which is precisely why the production bug existed.
func TestLoopbackDialOptionsIgnoreAnEnvironmentProxy(t *testing.T) {
	// A proxy that accepts and immediately hangs up: the "server preface: EOF"
	// symptom in one line.
	badProxy, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	defer func() { _ = badProxy.Close() }()
	go func() {
		for {
			conn, err := badProxy.Accept()
			if err != nil {
				return
			}
			_ = conn.Close()
		}
	}()
	t.Setenv("HTTP_PROXY", "http://"+badProxy.Addr().String())
	t.Setenv("HTTPS_PROXY", "http://"+badProxy.Addr().String())
	t.Setenv("NO_PROXY", "")

	grpcLis, err := net.Listen("tcp", "0.0.0.0:0")
	require.NoError(t, err)
	grpcSrv := grpc.NewServer()
	sdspb.RegisterSDSControllerServer(grpcSrv, NewServer(&Controller{logger: zap.NewNop()}))
	go func() { _ = grpcSrv.Serve(grpcLis) }()
	defer grpcSrv.Stop()

	target := fmt.Sprintf("0.0.0.0:%d", grpcLis.Addr().(*net.TCPAddr).Port)
	mux := runtime.NewServeMux()
	require.NoError(t, sdspb.RegisterSDSControllerHandlerFromEndpoint(
		context.Background(), mux, target, loopbackDialOptions(nil)))

	req := httptest.NewRequest(http.MethodGet, "/v1/logs", nil)
	rec := httptest.NewRecorder()
	done := make(chan struct{})
	go func() { defer close(done); mux.ServeHTTP(rec, req) }()
	select {
	case <-done:
	case <-time.After(15 * time.Second):
		t.Fatal("gateway request hung — the dial was probably sent to the proxy")
	}

	assert.NotEqual(t, http.StatusServiceUnavailable, rec.Code,
		"503 means the dial went through the proxy: %s", rec.Body.String())
	assert.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
}
