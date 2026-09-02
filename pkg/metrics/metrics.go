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
	"sort"
	"strings"
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

// ==================== Wholesale-replaced gauge vectors ====================

// seriesSet is a GaugeVec whose entire contents are replaced each collection
// cycle, remembering which label sets it currently exports so the ones that
// disappear can be deleted individually.
//
// GaugeVec.Reset() before repopulating would be one line instead of this, but
// it empties the vector for the whole rebuild. A scrape landing in that window
// sees no pools and no replicas at all — indistinguishable from a cluster that
// has none, which is the exact class of lie this package exists to avoid.
// Deleting only the vanished series never has such a window.
type seriesSet struct {
	vec *prometheus.GaugeVec
	// live maps a canonical label key to the labels themselves, for the series
	// currently exported.
	live map[string]prometheus.Labels
	// next accumulates the labels written during the cycle in progress; nil
	// outside a begin/commit pair.
	next map[string]prometheus.Labels
}

func newSeriesSet(vec *prometheus.GaugeVec) *seriesSet {
	return &seriesSet{vec: vec, live: map[string]prometheus.Labels{}}
}

// begin starts a replacement cycle.
func (s *seriesSet) begin() {
	s.next = make(map[string]prometheus.Labels, len(s.live))
}

// set writes one series and marks it as surviving the cycle in progress.
func (s *seriesSet) set(labels prometheus.Labels, value float64) {
	s.vec.With(labels).Set(value)
	if s.next != nil {
		s.next[labelKey(labels)] = labels
	}
}

// commit deletes every series that was exported before this cycle and was not
// written during it.
func (s *seriesSet) commit() {
	for key, labels := range s.live {
		if _, kept := s.next[key]; !kept {
			s.vec.Delete(labels)
		}
	}
	s.live = s.next
	s.next = nil
}

// reset drops every series, used only by ResetMetrics.
func (s *seriesSet) reset() {
	s.vec.Reset()
	s.live = map[string]prometheus.Labels{}
	s.next = nil
}

// labelKey renders a label set as a comparable string. Label names are sorted
// so the key does not depend on map iteration order.
func labelKey(labels prometheus.Labels) string {
	names := make([]string, 0, len(labels))
	for name := range labels {
		names = append(names, name)
	}
	sort.Strings(names)

	var b strings.Builder
	for _, name := range names {
		b.WriteString(name)
		b.WriteByte('=')
		b.WriteString(labels[name])
		b.WriteByte(0x1f) // unit separator: cannot appear in a label value
	}
	return b.String()
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

// ReplicaState is one replica's live DRBD state.
//
// The optional fields are pointers because their zero values are meaningful
// readings in their own right: 0% synced and "quorum lost" are both real
// states, and neither may be invented for a replica that simply did not report.
type ReplicaState struct {
	Resource string
	Node     string
	// Role, DiskState and ReplicationState are exported verbatim as label
	// values; an empty one is skipped rather than exported as "".
	Role             string
	DiskState        string
	ReplicationState string
	// SyncPercent is resync completion in 0..100, or nil when the status source
	// could not report it. Steady state is 100, not 0: drbdsetup omits the
	// completion field entirely once a resync has finished, and treating that
	// absence as zero paints a fully in-sync cluster as one whose resync never
	// started.
	SyncPercent *float64
	// Quorum is nil unless DRBD actually reported quorum for this replica,
	// which in practice is only the node that was queried — a node can only
	// speak for its own quorum, and inferring a peer's would be a guess.
	Quorum *bool
}

// ReplicationSnapshot is one complete reading of DRBD replication.
type ReplicationSnapshot struct {
	// Resources is every resource the controller knows about, whether or not
	// any of its replicas could be read. Each becomes an sds_drbd_resource_up
	// series, which is how "nothing answered for this resource" is expressed:
	// its replica series simply do not exist, and resource_up is 0.
	Resources []string
	// Replicas is every replica whose state was actually read. Replicas that
	// could not be reached are absent, not zeroed — a node that has fallen off
	// the network must not read as a node reporting perfect health, nor as one
	// reporting total failure.
	Replicas []ReplicaState
}

// SetReplication replaces the whole DRBD replication view.
//
// DRBD's role, disk state and replication state are enumerations, and there are
// two usual ways to export one. This exports only the value currently held, as
// a series carrying that value as a label with a constant 1 — so
// `sds_drbd_disk_state{state!="UpToDate"} == 1` finds every unhealthy replica,
// and `count by (state)` gives the cluster's distribution.
//
// The alternative — a 0/1 series for every value of the enum — allows
// `{state="UpToDate"} == 0` as well, but costs roughly 25 series per replica
// instead of 3, and requires this package to hold a copy of DRBD's enums. Those
// enums differ across DRBD versions, so the copy would go stale silently and
// start dropping states that the kernel reports and this code has never heard
// of. Exporting exactly what drbdsetup said cannot go stale.
//
// A state that stops being current has its series deleted rather than set to 0,
// which is the same rule as everywhere else here: absent means "not observed".
func (m *Metrics) SetReplication(snap ReplicationSnapshot) {
	m.mu.Lock()
	defer m.mu.Unlock()

	for _, s := range m.replicaSeries() {
		s.begin()
	}

	observed := make(map[string]bool, len(snap.Resources))
	for _, r := range snap.Replicas {
		observed[r.Resource] = true
		id := prometheus.Labels{"resource": r.Resource, "node": r.Node}

		if r.Role != "" {
			m.drbdRole.set(withLabel(id, "role", r.Role), 1)
		}
		if r.DiskState != "" {
			m.drbdDiskState.set(withLabel(id, "state", r.DiskState), 1)
		}
		if r.ReplicationState != "" {
			m.drbdReplicationState.set(withLabel(id, "state", r.ReplicationState), 1)
		}
		if r.SyncPercent != nil {
			m.drbdResync.set(id, clampRatio(*r.SyncPercent/100))
		}
		if r.Quorum != nil {
			quorum := 0.0
			if *r.Quorum {
				quorum = 1
			}
			m.drbdQuorum.set(id, quorum)
		}
	}

	for _, name := range snap.Resources {
		up := 0.0
		if observed[name] {
			up = 1
		}
		m.drbdResourceUp.set(prometheus.Labels{"resource": name}, up)
	}

	for _, s := range m.replicaSeries() {
		s.commit()
	}
}

func (m *Metrics) replicaSeries() []*seriesSet {
	return []*seriesSet{
		m.drbdRole, m.drbdDiskState, m.drbdReplicationState,
		m.drbdResync, m.drbdQuorum, m.drbdResourceUp,
	}
}

// withLabel copies a label set with one more label, so the shared
// resource/node identity is not mutated by the caller that adds a state.
func withLabel(base prometheus.Labels, name, value string) prometheus.Labels {
	out := make(prometheus.Labels, len(base)+1)
	for k, v := range base {
		out[k] = v
	}
	out[name] = value
	return out
}

// clampRatio keeps a completion figure inside 0..1. DRBD has been seen to
// report slightly over 100 on a finishing resync, and a ratio above 1 on a
// panel labelled "percent complete" reads as a bug in the cluster rather than
// in the reporting.
func clampRatio(v float64) float64 {
	switch {
	case v < 0:
		return 0
	case v > 1:
		return 1
	default:
		return v
	}
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
