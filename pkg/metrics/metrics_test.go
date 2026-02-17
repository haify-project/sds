package metrics

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
	"google.golang.org/grpc"
)

var (
	testMetrics     *Metrics
	testMetricsOnce sync.Once
	testLogger      = zap.NewNop()
)

// getTestMetrics returns a shared Metrics instance for testing
func getTestMetrics(t *testing.T) *Metrics {
	testMetricsOnce.Do(func() {
		var err error
		testMetrics, err = New(testLogger)
		require.NoError(t, err)
	})
	// Reset metrics before each test
	testMetrics.ResetMetrics()
	return testMetrics
}

func TestNew(t *testing.T) {
	// This test verifies the structure, not creating a new instance
	// since promauto uses the default registry
	m := getTestMetrics(t)
	require.NotNil(t, m)

	assert.NotNil(t, m.registry)
	assert.NotNil(t, m.operationsTotal)
	assert.NotNil(t, m.operationDuration)
	assert.NotNil(t, m.resources)
	assert.NotNil(t, m.storageCapacity)
	assert.NotNil(t, m.nodes)
	assert.NotNil(t, m.gateways)
	assert.NotNil(t, m.grpcRequestsTotal)
	assert.NotNil(t, m.grpcRequestDuration)
	assert.NotNil(t, m.up)
}

func TestHandler(t *testing.T) {
	m := getTestMetrics(t)

	handler := m.Handler()
	assert.NotNil(t, handler)

	// Test that handler returns valid Prometheus metrics
	req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)

	// Check that sds_controller_up metric is present
	body := rec.Body.String()
	assert.Contains(t, body, "sds_controller_up")
}

func TestRecordOperation(t *testing.T) {
	m := getTestMetrics(t)

	m.RecordOperation("create_pool", "success", 0.5)

	// Verify the metric was recorded via the handler
	handler := m.Handler()
	req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	body := rec.Body.String()
	assert.Contains(t, body, "sds_controller_operations_total")
	assert.Contains(t, body, `operation="create_pool"`)
	assert.Contains(t, body, `result="success"`)
}

func TestRecordResourceCount(t *testing.T) {
	m := getTestMetrics(t)

	m.RecordResourceCount("pool", 5)
	m.RecordResourceCount("resource", 10)

	handler := m.Handler()
	req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	body := rec.Body.String()
	assert.Contains(t, body, "sds_controller_resources")
	assert.Contains(t, body, `type="pool"`)
	assert.Contains(t, body, `type="resource"`)
}

func TestIncrementDecrementResourceCount(t *testing.T) {
	m := getTestMetrics(t)

	m.RecordResourceCount("volume", 5)
	m.IncrementResourceCount("volume")
	m.IncrementResourceCount("volume")
	m.DecrementResourceCount("volume")

	handler := m.Handler()
	req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	body := rec.Body.String()
	// Should be 5 + 1 + 1 - 1 = 6
	assert.Contains(t, body, "sds_controller_resources")
}

func TestRecordStorageCapacity(t *testing.T) {
	m := getTestMetrics(t)

	m.RecordStorageCapacity("vg0", 500*1024*1024*1024, 1024*1024*1024*1024)

	handler := m.Handler()
	req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	body := rec.Body.String()
	assert.Contains(t, body, "sds_controller_storage_capacity_bytes")
	assert.Contains(t, body, `pool="vg0"`)
	assert.Contains(t, body, `state="used"`)
	assert.Contains(t, body, `state="total"`)
	assert.Contains(t, body, `state="free"`)
}

func TestRecordStorageCapacityZeroTotal(t *testing.T) {
	m := getTestMetrics(t)

	// When total is 0, free should not be negative
	m.RecordStorageCapacity("vg1", 100, 0)

	handler := m.Handler()
	req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	body := rec.Body.String()
	assert.Contains(t, body, "sds_controller_storage_capacity_bytes")
}

func TestRecordNodeState(t *testing.T) {
	m := getTestMetrics(t)

	m.RecordNodeState("online", 3)
	m.RecordNodeState("offline", 1)

	handler := m.Handler()
	req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	body := rec.Body.String()
	assert.Contains(t, body, "sds_controller_nodes")
	assert.Contains(t, body, `state="online"`)
	assert.Contains(t, body, `state="offline"`)
}

