package controller

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

const testToken = "test-token-0123456789"

func callUnary(t *testing.T, ctx context.Context, method string) error {
	t.Helper()
	interceptor := authUnaryInterceptor(testToken)
	_, err := interceptor(ctx, nil,
		&grpc.UnaryServerInfo{FullMethod: method},
		func(ctx context.Context, req interface{}) (interface{}, error) { return "ok", nil })
	return err
}

func TestAuthUnaryInterceptor(t *testing.T) {
	method := "/v1.HaifyController/ListPools"

	t.Run("missing metadata rejected", func(t *testing.T) {
		err := callUnary(t, context.Background(), method)
		assert.Equal(t, codes.Unauthenticated, status.Code(err))
	})

	t.Run("wrong token rejected", func(t *testing.T) {
		ctx := metadata.NewIncomingContext(context.Background(),
			metadata.Pairs("authorization", "Bearer wrong-token-000000"))
		err := callUnary(t, ctx, method)
		assert.Equal(t, codes.Unauthenticated, status.Code(err))
	})

	t.Run("valid bearer token accepted", func(t *testing.T) {
		ctx := metadata.NewIncomingContext(context.Background(),
			metadata.Pairs("authorization", "Bearer "+testToken))
		require.NoError(t, callUnary(t, ctx, method))
	})

	t.Run("grpc-gateway header key accepted", func(t *testing.T) {
		// The gateway's default header matcher forwards the HTTP
		// Authorization header as grpcgateway-authorization.
		ctx := metadata.NewIncomingContext(context.Background(),
			metadata.Pairs("grpcgateway-authorization", "Bearer "+testToken))
		require.NoError(t, callUnary(t, ctx, method))
	})

	t.Run("token without Bearer prefix accepted", func(t *testing.T) {
		ctx := metadata.NewIncomingContext(context.Background(),
			metadata.Pairs("authorization", testToken))
		require.NoError(t, callUnary(t, ctx, method))
	})

	t.Run("health service exempt", func(t *testing.T) {
		require.NoError(t, callUnary(t, context.Background(), "/grpc.health.v1.Health/Check"))
	})

	t.Run("empty client token against empty config still requires match", func(t *testing.T) {
		// Defense in depth: an empty configured token never validates via
		// the interceptor path (config validation rejects short tokens, but
		// the interceptor must not treat empty == empty as success for an
		// unauthenticated request with no header at all).
		interceptor := authUnaryInterceptor("")
		_, err := interceptor(context.Background(), nil,
			&grpc.UnaryServerInfo{FullMethod: method},
			func(ctx context.Context, req interface{}) (interface{}, error) { return "ok", nil })
		assert.Equal(t, codes.Unauthenticated, status.Code(err))
	})
}

type fakeServerStream struct {
	grpc.ServerStream
	ctx context.Context
}

func (f fakeServerStream) Context() context.Context { return f.ctx }

func TestAuthStreamInterceptor(t *testing.T) {
	interceptor := authStreamInterceptor(testToken)
	handler := func(srv interface{}, ss grpc.ServerStream) error { return nil }

	t.Run("missing token rejected", func(t *testing.T) {
		err := interceptor(nil, fakeServerStream{ctx: context.Background()},
			&grpc.StreamServerInfo{FullMethod: "/v1.HaifyController/Watch"}, handler)
		assert.Equal(t, codes.Unauthenticated, status.Code(err))
	})

	t.Run("valid token accepted", func(t *testing.T) {
		ctx := metadata.NewIncomingContext(context.Background(),
			metadata.Pairs("authorization", "Bearer "+testToken))
		err := interceptor(nil, fakeServerStream{ctx: ctx},
			&grpc.StreamServerInfo{FullMethod: "/v1.HaifyController/Watch"}, handler)
		require.NoError(t, err)
	})
}
