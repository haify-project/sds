package metrics

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"google.golang.org/grpc"
)

func TestUnaryServerInterceptor(t *testing.T) {
	m := getTestMetrics(t)

	interceptor := m.UnaryServerInterceptor()
	assert.NotNil(t, interceptor)

	// Test with successful handler
	handlerCalled := false
	handler := func(ctx context.Context, req interface{}) (interface{}, error) {
		handlerCalled = true
		return "response", nil
	}

	info := &grpc.UnaryServerInfo{
		FullMethod: "/v1.SDSController/ListPools",
	}

	resp, err := interceptor(context.Background(), nil, info, handler)
	assert.NoError(t, err)
	assert.Equal(t, "response", resp)
	assert.True(t, handlerCalled)

	// Verify metrics were recorded
	h := m.Handler()
	req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	body := rec.Body.String()
	assert.Contains(t, body, "sds_controller_grpc_requests_total")
	assert.Contains(t, body, `method="/v1.SDSController/ListPools"`)
	assert.Contains(t, body, `status="OK"`)
}

func TestUnaryServerInterceptorWithError(t *testing.T) {
	m := getTestMetrics(t)

	interceptor := m.UnaryServerInterceptor()

	testErr := errors.New("test error")
	handler := func(ctx context.Context, req interface{}) (interface{}, error) {
		return nil, testErr
	}

	info := &grpc.UnaryServerInfo{
		FullMethod: "/v1.SDSController/CreatePool",
	}

	resp, err := interceptor(context.Background(), nil, info, handler)
	assert.Error(t, err)
	assert.Nil(t, resp)

	// Verify metrics recorded the error status
	h := m.Handler()
	req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	body := rec.Body.String()
	assert.Contains(t, body, `method="/v1.SDSController/CreatePool"`)
	// The status code will be determined by grpc/status.Code()
	assert.Contains(t, body, "sds_controller_grpc_requests_total")
}

func TestStreamServerInterceptor(t *testing.T) {
	m := getTestMetrics(t)

	interceptor := m.StreamServerInterceptor()
	assert.NotNil(t, interceptor)

	// Test with successful handler
	handlerCalled := false
	handler := func(srv interface{}, ss grpc.ServerStream) error {
		handlerCalled = true
		return nil
	}

	info := &grpc.StreamServerInfo{
		FullMethod: "/v1.SDSController/StreamEvents",
	}

	err := interceptor(nil, nil, info, handler)
	assert.NoError(t, err)
	assert.True(t, handlerCalled)

	// Verify metrics were recorded
	h := m.Handler()
	req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	body := rec.Body.String()
	assert.Contains(t, body, `method="/v1.SDSController/StreamEvents"`)
	assert.Contains(t, body, `status="OK"`)
}

func TestStreamServerInterceptorWithError(t *testing.T) {
	m := getTestMetrics(t)

	interceptor := m.StreamServerInterceptor()

	testErr := errors.New("stream error")
	handler := func(srv interface{}, ss grpc.ServerStream) error {
		return testErr
	}

	info := &grpc.StreamServerInfo{
		FullMethod: "/v1.SDSController/StreamData",
	}

	err := interceptor(nil, nil, info, handler)
	assert.Error(t, err)

	// Verify metrics recorded the error
	h := m.Handler()
	req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	body := rec.Body.String()
	assert.Contains(t, body, `method="/v1.SDSController/StreamData"`)
}

func TestChainUnaryServer(t *testing.T) {
	// Track execution order
	var order []string

	interceptor1 := func(ctx context.Context, req interface{}, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (interface{}, error) {
		order = append(order, "interceptor1")
		return handler(ctx, req)
	}

	interceptor2 := func(ctx context.Context, req interface{}, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (interface{}, error) {
		order = append(order, "interceptor2")
		return handler(ctx, req)
	}

	handler := func(ctx context.Context, req interface{}) (interface{}, error) {
		order = append(order, "handler")
		return "response", nil
	}

	// Chain interceptors
	chained := ChainUnaryServer(interceptor1, interceptor2)

	info := &grpc.UnaryServerInfo{
		FullMethod: "/v1.Test/Method",
	}

	resp, err := chained(context.Background(), nil, info, handler)
	assert.NoError(t, err)
	assert.Equal(t, "response", resp)

	// Verify execution order (interceptor1 should be outermost)
	assert.Equal(t, []string{"interceptor1", "interceptor2", "handler"}, order)
}

func TestChainUnaryServerEmpty(t *testing.T) {
	// Test with no interceptors
	chained := ChainUnaryServer()

	handler := func(ctx context.Context, req interface{}) (interface{}, error) {
		return "response", nil
	}

	info := &grpc.UnaryServerInfo{
		FullMethod: "/v1.Test/Method",
	}

	resp, err := chained(context.Background(), nil, info, handler)
	assert.NoError(t, err)
	assert.Equal(t, "response", resp)
}

func TestWrapInterceptor(t *testing.T) {
	// Create a simple interceptor that adds a prefix to the response
	interceptor := func(ctx context.Context, req interface{}, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (interface{}, error) {
		resp, err := handler(ctx, req)
		if err != nil {
			return nil, err
		}
		return "intercepted:" + resp.(string), nil
	}

	handler := func(ctx context.Context, req interface{}) (interface{}, error) {
		return "response", nil
	}

	info := &grpc.UnaryServerInfo{
		FullMethod: "/v1.Test/Method",
	}

	wrapped := wrapInterceptor(interceptor, handler, info)
	assert.NotNil(t, wrapped)

	resp, err := wrapped(context.Background(), nil)
	assert.NoError(t, err)
	assert.Equal(t, "intercepted:response", resp)
}
