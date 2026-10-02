// Package metrics provides Prometheus metrics support for the SDS controller.
//
// Two families live here. The `sds_controller_*` family describes the
// controller itself and the inventory it manages; the `sds_drbd_*` family
// describes live replication, one series per replica.
//
// The rule the whole package is built around: a number that was never observed
// must not be exported as zero. A flat zero on a dashboard is indistinguishable
// from a healthy idle cluster, so every gauge fed from a poll is only written
// when that poll actually answered, and series whose subject vanished are
// deleted rather than zeroed. See seriesSet and RecordObservation.
package metrics

import (
	"net/http"
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/client_golang/prometheus/promauto"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"go.uber.org/zap"
)

const (
	namespace = "sds"
	subsystem = "controller"
	// drbdSubsystem separates live replication state from controller
	// inventory. They answer different questions and are collected from
	// different places, and a single subsystem would have made
	// `sds_controller_disk_state` read as a property of the controller.
	drbdSubsystem = "drbd"
)

// Metrics holds all Prometheus metrics for the SDS controller
type Metrics struct {
	logger   *zap.Logger
	registry *prometheus.Registry

	// Operations counter tracks total operations with result status
	operationsTotal *prometheus.CounterVec

	// Operation duration histogram tracks operation latency
	operationDuration *prometheus.HistogramVec

	// Resources gauge tracks how many resources are healthy and how many are
	// degraded. Both label values are written on every successful observation,
	// so a zero here is a measured zero.
	resources *prometheus.GaugeVec

	// Storage capacity tracks pool storage in bytes
	storageCapacity *seriesSet

	// Nodes gauge tracks node counts by reachability
	nodes *prometheus.GaugeVec

	// Gateways gauge tracks gateway counts by type and state
	gateways *seriesSet

	// observedAt records when each data source last answered, so a dashboard
	// can tell a current reading from one frozen by a source that has been
	// failing for an hour. It is the companion to the "never write a zero we
	// did not measure" rule: keeping the last good value is only honest if the
	// staleness is visible.
	observedAt *prometheus.GaugeVec

	// gRPC requests counter
	grpcRequestsTotal *prometheus.CounterVec

	// gRPC request duration histogram
	grpcRequestDuration *prometheus.HistogramVec

	// Live DRBD replication, one series per replica. See SetReplication for
	// why the enum states are exported as a present-value series rather than
	// as a numeric code or a full 0/1 matrix.
	drbdRole             *seriesSet
	drbdDiskState        *seriesSet
	drbdReplicationState *seriesSet
	drbdResync           *seriesSet
	drbdQuorum           *seriesSet
	drbdResourceUp       *seriesSet

	// Up gauge indicates the instance is available (always 1)
	up prometheus.Gauge

	health healthSeries

	mu sync.Mutex
}

