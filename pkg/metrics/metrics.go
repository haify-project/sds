// Package metrics provides Prometheus metrics support for the SDS controller
package metrics

import (
	"net/http"
	"sync"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"go.uber.org/zap"
)

const (
	namespace = "sds"
	subsystem = "controller"
)

// Metrics holds all Prometheus metrics for the SDS controller
type Metrics struct {
	logger   *zap.Logger
	registry *prometheus.Registry

	// Operations counter tracks total operations with result status
	operationsTotal *prometheus.CounterVec

	// Operation duration histogram tracks operation latency
	operationDuration *prometheus.HistogramVec

	// Resources gauge tracks resource counts by type
	resources *prometheus.GaugeVec

	// Storage capacity tracks pool storage in bytes
	storageCapacity *prometheus.GaugeVec

	// Nodes gauge tracks node counts by state
	nodes *prometheus.GaugeVec

	// Gateways gauge tracks gateway counts by type and state
	gateways *prometheus.GaugeVec

	// gRPC requests counter
	grpcRequestsTotal *prometheus.CounterVec

	// gRPC request duration histogram
	grpcRequestDuration *prometheus.HistogramVec

	// Up gauge indicates the instance is available (always 1)
	up prometheus.Gauge

	// Go runtime metrics
	goRuntimeMetrics *prometheus.CounterVec

	// Process metrics
	processMetrics *prometheus.GaugeVec

	mu sync.Mutex
}

// New creates and registers all Prometheus metrics
func New(logger *zap.Logger) (*Metrics, error) {
	registry := prometheus.NewRegistry()

	m := &Metrics{
		logger:   logger,
		registry: registry,
		operationsTotal: promauto.NewCounterVec(
			prometheus.CounterOpts{
				Namespace: namespace,
				Subsystem: subsystem,
				Name:      "operations_total",
				Help:      "Total number of operations performed by result",
			},
			[]string{"operation", "result"},
		),
		operationDuration: promauto.NewHistogramVec(
			prometheus.HistogramOpts{
				Namespace: namespace,
				Subsystem: subsystem,
				Name:      "operation_duration_seconds",
				Help:      "Operation duration in seconds",
				Buckets:   prometheus.DefBuckets,
			},
			[]string{"operation"},
		),
		resources: promauto.NewGaugeVec(
			prometheus.GaugeOpts{
				Namespace: namespace,
				Subsystem: subsystem,
				Name:      "resources",
				Help:      "Number of resources by type",
			},
			[]string{"type"},
		),
		storageCapacity: promauto.NewGaugeVec(
			prometheus.GaugeOpts{
				Namespace: namespace,
				Subsystem: subsystem,
				Name:      "storage_capacity_bytes",
				Help:      "Storage capacity in bytes by pool and state",
			},
			[]string{"pool", "state"},
		),
		nodes: promauto.NewGaugeVec(
			prometheus.GaugeOpts{
				Namespace: namespace,
				Subsystem: subsystem,
				Name:      "nodes",
				Help:      "Number of nodes by state",
			},
			[]string{"state"},
		),
		gateways: promauto.NewGaugeVec(
			prometheus.GaugeOpts{
				Namespace: namespace,
				Subsystem: subsystem,
				Name:      "gateways",
				Help:      "Number of gateways by type and state",
			},
			[]string{"type", "state"},
		),
		grpcRequestsTotal: promauto.NewCounterVec(
			prometheus.CounterOpts{
				Namespace: namespace,
				Subsystem: subsystem,
				Name:      "grpc_requests_total",
				Help:      "Total number of gRPC requests by method and status",
			},
			[]string{"method", "status"},
		),
		grpcRequestDuration: promauto.NewHistogramVec(
			prometheus.HistogramOpts{
				Namespace: namespace,
				Subsystem: subsystem,
				Name:      "grpc_request_duration_seconds",
				Help:      "gRPC request duration in seconds",
				Buckets:   prometheus.DefBuckets,
			},
			[]string{"method"},
		),
		up: promauto.NewGauge(
			prometheus.GaugeOpts{
				Namespace: namespace,
				Subsystem: subsystem,
				Name:      "up",
				Help:      "Indicates the SDS controller instance is available (always 1)",
			},
		),
	}

	// Register all custom metrics with the custom registry
	m.registry.MustRegister(
		m.operationsTotal,
		m.operationDuration,
		m.resources,
		m.storageCapacity,
		m.nodes,
		m.gateways,
		m.grpcRequestsTotal,
		m.grpcRequestDuration,
		m.up,
	)

	// Set up to 1
	m.up.Set(1)

	// Register Go and process collectors with the custom registry
	m.registry.MustRegister(prometheus.NewGoCollector())
	m.registry.MustRegister(prometheus.NewProcessCollector(prometheus.ProcessCollectorOpts{}))

	logger.Info("Prometheus metrics initialized")
	return m, nil
}

