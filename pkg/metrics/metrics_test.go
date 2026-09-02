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
	dto "github.com/prometheus/client_model/go"
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

	m.RecordResourceCount("healthy", 5)
	m.RecordResourceCount("degraded", 2)

	assert.Equal(t, 5.0, requireGauge(t, m, "sds_controller_resources", labels{"state": "healthy"}))
	assert.Equal(t, 2.0, requireGauge(t, m, "sds_controller_resources", labels{"state": "degraded"}))
}

func TestSetPoolCapacitiesExportsTotalUsedAndFree(t *testing.T) {
	m := getTestMetrics(t)

	m.SetPoolCapacities([]PoolCapacity{{Pool: "vg0", Node: "node-a", Total: 1024, Used: 400}})

	id := func(state string) labels {
		return labels{"pool": "vg0", "node": "node-a", "state": state}
	}
	assert.Equal(t, 1024.0, requireGauge(t, m, capacityMetric, id("total")))
	assert.Equal(t, 400.0, requireGauge(t, m, capacityMetric, id("used")))
	assert.Equal(t, 624.0, requireGauge(t, m, capacityMetric, id("free")))
}

// Every node in an SDS cluster tends to carry a volume group of the same name,
// so the pool name alone does not identify a series. Without the node label the
// second node's capacity overwrote the first's, and the cluster read as half
// its real size.
func TestSetPoolCapacitiesKeepsSameNamedPoolsOnDifferentNodesApart(t *testing.T) {
	m := getTestMetrics(t)

	m.SetPoolCapacities([]PoolCapacity{
		{Pool: "vg0", Node: "node-a", Total: 1000, Used: 100},
		{Pool: "vg0", Node: "node-b", Total: 2000, Used: 200},
	})

	assert.Equal(t, 1000.0, requireGauge(t, m, capacityMetric, labels{"pool": "vg0", "node": "node-a", "state": "total"}))
	assert.Equal(t, 2000.0, requireGauge(t, m, capacityMetric, labels{"pool": "vg0", "node": "node-b", "state": "total"}))
}

// Nothing else ever zeroes a pool that was deleted or whose node stopped
// answering, so a per-pool setter kept exporting its last known capacity for
// the life of the process. The view is a replacement precisely so the series
// disappears with the pool.
func TestSetPoolCapacitiesDropsPoolsMissingFromTheNewView(t *testing.T) {
	m := getTestMetrics(t)

	m.SetPoolCapacities([]PoolCapacity{
		{Pool: "vg0", Node: "node-a", Total: 1000, Used: 100},
		{Pool: "vg1", Node: "node-a", Total: 2000, Used: 200},
	})
	m.SetPoolCapacities([]PoolCapacity{{Pool: "vg0", Node: "node-a", Total: 1000, Used: 100}})

	_, stillThere := gaugeValue(t, m, capacityMetric, labels{"pool": "vg1", "node": "node-a", "state": "total"})
	assert.False(t, stillThere, "a pool absent from the new view must stop being exported, not linger at its last value")
	assert.Equal(t, 1000.0, requireGauge(t, m, capacityMetric, labels{"pool": "vg0", "node": "node-a", "state": "total"}))
}

// A pool that could not report its size has an unknown free figure, not a full
// one. Subtracting a larger used from a smaller total would export a wrapped
// unsigned value; omitting the series says "unknown" honestly.
func TestSetPoolCapacitiesOmitsFreeWhenTotalIsBelowUsed(t *testing.T) {
	m := getTestMetrics(t)

	m.SetPoolCapacities([]PoolCapacity{{Pool: "vg1", Node: "node-a", Total: 0, Used: 100}})

	_, ok := gaugeValue(t, m, capacityMetric, labels{"pool": "vg1", "node": "node-a", "state": "free"})
	assert.False(t, ok, "free must be omitted, never derived, when the total is not credible")
	assert.Equal(t, 100.0, requireGauge(t, m, capacityMetric, labels{"pool": "vg1", "node": "node-a", "state": "used"}))
}

func TestRecordNodeState(t *testing.T) {
	m := getTestMetrics(t)

	m.RecordNodeState("online", 3)
	m.RecordNodeState("offline", 1)

	assert.Equal(t, 3.0, requireGauge(t, m, "sds_controller_nodes", labels{"state": "online"}))
	assert.Equal(t, 1.0, requireGauge(t, m, "sds_controller_nodes", labels{"state": "offline"}))
}