// New creates and registers all Prometheus metrics.
//
// Everything is registered into a registry owned by this instance rather than
// into prometheus.DefaultRegisterer. The default registerer is process-global,
// so registering there made New callable exactly once per process: a second
// call — a test, or a controller restarted in-process — panicked on duplicate
// registration, and the metrics were never served from that registry anyway
// since Handler reads the private one.
func New(logger *zap.Logger) (*Metrics, error) {
	registry := prometheus.NewRegistry()
	auto := promauto.With(registry)

	m := &Metrics{
		logger:   logger,
		registry: registry,
		operationsTotal: auto.NewCounterVec(
			prometheus.CounterOpts{
				Namespace: namespace,
				Subsystem: subsystem,
				Name:      "operations_total",
				Help:      "Total number of operations performed by result",
			},
			[]string{"operation", "result"},
		),
		operationDuration: auto.NewHistogramVec(
			prometheus.HistogramOpts{
				Namespace: namespace,
				Subsystem: subsystem,
				Name:      "operation_duration_seconds",
				Help:      "Operation duration in seconds",
				Buckets:   prometheus.DefBuckets,
			},
			[]string{"operation"},
		),
		resources: auto.NewGaugeVec(
			prometheus.GaugeOpts{
				Namespace: namespace,
				Subsystem: subsystem,
				Name:      "resources",
				Help:      "Number of DRBD resources by health state (healthy, degraded)",
			},
			[]string{"state"},
		),
		nodes: auto.NewGaugeVec(
			prometheus.GaugeOpts{
				Namespace: namespace,
				Subsystem: subsystem,
				Name:      "nodes",
				Help:      "Number of registered nodes by state (reachable, unreachable)",
			},
			[]string{"state"},
		),
		observedAt: auto.NewGaugeVec(
			prometheus.GaugeOpts{
				Namespace: namespace,
				Subsystem: subsystem,
				Name:      "last_observation_timestamp_seconds",
				Help:      "Unix time of the last successful observation of each data source",
			},
			[]string{"source"},
		),
		grpcRequestsTotal: auto.NewCounterVec(
			prometheus.CounterOpts{
				Namespace: namespace,
				Subsystem: subsystem,
				Name:      "grpc_requests_total",
				Help:      "Total number of gRPC requests by method and status",
			},
			[]string{"method", "status"},
		),
		grpcRequestDuration: auto.NewHistogramVec(
			prometheus.HistogramOpts{
				Namespace: namespace,
				Subsystem: subsystem,
				Name:      "grpc_request_duration_seconds",
				Help:      "gRPC request duration in seconds",
				Buckets:   prometheus.DefBuckets,
			},
			[]string{"method"},
		),
		up: auto.NewGauge(
			prometheus.GaugeOpts{
				Namespace: namespace,
				Subsystem: subsystem,
				Name:      "up",
				Help:      "Indicates the SDS controller instance is available (always 1)",
			},
		),
	}

	// The pool label is not unique on its own: every node in an SDS cluster
	// tends to carry a volume group of the same name, so without the node the
	// second pool's capacity silently overwrites the first's.
	m.storageCapacity = newSeriesSet(auto.NewGaugeVec(
		prometheus.GaugeOpts{
			Namespace: namespace,
			Subsystem: subsystem,
			Name:      "storage_capacity_bytes",
			Help:      "Storage capacity in bytes by pool, node and state (total, used, free)",
		},
		[]string{"pool", "node", "state"},
	))
	m.gateways = newSeriesSet(auto.NewGaugeVec(
		prometheus.GaugeOpts{
			Namespace: namespace,
			Subsystem: subsystem,
			Name:      "gateways",
			Help:      "Number of gateways by type and state",
		},
		[]string{"type", "state"},
	))
	m.drbdRole = newSeriesSet(auto.NewGaugeVec(
		prometheus.GaugeOpts{
			Namespace: namespace,
			Subsystem: drbdSubsystem,
			Name:      "role",
			Help:      "1 for the DRBD role a replica currently holds; other roles are not exported",
		},
		[]string{"resource", "node", "role"},
	))
	m.drbdDiskState = newSeriesSet(auto.NewGaugeVec(
		prometheus.GaugeOpts{
			Namespace: namespace,
			Subsystem: drbdSubsystem,
			Name:      "disk_state",
			Help:      "1 for the DRBD disk state a replica currently reports; other states are not exported",
		},
		[]string{"resource", "node", "state"},
	))
	m.drbdReplicationState = newSeriesSet(auto.NewGaugeVec(
		prometheus.GaugeOpts{
			Namespace: namespace,
			Subsystem: drbdSubsystem,
			Name:      "replication_state",
			Help:      "1 for the DRBD replication state a peer connection currently reports; other states are not exported",
		},
		[]string{"resource", "node", "state"},
	))
	m.drbdResync = newSeriesSet(auto.NewGaugeVec(
		prometheus.GaugeOpts{
			Namespace: namespace,
			Subsystem: drbdSubsystem,
			Name:      "resync_completed_ratio",
			Help:      "Resync completion of a replica as a fraction of 1; 1 means fully in sync",
		},
		[]string{"resource", "node"},
	))
	m.drbdQuorum = newSeriesSet(auto.NewGaugeVec(
		prometheus.GaugeOpts{
			Namespace: namespace,
			Subsystem: drbdSubsystem,
			Name:      "quorum",
			Help:      "1 when a replica holds DRBD quorum, 0 when it has lost it; absent when unreported",
		},
		[]string{"resource", "node"},
	))
	m.drbdResourceUp = newSeriesSet(auto.NewGaugeVec(
		prometheus.GaugeOpts{
			Namespace: namespace,
			Subsystem: drbdSubsystem,
			Name:      "resource_up",
			Help:      "1 when live DRBD status was read for a resource, 0 when no node answered for it",
		},
		[]string{"resource"},
	))

	m.registerHealth(auto)

	// Set up to 1
	m.up.Set(1)

	// Register Go and process collectors with the custom registry
	m.registry.MustRegister(collectors.NewGoCollector())
	m.registry.MustRegister(collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}))

	logger.Info("Prometheus metrics initialized")
	return m, nil
}