func TestIncrementDecrementNodeCount(t *testing.T) {
	m := getTestMetrics(t)

	m.RecordNodeState("online", 2)
	m.IncrementNodeCount("online")
	m.DecrementNodeCount("offline")

	handler := m.Handler()
	req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	body := rec.Body.String()
	assert.Contains(t, body, "sds_controller_nodes")
}

func TestRecordGatewayState(t *testing.T) {
	m := getTestMetrics(t)

	m.RecordGatewayState("nfs", "active", 2)
	m.RecordGatewayState("iscsi", "active", 1)
	m.RecordGatewayState("nvmeof", "inactive", 1)

	handler := m.Handler()
	req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	body := rec.Body.String()
	assert.Contains(t, body, "sds_controller_gateways")
	assert.Contains(t, body, `type="nfs"`)
	assert.Contains(t, body, `type="iscsi"`)
	assert.Contains(t, body, `type="nvmeof"`)
	assert.Contains(t, body, `state="active"`)
	assert.Contains(t, body, `state="inactive"`)
}

func TestIncrementDecrementGatewayCount(t *testing.T) {
	m := getTestMetrics(t)

	m.RecordGatewayState("nfs", "active", 1)
	m.IncrementGatewayCount("nfs", "active")
	m.DecrementGatewayCount("iscsi", "inactive")

	handler := m.Handler()
	req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	body := rec.Body.String()
	assert.Contains(t, body, "sds_controller_gateways")
}

func TestRecordGRPCRequest(t *testing.T) {
	m := getTestMetrics(t)

	m.RecordGRPCRequest("/v1.SDSController/ListPools", "OK", 0.05)
	m.RecordGRPCRequest("/v1.SDSController/CreatePool", "Unknown", 0.1)

	handler := m.Handler()
	req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	body := rec.Body.String()
	assert.Contains(t, body, "sds_controller_grpc_requests_total")
	assert.Contains(t, body, "sds_controller_grpc_request_duration_seconds")
	assert.Contains(t, body, `method="/v1.SDSController/ListPools"`)
	assert.Contains(t, body, `status="OK"`)
	assert.Contains(t, body, `status="Unknown"`)
}

func TestIncrementOperationsCounter(t *testing.T) {
	m := getTestMetrics(t)

	m.IncrementOperationsCounter("create_pool", "success")
	m.IncrementOperationsCounter("create_pool", "error")

	handler := m.Handler()
	req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	body := rec.Body.String()
	assert.Contains(t, body, "sds_controller_operations_total")
	assert.Contains(t, body, `operation="create_pool"`)
}

func TestResetMetrics(t *testing.T) {
	m := getTestMetrics(t)

	// Record some metrics
	m.RecordResourceCount("pool", 5)
	m.RecordNodeState("online", 3)
	m.RecordGatewayState("nfs", "active", 2)

	// Reset
	m.ResetMetrics()

	// Verify metrics were reset
	handler := m.Handler()
	req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	body := rec.Body.String()
	// The up metric should still be present
	assert.Contains(t, body, "sds_controller_up")
}

func TestGetRegistry(t *testing.T) {
	m := getTestMetrics(t)

	registry := m.GetRegistry()
	assert.NotNil(t, registry)
	assert.IsType(t, &prometheus.Registry{}, registry)
}

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

func TestUpMetric(t *testing.T) {
	m := getTestMetrics(t)

	h := m.Handler()
	req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	body := rec.Body.String()
	// The up gauge should be 1
	assert.True(t, strings.Contains(body, "sds_controller_up 1"))
}

func TestConcurrentAccess(t *testing.T) {
	m := getTestMetrics(t)

	// Test concurrent metric operations
	done := make(chan bool)

	for i := 0; i < 10; i++ {
		go func() {
			for j := 0; j < 100; j++ {
				m.RecordOperation("test", "success", 0.1)
				m.IncrementResourceCount("pool")
				m.DecrementResourceCount("pool")
				m.RecordNodeState("online", 5)
				m.RecordGatewayState("nfs", "active", 1)
				m.RecordGRPCRequest("/test", "OK", 0.05)
			}
			done <- true
		}()
	}

	// Wait for all goroutines
	for i := 0; i < 10; i++ {
		<-done
	}

	// Should not panic and metrics should be accessible
	handler := m.Handler()
	req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)
}