func TestSetGatewayCounts(t *testing.T) {
	m := getTestMetrics(t)

	m.SetGatewayCounts([]GatewayCount{
		{Type: "nfs", State: "started", Count: 2},
		{Type: "iscsi", State: "stopped", Count: 1},
	})

	assert.Equal(t, 2.0, requireGauge(t, m, gatewayMetric, labels{"type": "nfs", "state": "started"}))
	assert.Equal(t, 1.0, requireGauge(t, m, gatewayMetric, labels{"type": "iscsi", "state": "stopped"}))
}

// A gateway that moves from started to stopped writes the new state, but only a
// replacement view retires the old one — otherwise the same gateway is counted
// in both states at once, and the panel shows more gateways than exist.
func TestSetGatewayCountsDropsStatesMissingFromTheNewView(t *testing.T) {
	m := getTestMetrics(t)

	m.SetGatewayCounts([]GatewayCount{{Type: "nfs", State: "started", Count: 1}})
	m.SetGatewayCounts([]GatewayCount{{Type: "nfs", State: "stopped", Count: 1}})

	_, stillStarted := gaugeValue(t, m, gatewayMetric, labels{"type": "nfs", "state": "started"})
	assert.False(t, stillStarted, "the state a gateway left must stop being exported")
	assert.Equal(t, 1.0, requireGauge(t, m, gatewayMetric, labels{"type": "nfs", "state": "stopped"}))
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

// Steady state is the case that matters. drbdsetup omits the completion field
// entirely once a resync has finished, so a reader that treats the absence as
// zero paints a fully in-sync cluster as one whose resync never started — and
// that is the reading an operator acts on at three in the morning.
func TestSetReplicationExportsSteadyStateAsFullySynced(t *testing.T) {
	m := getTestMetrics(t)

	m.SetReplication(ReplicationSnapshot{
		Resources: []string{"data"},
		Replicas: []ReplicaState{{
			Resource: "data", Node: "node-a",
			Role: "Primary", DiskState: "UpToDate",
			SyncPercent: ptr(100.0), Quorum: ptr(true),
		}},
	})

	id := labels{"resource": "data", "node": "node-a"}
	assert.Equal(t, 1.0, requireGauge(t, m, "sds_drbd_resync_completed_ratio", id))
	assert.Equal(t, 1.0, requireGauge(t, m, "sds_drbd_quorum", id))
	assert.Equal(t, 1.0, requireGauge(t, m, "sds_drbd_role", labels{"resource": "data", "node": "node-a", "role": "Primary"}))
	assert.Equal(t, 1.0, requireGauge(t, m, "sds_drbd_disk_state", labels{"resource": "data", "node": "node-a", "state": "UpToDate"}))
	assert.Equal(t, 1.0, requireGauge(t, m, "sds_drbd_resource_up", labels{"resource": "data"}))
}

// A replica that did not report is absent, never zero. Zero is a real reading —
// "0% synced", "quorum lost" — and inventing it for an unreachable node turns a
// network problem into a data-loss alarm.
func TestSetReplicationOmitsAReplicaThatStoppedReporting(t *testing.T) {
	m := getTestMetrics(t)

	both := []ReplicaState{
		{Resource: "data", Node: "node-a", DiskState: "UpToDate", SyncPercent: ptr(100.0)},
		{Resource: "data", Node: "node-b", DiskState: "UpToDate", SyncPercent: ptr(100.0)},
	}
	m.SetReplication(ReplicationSnapshot{Resources: []string{"data"}, Replicas: both})
	m.SetReplication(ReplicationSnapshot{Resources: []string{"data"}, Replicas: both[:1]})

	_, ok := gaugeValue(t, m, "sds_drbd_resync_completed_ratio", labels{"resource": "data", "node": "node-b"})
	assert.False(t, ok, "an unreachable replica must vanish from /metrics, not read as 0%% synced")
	assert.Equal(t, 1.0, requireGauge(t, m, "sds_drbd_resync_completed_ratio", labels{"resource": "data", "node": "node-a"}))
	// The resource itself still answered through node-a.
	assert.Equal(t, 1.0, requireGauge(t, m, "sds_drbd_resource_up", labels{"resource": "data"}))
}

// resource_up is how "no node answered for this resource" is said out loud.
// Without it a resource whose every replica is unreachable is indistinguishable
// from one that was deleted.
func TestSetReplicationReportsResourceDownWhenNoReplicaAnswered(t *testing.T) {
	m := getTestMetrics(t)

	m.SetReplication(ReplicationSnapshot{Resources: []string{"data", "quiet"}, Replicas: []ReplicaState{
		{Resource: "data", Node: "node-a", DiskState: "UpToDate"},
	}})

	assert.Equal(t, 1.0, requireGauge(t, m, "sds_drbd_resource_up", labels{"resource": "data"}))
	assert.Equal(t, 0.0, requireGauge(t, m, "sds_drbd_resource_up", labels{"resource": "quiet"}))
	_, ok := gaugeValue(t, m, "sds_drbd_disk_state", labels{"resource": "quiet", "node": "node-a", "state": "UpToDate"})
	assert.False(t, ok, "a resource nobody answered for must export no replica series at all")
}

// Quorum has three states, not two: held, lost, and never reported. Only the
// node that was actually queried can speak for its own quorum, so a nil must
// stay off /metrics rather than become a confident zero.
func TestSetReplicationDistinguishesLostQuorumFromUnreportedQuorum(t *testing.T) {
	m := getTestMetrics(t)

	m.SetReplication(ReplicationSnapshot{Resources: []string{"data"}, Replicas: []ReplicaState{
		{Resource: "data", Node: "node-a", Quorum: ptr(false)},
		{Resource: "data", Node: "node-b"},
	}})

	assert.Equal(t, 0.0, requireGauge(t, m, "sds_drbd_quorum", labels{"resource": "data", "node": "node-a"}))
	_, ok := gaugeValue(t, m, "sds_drbd_quorum", labels{"resource": "data", "node": "node-b"})
	assert.False(t, ok, "an unreported quorum must not be exported as quorum lost")
}

// An empty state string is a source that did not say, not a state named "".
func TestSetReplicationSkipsEmptyStateLabels(t *testing.T) {
	m := getTestMetrics(t)

	m.SetReplication(ReplicationSnapshot{Resources: []string{"data"}, Replicas: []ReplicaState{
		{Resource: "data", Node: "node-a", Role: "", DiskState: "UpToDate", ReplicationState: ""},
	}})

	_, roleExported := gaugeValue(t, m, "sds_drbd_role", labels{"resource": "data", "node": "node-a", "role": ""})
	assert.False(t, roleExported, "an unknown role must not be exported as an empty label value")
	_, replExported := gaugeValue(t, m, "sds_drbd_replication_state", labels{"resource": "data", "node": "node-a", "state": ""})
	assert.False(t, replExported)
}

// A replica can be present and still not report its resync figure — the field
// is simply absent outside a resync. Exporting the nil as 0 would put a fully
// healthy replica on a dashboard at "0% synced", which is the same lie as
// zeroing an unreachable node, told about a node that answered.
func TestSetReplicationOmitsResyncWhenTheReplicaDidNotReportIt(t *testing.T) {
	m := getTestMetrics(t)

	m.SetReplication(ReplicationSnapshot{Resources: []string{"data"}, Replicas: []ReplicaState{
		{Resource: "data", Node: "node-a", DiskState: "UpToDate", SyncPercent: nil},
	}})

	_, ok := gaugeValue(t, m, "sds_drbd_resync_completed_ratio", labels{"resource": "data", "node": "node-a"})
	assert.False(t, ok, "an unreported resync figure must be absent, not exported as 0%%")
	// The replica itself is still very much there.
	assert.Equal(t, 1.0, requireGauge(t, m, "sds_drbd_disk_state", labels{"resource": "data", "node": "node-a", "state": "UpToDate"}))
}

// DRBD has been seen to report slightly over 100 on a finishing resync. A ratio
// above 1 on a panel labelled "percent complete" reads as a bug in the cluster
// rather than in the reporting.
func TestSetReplicationClampsResyncRatio(t *testing.T) {
	m := getTestMetrics(t)

	m.SetReplication(ReplicationSnapshot{Resources: []string{"data"}, Replicas: []ReplicaState{
		{Resource: "data", Node: "node-a", SyncPercent: ptr(105.0)},
		{Resource: "data", Node: "node-b", SyncPercent: ptr(-3.0)},
	}})

	assert.Equal(t, 1.0, requireGauge(t, m, "sds_drbd_resync_completed_ratio", labels{"resource": "data", "node": "node-a"}))
	assert.Equal(t, 0.0, requireGauge(t, m, "sds_drbd_resync_completed_ratio", labels{"resource": "data", "node": "node-b"}))
}

// The gauges keep their last good value when a source fails, so staleness has
// to be visible somewhere else. A failed observation must not move the clock
// forward, or "current" and "forty minutes old" become the same reading.
func TestRecordObservationAdvancesTheClockOnlyOnSuccess(t *testing.T) {
	m := getTestMetrics(t)

	_, before := gaugeValue(t, m, observedMetric, labels{"source": "resources"})
	require.False(t, before, "no observation has been recorded yet")

	m.RecordObservation("resources", false, 0.01)
	_, afterFailure := gaugeValue(t, m, observedMetric, labels{"source": "resources"})
	assert.False(t, afterFailure, "a failed read must not stamp a fresh observation time")

	m.RecordObservation("resources", true, 0.01)
	stamped := requireGauge(t, m, observedMetric, labels{"source": "resources"})
	assert.Greater(t, stamped, 0.0)

	// Both outcomes are still counted, which is what makes a source that is
	// failing every poll visible at all.
	body := scrape(t, m)
	assert.Contains(t, body, `operation="observe_resources"`)
	assert.Contains(t, body, `result="error"`)
	assert.Contains(t, body, `result="success"`)
}

func TestResetMetrics(t *testing.T) {
	m := getTestMetrics(t)

	// Record some metrics
	m.RecordResourceCount("healthy", 5)
	m.RecordNodeState("online", 3)
	m.SetGatewayCounts([]GatewayCount{{Type: "nfs", State: "started", Count: 2}})

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
				m.RecordResourceCount("healthy", 5)
				m.RecordNodeState("online", 5)
				m.SetGatewayCounts([]GatewayCount{{Type: "nfs", State: "started", Count: 1}})
				m.SetReplication(ReplicationSnapshot{Resources: []string{"data"}, Replicas: []ReplicaState{{Resource: "data", Node: "node-a", DiskState: "UpToDate", SyncPercent: ptr(100.0)}}})
				m.RecordObservation("resources", true, 0.01)
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

// Metric names spelled out once. A test that asserts on the wrong name passes
// vacuously against gaugeValue's "not exported" answer, so the names live here
// rather than being retyped per test.
const (
	capacityMetric = "sds_controller_storage_capacity_bytes"
	gatewayMetric  = "sds_controller_gateways"
	observedMetric = "sds_controller_last_observation_timestamp_seconds"
)

// labels is a shorthand for the label sets these assertions are made of.
type labels map[string]string

// gaugeValue reports one series' value and whether it is exported at all.
//
// It deliberately does not go through GaugeVec.With, which creates a series as
// a side effect of looking one up and would make every "this must be absent"
// assertion pass. Absence is returned separately from zero because telling
// those two apart is the entire point of the metrics under test.
func gaugeValue(t *testing.T, m *Metrics, name string, want labels) (float64, bool) {
	t.Helper()
	families, err := m.registry.Gather()
	require.NoError(t, err)
	for _, f := range families {
		if f.GetName() != name {
			continue
		}
		for _, metric := range f.GetMetric() {
			if labelsEqual(metric.GetLabel(), want) {
				return metric.GetGauge().GetValue(), true
			}
		}
	}
	return 0, false
}

// requireGauge is gaugeValue for the cases that must exist, failing with the
// series name rather than silently reading zero.
func requireGauge(t *testing.T, m *Metrics, name string, want labels) float64 {
	t.Helper()
	v, ok := gaugeValue(t, m, name, want)
	require.Truef(t, ok, "series %s%v is not exported", name, want)
	return v
}

func labelsEqual(got []*dto.LabelPair, want labels) bool {
	if len(got) != len(want) {
		return false
	}
	for _, pair := range got {
		if v, ok := want[pair.GetName()]; !ok || v != pair.GetValue() {
			return false
		}
	}
	return true
}

// scrape renders /metrics the way Prometheus would read it.
func scrape(t *testing.T, m *Metrics) string {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	rec := httptest.NewRecorder()
	m.Handler().ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code)
	return rec.Body.String()
}

func ptr[T any](v T) *T { return &v }