// Handler returns the HTTP handler for the /metrics endpoint
func (m *Metrics) Handler() http.Handler {
	return promhttp.HandlerFor(m.registry, promhttp.HandlerOpts{
		EnableOpenMetrics: true,
	})
}

// ==================== Recorders ====================

// RecordOperation records an operation with its result and duration.
func (m *Metrics) RecordOperation(operation, result string, duration float64) {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.operationsTotal.WithLabelValues(operation, result).Inc()
	m.operationDuration.WithLabelValues(operation).Observe(duration)
}

// RecordObservation records one attempt to read one cluster data source.
//
// The timestamp is only advanced on success. That is what lets a consumer tell
// "the cluster really has three reachable nodes" from "the last poll that could
// answer said three, forty minutes ago" — the gauges deliberately keep their
// last good value when a source fails, so the staleness has to be exported
// somewhere.
func (m *Metrics) RecordObservation(source string, ok bool, duration float64) {
	result := "error"
	if ok {
		result = "success"
	}
	m.RecordOperation("observe_"+source, result, duration)

	if !ok {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.observedAt.WithLabelValues(source).Set(float64(time.Now().Unix()))
}

// RecordResourceCount sets how many resources are in one health state. Callers
// write every state they know about on each successful poll, so an unwritten
// state is one that has never been observed rather than one that is empty.
func (m *Metrics) RecordResourceCount(state string, count float64) {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.resources.WithLabelValues(state).Set(count)
}

// RecordNodeState records the count of nodes in a specific state.
func (m *Metrics) RecordNodeState(state string, count float64) {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.nodes.WithLabelValues(state).Set(count)
}

// PoolCapacity is one storage pool's capacity, in bytes.
//
// TotalBytes is what the pool can hold and UsedBytes how much of it is
// allocated; free is derived rather than passed so the three can never
// disagree.
type PoolCapacity struct {
	Pool  string
	Node  string
	Total uint64
	Used  uint64
}

// SetPoolCapacities replaces the whole storage capacity view.
//
// This replaced a per-pool setter. A pool that is deleted, or that lives on a
// node that stopped answering, has no later call to zero it, so the per-pool
// form exported a removed pool's last known capacity forever — and it took no
// node, so two nodes' identically named volume groups overwrote each other.
// Callers must only invoke this when the pool listing actually succeeded;
// passing an empty slice deletes every pool series.
func (m *Metrics) SetPoolCapacities(pools []PoolCapacity) {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.storageCapacity.begin()
	for _, p := range pools {
		labels := func(state string) prometheus.Labels {
			return prometheus.Labels{"pool": p.Pool, "node": p.Node, "state": state}
		}
		m.storageCapacity.set(labels("total"), float64(p.Total))
		m.storageCapacity.set(labels("used"), float64(p.Used))
		// A pool whose total is unknown has no meaningful free figure; total
		// minus used would render an unknown capacity as a fully free one.
		if p.Total >= p.Used {
			m.storageCapacity.set(labels("free"), float64(p.Total-p.Used))
		}
	}
	m.storageCapacity.commit()
}

// GatewayCount is the number of gateways of one type in one state.
type GatewayCount struct {
	Type  string
	State string
	Count float64
}

// SetGatewayCounts replaces the whole gateway view, for the same reason
// SetPoolCapacities does: nothing else ever zeroes the state a gateway has left
// behind, so a gateway that moved from "started" to "stopped" would otherwise
// be counted in both forever.
func (m *Metrics) SetGatewayCounts(counts []GatewayCount) {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.gateways.begin()
	for _, c := range counts {
		m.gateways.set(prometheus.Labels{"type": c.Type, "state": c.State}, c.Count)
	}
	m.gateways.commit()
}

// RecordGRPCRequest records a gRPC request with method, status, and duration
func (m *Metrics) RecordGRPCRequest(method, status string, duration float64) {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.grpcRequestsTotal.WithLabelValues(method, status).Inc()
	m.grpcRequestDuration.WithLabelValues(method).Observe(duration)
}

// ResetMetrics resets all metrics to zero (useful for testing)
func (m *Metrics) ResetMetrics() {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.operationsTotal.Reset()
	m.operationDuration.Reset()
	m.resources.Reset()
	m.nodes.Reset()
	m.observedAt.Reset()
	m.grpcRequestsTotal.Reset()
	m.grpcRequestDuration.Reset()

	m.storageCapacity.reset()
	m.gateways.reset()
	for _, s := range m.replicaSeries() {
		s.reset()
	}
}

// GetRegistry returns the Prometheus registry
func (m *Metrics) GetRegistry() *prometheus.Registry {
	return m.registry
}