// Handler returns the HTTP handler for the /metrics endpoint
func (m *Metrics) Handler() http.Handler {
	return promhttp.HandlerFor(m.registry, promhttp.HandlerOpts{
		EnableOpenMetrics: true,
	})
}

// RecordOperation records an operation with its status and duration
func (m *Metrics) RecordOperation(operation, status string, duration float64) {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.operationsTotal.WithLabelValues(operation, status).Inc()
	m.operationDuration.WithLabelValues(operation).Observe(duration)
}

// RecordResourceCount sets the count for a specific resource type
func (m *Metrics) RecordResourceCount(resourceType string, count float64) {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.resources.WithLabelValues(resourceType).Set(count)
}

// IncrementResourceCount adds to the count for a specific resource type
func (m *Metrics) IncrementResourceCount(resourceType string) {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.resources.WithLabelValues(resourceType).Inc()
}

// DecrementResourceCount subtracts from the count for a specific resource type
func (m *Metrics) DecrementResourceCount(resourceType string) {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.resources.WithLabelValues(resourceType).Dec()
}

// RecordStorageCapacity records storage capacity for a pool
func (m *Metrics) RecordStorageCapacity(pool string, used, total float64) {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.storageCapacity.WithLabelValues(pool, "used").Set(used)
	m.storageCapacity.WithLabelValues(pool, "total").Set(total)
	if total > 0 {
		m.storageCapacity.WithLabelValues(pool, "free").Set(total - used)
	}
}

// RecordNodeState records the count of nodes in a specific state
func (m *Metrics) RecordNodeState(state string, count float64) {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.nodes.WithLabelValues(state).Set(count)
}

// IncrementNodeCount increments the count for a node state
func (m *Metrics) IncrementNodeCount(state string) {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.nodes.WithLabelValues(state).Inc()
}

// DecrementNodeCount decrements the count for a node state
func (m *Metrics) DecrementNodeCount(state string) {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.nodes.WithLabelValues(state).Dec()
}

// RecordGatewayState records the count of gateways by type and state
func (m *Metrics) RecordGatewayState(gatewayType, state string, count float64) {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.gateways.WithLabelValues(gatewayType, state).Set(count)
}

// IncrementGatewayCount increments the count for a gateway type and state
func (m *Metrics) IncrementGatewayCount(gatewayType, state string) {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.gateways.WithLabelValues(gatewayType, state).Inc()
}

// DecrementGatewayCount decrements the count for a gateway type and state
func (m *Metrics) DecrementGatewayCount(gatewayType, state string) {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.gateways.WithLabelValues(gatewayType, state).Dec()
}

// RecordGRPCRequest records a gRPC request with method, status, and duration
func (m *Metrics) RecordGRPCRequest(method, status string, duration float64) {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.grpcRequestsTotal.WithLabelValues(method, status).Inc()
	m.grpcRequestDuration.WithLabelValues(method).Observe(duration)
}

// IncrementOperationsCounter increments the operations counter for a given operation and result
func (m *Metrics) IncrementOperationsCounter(operation, result string) {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.operationsTotal.WithLabelValues(operation, result).Inc()
}

// ResetMetrics resets all metrics to zero (useful for testing)
func (m *Metrics) ResetMetrics() {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.operationsTotal.Reset()
	m.operationDuration.Reset()
	m.resources.Reset()
	m.storageCapacity.Reset()
	m.nodes.Reset()
	m.gateways.Reset()
	m.grpcRequestsTotal.Reset()
	m.grpcRequestDuration.Reset()
}

// GetRegistry returns the Prometheus registry
func (m *Metrics) GetRegistry() *prometheus.Registry {
	return m.registry
}
