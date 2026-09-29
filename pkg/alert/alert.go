// Package alert watches cluster health and turns what it sees into events on
// an event.Bus.
//
// It only detects; it does not deliver. Webhooks and watch streams are
// subscribers on the bus, so adding a new way to be notified requires no change
// here, and every consumer sees the same event stream in the same order.
//
// Two kinds of condition are handled differently on purpose:
//
//   - Level conditions (a replica is degraded, a node is unreachable, a WAN
//     link is broken) are either true or false at every poll. These fire the
//     first time they are seen — including on the very first poll after a
//     controller restart, because a problem that predates the restart is still a
//     problem — and resolve when they clear.
//   - Edge conditions (the Primary moved) exist only as a transition between two
//     polls. These are suppressed on a resource's first observation: after a
//     restart the controller has no idea where the Primary used to be, and
//     announcing "failed over to node-a" every time it starts would train
//     operators to ignore the one alert that most needs attention.
package alert

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"go.uber.org/zap"

	"github.com/liliang-cn/sds/pkg/event"
)

// NodeStateInfo is one replica's DRBD state as the monitor needs it.
type NodeStateInfo struct {
	DiskState        string
	ReplicationState string
	// Role is "Primary", "Secondary", or "Unknown". Failover detection is built
	// on this.
	Role string
	// ExpectedDiskless marks a node that is supposed to have no local copy: a
	// quorum tiebreaker, or a diskless client mounting the volume over the DRBD
	// network. For these, "Diskless" is the healthy steady state, not a fault.
	//
	// Without this the monitor fires a warning for every tiebreaker — and
	// resource.auto_tiebreaker defaults to on, so every two-replica resource
	// has one — producing a permanent alert that can never clear. A wall of
	// un-clearable alerts is worse than no alerting: it trains people to
	// ignore the ones that matter.
	ExpectedDiskless bool
	// SyncPercent is resync completion for this replica in 0..100, or nil when
	// the status source carried no completion figure at all.
	//
	// Nil is not zero. Only the structured drbdsetup JSON reports completion;
	// the plain-text fallback parse has no such field, and charting its zero
	// value would paint a fully in-sync replica as one whose resync never
	// started. Nothing here alerts on it — it is carried for Observer.
	SyncPercent *float64
	// Quorum reports whether this replica currently holds DRBD quorum, or nil
	// when DRBD did not say. Nil is the normal case for peers: a node's status
	// only carries its own quorum, and a peer's would be a guess. Carried for
	// Observer; the quorum alerts are the operator's to write from the metric.
	Quorum *bool
	// Connection is the peer's DRBD connection state ("Connected",
	// "Connecting", "StandAlone", ...) as the node that answered the status
	// query sees it. Empty for that answering node itself, which has no
	// connection to describe, and for status sources that do not report one.
	//
	// A peer that is not Connected tells us nothing else about itself: DRBD
	// prints its name and its connection state and stops. Role, DiskState and
	// ReplicationState are all empty for such a peer, and reading those empties
	// as facts is what this field exists to prevent.
	Connection string
	// OutOfSyncKiB is how much of this replica DRBD knows differs from the node
	// whose status was read. On a connected, Established replica it is non-zero
	// only after an online verify found blocks that disagree.
	OutOfSyncKiB uint64
}

// connected reports whether this replica's link is usable, which is the
// precondition for believing anything else the view says about it.
//
// An empty Connection is the answering node itself (or a status source with no
// connection field) and counts as connected: it is the one node whose state was
// read locally rather than across a link.
func (st NodeStateInfo) connected() bool {
	return st.Connection == "" || strings.EqualFold(st.Connection, "Connected")
}

// ResourceStatusInfo is one resource's health across its replicas.
type ResourceStatusInfo struct {
	Name       string
	NodeStates map[string]NodeStateInfo
	// WAN replication health (only meaningful when WANEnabled). WANHealthy is
	// true when both sds-proxy instances are active and the DR WAN endpoint is
	// reachable; WANMessage describes the fault otherwise.
	WANEnabled bool
	WANHealthy bool
	WANMessage string
}

// Degraded reports whether any replica of the resource is in a faulty state.
//
// It is the same predicate the degrade alert fires on, exported rather than
// reimplemented so a "degraded resources" gauge and a resource.degraded alert
// can never disagree about the same instant — the disagreement an operator
// notices is the one between the alert that paged them and the page they open
// next.
func (r ResourceStatusInfo) Degraded() bool {
	for _, state := range r.NodeStates {
		if bad, _ := isDegraded(state); bad {
			return true
		}
	}
	return false
}

// ResourceLister is satisfied by *controller.ResourceManager or a fake in tests.
type ResourceLister interface {
	GetResourceStatusList(ctx context.Context) ([]ResourceStatusInfo, error)
}

// NodeStatusInfo is one registered node's reachability.
type NodeStatusInfo struct {
	Name      string
	Reachable bool
	Message   string
}

// NodeLister reports whether each registered node can still be reached. It is
// optional: a Monitor built without one simply never raises node events.
type NodeLister interface {
	GetNodeStatusList(ctx context.Context) ([]NodeStatusInfo, error)
}

// PoolStatusInfo is one storage pool's capacity as the monitor needs it.
//
// Only thin pool utilisation is here, and deliberately not the volume group's
// free space: SDS creates a thin pool with every free extent in its group, so
// vg_free is zero from the moment the pool exists and alerting on it would fire
// permanently for every pool in the cluster.
type PoolStatusInfo struct {
	Name string
	Node string
	// ThinPool is the thin pool LV inside the group, empty when the group holds
	// none. Empty means the percentages below carry no information — a thick
	// group is not a pool at 0% — and no capacity condition is evaluated.
	ThinPool    string
	DataPercent float64
	MetaPercent float64
	// OutOfSpace is LVM's own out-of-data-space flag, which is a fact rather
	// than a threshold and is alerted on regardless of the percentages.
	OutOfSpace bool
	// TotalBytes and FreeBytes are the volume group's own capacity, and
	// ThinSizeBytes the data capacity of the thin pool inside it — the figure
	// DataPercent is a percentage of.
	//
	// No condition here reads them, for the reason in the type comment: SDS
	// drives vg_free to zero on purpose, so alerting on it would fire forever.
	// They are carried for Observer, which charts capacity rather than judging
	// it, and for which a thick group's group-level figures are the only ones
	// that exist.
	TotalBytes    uint64
	FreeBytes     uint64
	ThinSizeBytes uint64
}

// PoolLister reports the capacity of every managed pool. It is optional: a
// Monitor built without one never raises pool events.
type PoolLister interface {
	GetPoolStatusList(ctx context.Context) ([]PoolStatusInfo, error)
}

// SourceStatus is how one of the monitor's data sources answered on one poll.
//
// The three states it distinguishes are the whole point of it. A source that is
// switched off, a source that failed, and a source that answered with nothing
// look identical once flattened to a slice of results, and a consumer that
// cannot tell them apart will publish "zero nodes are reachable" for a database
// error — which is precisely the reading an operator cannot distinguish from a
// dead cluster.
type SourceStatus struct {
	// Enabled is false when the source is not configured at all. Nothing about
	// it was attempted, so nothing about it should be reported either way.
	Enabled bool
	// OK is true when the listing succeeded. False with Enabled set means the
	// listing was attempted and failed; Items is then empty and means nothing.
	OK bool
	// Err is the listing failure, nil when OK.
	Err error
	// Duration is how long the listing took, successful or not.
	Duration time.Duration
}

// ResourceObservation is the resource listing of one poll.
type ResourceObservation struct {
	SourceStatus
	Items []ResourceStatusInfo
}

// NodeObservation is the node listing of one poll.
type NodeObservation struct {
	SourceStatus
	Items []NodeStatusInfo
}

// PoolObservation is the pool listing of one poll.
type PoolObservation struct {
	SourceStatus
	Items []PoolStatusInfo
}

// Observation is everything one poll cycle saw, exactly as the checks saw it.
type Observation struct {
	Resources ResourceObservation
	Nodes     NodeObservation
	Pools     PoolObservation
}

// Observer is handed each poll's Observation after the events for it have been
// published.
//
// The monitor's job is to turn cluster state into events, but the state it
// gathers is also exactly what a metrics exporter needs, and gathering it twice
// would double the SSH traffic and let the two views disagree about the same
// instant. So one poll is published to whoever asked for it.
//
// This is an interface rather than a direct call into a metrics package for the
// same reason the three listers above are interfaces: alert depends on
// abstractions in both directions, has no opinion about what a consumer does
// with a snapshot, and stays testable with nothing but fakes. The adapter that
// turns an Observation into Prometheus series lives on the controller side,
// alongside the adapters that satisfy the listers.
//
// Observed is called from the poll goroutine and must not block: a slow
// observer delays the next health check, which is the one thing the monitor
// cannot afford.
type Observer interface {
	Observed(Observation)
}

// source names which lister owns a condition, so a condition can be cleared
// when its subject vanishes without being cleared merely because a *different*
// lister failed.
//
// This used to be inferred from the shape of the event key — a condition with
// an empty Resource was a node condition, anything else a resource condition.
// Pool conditions have both a name and a node and fit neither, and guessing
// would have let a failed pool listing resolve every node alert, or a healthy
// node poll silently clear a pool alert whose source never answered.
type source int

const (
	sourceResources source = iota
	sourceNodes
	sourcePools
)

// Monitor polls cluster health and publishes state changes to a bus.
type Monitor struct {
	interval  time.Duration
	resources ResourceLister
	nodes     NodeLister
	pools     PoolLister
	nearFull  float64
	full      float64
	bus       *event.Bus
	observer  Observer
	log       *zap.Logger

	mu sync.Mutex
	// firing tracks which level conditions are currently raised, keyed by
	// event.Event.Key(), so each is reported once when it starts and once when
	// it clears rather than on every poll.
	firing map[string]bool
	// owners records which lister raised each firing condition. Kept alongside
	// firing rather than derived from the key, so resolveVanished can tell a
	// subject that disappeared from a source that failed to answer.
	owners map[string]source
	// primaries remembers each resource's Primary set between polls. A resource
	// absent from this map has not been observed yet, which is what suppresses
	// failover alerts on the first poll.
	primaries map[string]string
	// lastPrimary remembers the last node that actually held the Primary role,
	// and unlike primaries it is not cleared when the role goes away. It is what
	// makes "no Primary" expressible as a condition: the alert has to keep
	// naming the node that was demoted for as long as it stays raised, and
	// primaries is "" on every poll after the first one that noticed.
	//
	// Its presence is also the test for whether a resource is supposed to have a
	// Primary at all. Most resources sit Secondary on every node by design, so
	// treating that as an outage would raise a critical for each of them.
	lastPrimary map[string]string

	stop     chan struct{}
	stopOnce sync.Once

	// kick asks for a poll now, when something reports a DRBD state change;
	// see Kick. Buffered by one so a burst of changes collapses into a poll.
	kick chan struct{}
	// eventDriven is set while every node's DRBD state changes are being
	// received as they happen. Then a quiet, healthy cluster needs no
	// thirty-second polling to be noticed breaking, and it is polled on
	// idleInterval instead; see nextInterval.
	eventDriven  atomic.Bool
	idleInterval time.Duration
	// steady is whether the last poll found every resource healthy. A resync
	// or a degraded replica emits no event as it progresses, so the interval
	// stays short until it is over.
	steady atomic.Bool

	pollsMu sync.Mutex
	polls   int
}

// Options configures a Monitor.
type Options struct {
	// Interval between polls. Zero uses 30s.
	Interval time.Duration
	// IdleInterval is the interval while the monitor is event-driven and the
	// last poll found nothing wrong. Below Interval uses DefaultIdleInterval.
	IdleInterval time.Duration
	// Resources is required.
	Resources ResourceLister
	// Nodes is optional; nil disables node reachability alerts.
	Nodes NodeLister
	// Pools is optional; nil disables thin pool capacity alerts.
	Pools PoolLister
	// NearFullPercent and FullPercent are the thin pool utilisation thresholds.
	// Zero or out-of-range values fall back to the defaults, and a NearFull at
	// or above Full is corrected rather than rejected — a misconfigured
	// threshold must not silently disable the alerting it configures.
	NearFullPercent float64
	FullPercent     float64
	// Observer is optional; nil means each poll's snapshot is used for events
	// only.
	Observer Observer
	// Logger is optional.
	Logger *zap.Logger
}

// Default thin pool utilisation thresholds, in percent.
const (
	DefaultNearFullPercent = 85.0
	DefaultFullPercent     = 95.0
)

// DefaultIdleInterval is how often a healthy cluster is polled while its DRBD
// state changes arrive as events. It still bounds how late a condition no
// event reports — a thin pool filling up — is noticed.
const DefaultIdleInterval = 5 * time.Minute

// kickSettle is how long a kick waits for the rest of a burst: one failover
// is a dozen state changes across several nodes within a second or two.
var kickSettle = 2 * time.Second

// NewMonitor creates a monitor publishing to bus.
func NewMonitor(bus *event.Bus, opts Options) *Monitor {
	if opts.Interval <= 0 {
		opts.Interval = 30 * time.Second
	}
	if opts.IdleInterval < opts.Interval {
		opts.IdleInterval = max(opts.Interval, DefaultIdleInterval)
	}
	if opts.Logger == nil {
		opts.Logger = zap.NewNop()
	}
	near, full := normalizeThresholds(opts.NearFullPercent, opts.FullPercent)
	return &Monitor{
		interval:     opts.Interval,
		resources:    opts.Resources,
		nodes:        opts.Nodes,
		pools:        opts.Pools,
		nearFull:     near,
		full:         full,
		bus:          bus,
		observer:     opts.Observer,
		log:          opts.Logger,
		firing:       make(map[string]bool),
		owners:       make(map[string]source),
		primaries:    make(map[string]string),
		lastPrimary:  make(map[string]string),
		stop:         make(chan struct{}),
		kick:         make(chan struct{}, 1),
		idleInterval: opts.IdleInterval,
	}
}

// normalizeThresholds keeps the two thin pool thresholds usable whatever the
// configuration says.
//
// A percentage outside (0, 100] is meaningless and falls back to the default. A
// near-full at or above full would make the warning unreachable — every pool
// crossing it would already be critical — so it is pulled below rather than
// rejected: a bad threshold should degrade the alert, not remove it.
func normalizeThresholds(near, full float64) (float64, float64) {
	if near <= 0 || near > 100 {
		near = DefaultNearFullPercent
	}
	if full <= 0 || full > 100 {
		full = DefaultFullPercent
	}
	if near >= full {
		near = full * DefaultNearFullPercent / DefaultFullPercent
	}
	return near, full
}

// Start begins background polling until ctx is cancelled or Stop is called.
//
// The first poll runs immediately rather than after one interval: a controller
// starting up into an already-degraded cluster should say so now, not in thirty
// seconds.
func (m *Monitor) Start(ctx context.Context) {
	go func() {
		m.Poll(ctx)
		timer := time.NewTimer(m.nextInterval())
		defer timer.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-m.stop:
				return
			case <-timer.C:
			case <-m.kick:
				select {
				case <-ctx.Done():
					return
				case <-m.stop:
					return
				case <-time.After(kickSettle):
				}
				select { // the kicks that came in while settling are this poll's
				case <-m.kick:
				default:
				}
			}
			m.Poll(ctx)
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
			timer.Reset(m.nextInterval())
		}
	}()
}

// Kick asks for a poll soon, because something changed. Never blocks.
func (m *Monitor) Kick() {
	select {
	case m.kick <- struct{}{}:
	default:
	}
}

// SetEventDriven says whether state changes are reaching Kick from every
// node. While they are, a healthy cluster is polled on the idle interval.
func (m *Monitor) SetEventDriven(on bool) {
	if m.eventDriven.Swap(on) != on && !on {
		m.Kick() // a node's changes are no longer arriving: look now
	}
}

func (m *Monitor) nextInterval() time.Duration {
	if m.eventDriven.Load() && m.steady.Load() {
		return m.idleInterval
	}
	return m.interval
}

// Stop halts background polling. Safe to call more than once, and safe on a
// monitor that was never started.
func (m *Monitor) Stop() {
	m.stopOnce.Do(func() { close(m.stop) })
}

// Polls reports how many poll cycles have completed.
func (m *Monitor) Polls() int {
	m.pollsMu.Lock()
	defer m.pollsMu.Unlock()
	return m.polls
}

// Poll runs one health check cycle. Exported so a caller can force a check and
// so tests need not wait on a ticker.
func (m *Monitor) Poll(ctx context.Context) {
	// scope records every condition this cycle actually evaluated, so a firing
	// alert whose subject has since vanished can be cleared. See resolveVanished.
	sc := &pollScope{seen: map[string]bool{}, live: map[string]bool{}, livePools: map[string]bool{}}
	var obs Observation
	m.checkResources(ctx, sc, &obs)
	m.checkNodes(ctx, sc, &obs)
	m.checkPools(ctx, sc, &obs)
	m.resolveVanished(sc)
	m.steady.Store(steadyState(obs))

	// After the events, not before: an observer that panics or blocks must not
	// be able to stop a degradation from being reported.
	if m.observer != nil {
		m.observer.Observed(obs)
	}

	m.pollsMu.Lock()
	m.polls++
	m.pollsMu.Unlock()
}

// pollScope is what one poll observed: the condition keys it evaluated, the
// resources it saw, and whether each source answered at all.
type pollScope struct {
	seen map[string]bool
	live map[string]bool
	// livePools holds the pools seen this poll, keyed "<pool>@<node>", which is
	// what makes a deleted pool distinguishable from a pool on a node that
	// dropped out of the listing.
	livePools map[string]bool
	// resourcesOK / nodesOK / poolsOK are false when that source could not be
	// listed. A source that failed reports nothing, which must never be
	// mistaken for "every condition it owns has cleared".
	resourcesOK bool
	nodesOK     bool
	poolsOK     bool
}

func (s *pollScope) mark(key string) {
	if s != nil {
		s.seen[key] = true
	}
}

func (m *Monitor) checkResources(ctx context.Context, sc *pollScope, obs *Observation) {
	if m.resources == nil {
		return
	}
	obs.Resources.Enabled = true

	start := time.Now()
	resources, err := m.resources.GetResourceStatusList(ctx)
	obs.Resources.Duration = time.Since(start)
	if err != nil {
		obs.Resources.Err = err
		m.log.Warn("alert monitor: list resources failed", zap.Error(err))
		return
	}
	obs.Resources.OK = true
	obs.Resources.Items = resources
	sc.resourcesOK = true

	for _, res := range resources {
		sc.live[res.Name] = true
		m.checkReplicas(res, sc)
		m.checkPrimary(res, sc)
		m.checkWAN(res, sc)
		m.checkOutOfSync(res, sc)
	}

	m.mu.Lock()
	for name := range m.primaries {
		if !sc.live[name] {
			delete(m.primaries, name)
			delete(m.lastPrimary, name)
		}
	}
	m.mu.Unlock()
}

// checkReplicas raises one condition per replica whose disk or replication
// state has left the healthy set.
func (m *Monitor) checkReplicas(res ResourceStatusInfo, sc *pollScope) {
	for node, state := range res.NodeStates {
		degraded, reason := isDegraded(state)
		m.level(event.Event{
			Type:     event.TypeResourceDegraded,
			Severity: event.SeverityWarning,
			Resource: res.Name,
			Node:     node,
			Details: map[string]string{
				"disk_state": state.DiskState,
				"repl_state": state.ReplicationState,
			},
		}, sc, sourceResources, degraded,
			fmt.Sprintf("resource %s on %s degraded: %s", res.Name, node, reason),
			fmt.Sprintf("resource %s on %s recovered to normal state", res.Name, node))
	}
}

// checkOutOfSync raises one condition per replica that is connected and
// replicating yet holds data that differs from its peer. While a replica is
// disconnected or resyncing the same counter only measures how far it has to
// catch up, which checkReplicas already reports.
func (m *Monitor) checkOutOfSync(res ResourceStatusInfo, sc *pollScope) {
	for node, state := range res.NodeStates {
		active := state.connected() && state.ReplicationState == "Established" && state.OutOfSyncKiB > 0
		m.level(event.Event{
			Type:     event.TypeResourceOutOfSync,
			Severity: event.SeverityWarning,
			Resource: res.Name,
			Node:     node,
			Details:  map[string]string{"out_of_sync_kib": fmt.Sprint(state.OutOfSyncKiB)},
		}, sc, sourceResources, active,
			fmt.Sprintf("resource %s: %d KiB on %s differ from its peer; resource verify %s shows which copies disagree", res.Name, state.OutOfSyncKiB, node, res.Name),
			fmt.Sprintf("resource %s on %s holds the same data as its peer again", res.Name, node))
	}
}

// checkPrimary detects the Primary role moving, disappearing, or appearing.
func (m *Monitor) checkPrimary(res ResourceStatusInfo, sc *pollScope) {
	cur := primarySet(res)

	m.mu.Lock()
	prev, known := m.primaries[res.Name]
	m.primaries[res.Name] = cur
	if cur != "" {
		m.lastPrimary[res.Name] = cur
	}
	demoted, expectPrimary := m.lastPrimary[res.Name]
	m.mu.Unlock()

	// Losing the Primary is a LEVEL condition and has to resolve under its own
	// type. It used to be published directly as a one-shot firing event, with a
	// separate resource.promoted event standing in for the clear — but a
	// receiver pairs firing with resolved by type, so nothing ever closed the
	// critical. It stayed outstanding for the life of the process, and survived
	// even deleting the resource, since resolveVanished can only clear
	// conditions that went through here and were recorded in m.firing.
	//
	// The node is deliberately not part of the key: it would change from the
	// demoted node to "" on the very next poll and split one condition into two.
	noPrimary := event.Event{
		Type:     event.TypeResourceNoPrimary,
		Severity: event.SeverityCritical,
		Resource: res.Name,
		Details:  map[string]string{"from": demoted},
	}

	m.mu.Lock()
	recovering := m.firing[noPrimary.Key()]
	m.mu.Unlock()

	// "Nobody reports Primary" only means there is no Primary when every replica
	// was actually asked. The controller reads a resource from whichever node
	// answers first, and that node describes its unreachable peers as one line
	// — "<peer> connection:Connecting" — carrying no role. So a node rebooting
	// back into the cluster produces, for the few seconds its links take to
	// establish, a view in which no node anywhere is Primary.
	//
	// That is measured, not hypothetical: on 2026-09-21 a node rejoining raised
	// this CRITICAL against a resource whose Primary had been serving
	// uninterrupted for two days — same second as the node.unreachable clear,
	// with no role change in the Primary's kernel log and no service restart.
	// A false CRITICAL on "the control plane lost its Primary" is worse than a
	// late true one: it is the alert people stop believing.
	partialView := !fullyConnected(res)

	m.level(noPrimary, sc, sourceResources, expectPrimary && cur == "" && !partialView,
		fmt.Sprintf("resource %s has no Primary: %s was demoted and nothing took over", res.Name, demoted),
		fmt.Sprintf("resource %s has a Primary again on %s", res.Name, cur))

	// First sighting: record where the Primary is without claiming it just moved
	// there. See the package comment.
	if !known || prev == cur {
		return
	}

	switch {
	case cur == "":
		// Reported by the level condition above.
	case prev == "":
		// A resource taking a Primary for the first time is worth saying. The
		// same transition arriving as the end of an outage is not: the
		// no_primary condition just resolved and named the same node, and two
		// events for one change is how a feed stops being read.
		if recovering {
			return
		}
		m.publish(event.Event{
			Type:     event.TypeResourcePromoted,
			Severity: event.SeverityInfo,
			Status:   event.StatusResolved,
			Resource: res.Name,
			Node:     cur,
			Message:  fmt.Sprintf("resource %s promoted on %s", res.Name, cur),
			Details:  map[string]string{"to": cur},
		})
	default:
		// One-shot, not a level condition: a failover has already happened and
		// there is no later state in which it "clears". Marking it firing would
		// leave it outstanding forever in any receiver that pairs firing with
		// resolved — the wall of un-clearable alerts that makes people stop
		// reading them. no_primary is different and does resolve, via promoted.
		m.publish(event.Event{
			Type:     event.TypeResourceFailover,
			Severity: event.SeverityWarning,
			Status:   event.StatusInfo,
			Resource: res.Name,
			Node:     cur,
			Message:  fmt.Sprintf("resource %s failed over: Primary moved from %s to %s", res.Name, prev, cur),
			Details:  map[string]string{"from": prev, "to": cur},
		})
	}
}

// checkWAN tracks the cross-site link as its own condition, keyed to the
// resource rather than a node, so a broken link is surfaced even when every
// local replica looks healthy.
func (m *Monitor) checkWAN(res ResourceStatusInfo, sc *pollScope) {
	if !res.WANEnabled {
		return
	}
	m.level(event.Event{
		Type:     event.TypeWANDegraded,
		Severity: event.SeverityCritical,
		Resource: res.Name,
		Node:     "wan",
	}, sc, sourceResources, !res.WANHealthy,
		fmt.Sprintf("resource %s WAN replication degraded: %s", res.Name, res.WANMessage),
		fmt.Sprintf("resource %s WAN replication recovered", res.Name))
}

func (m *Monitor) checkNodes(ctx context.Context, sc *pollScope, obs *Observation) {
	if m.nodes == nil {
		return
	}
	obs.Nodes.Enabled = true

	start := time.Now()
	nodes, err := m.nodes.GetNodeStatusList(ctx)
	obs.Nodes.Duration = time.Since(start)
	if err != nil {
		obs.Nodes.Err = err
		m.log.Warn("alert monitor: list nodes failed", zap.Error(err))
		return
	}
	obs.Nodes.OK = true
	obs.Nodes.Items = nodes
	sc.nodesOK = true

	for _, n := range nodes {
		msg := n.Message
		if msg == "" {
			msg = "no response"
		}
		m.level(event.Event{
			Type:     event.TypeNodeUnreachable,
			Severity: event.SeverityCritical,
			Node:     n.Name,
		}, sc, sourceNodes, !n.Reachable,
			fmt.Sprintf("node %s is unreachable: %s", n.Name, msg),
			fmt.Sprintf("node %s is reachable again", n.Name))
	}
}

// checkPools raises capacity conditions for every thin pool in the cluster.
//
// This is the one health signal that no other check can stand in for. A thin
// pool that runs out of data space stops accepting writes; the kernel then
// detaches the backing device, and DRBD reports Diskless on a node configured
// diskful — which surfaces as a resource.degraded alert naming a *replica*
// problem for what is really a *capacity* problem, and only once the damage is
// done. Watching the pool is what makes it preventable.
func (m *Monitor) checkPools(ctx context.Context, sc *pollScope, obs *Observation) {
	if m.pools == nil {
		return
	}
	obs.Pools.Enabled = true

	start := time.Now()
	pools, err := m.pools.GetPoolStatusList(ctx)
	obs.Pools.Duration = time.Since(start)
	if err != nil {
		obs.Pools.Err = err
		m.log.Warn("alert monitor: list pools failed", zap.Error(err))
		return
	}
	obs.Pools.OK = true
	obs.Pools.Items = pools
	sc.poolsOK = true

	for _, p := range pools {
		// Recorded before the thin check, not after: a pool converted back to
		// thick still exists, and saying it "no longer exists" when clearing its
		// old capacity alert would send an operator looking for a deletion that
		// never happened.
		sc.livePools[poolKey(p.Name, p.Node)] = true

		// A group with no thin pool has no utilisation to judge. Its vg_free is
		// a real number, but it is also the number SDS drives to zero on
		// purpose, so there is nothing here to alert on.
		if p.ThinPool == "" {
			continue
		}
		m.checkPoolDimension(p, sc, "data", p.DataPercent,
			event.TypePoolDataNearFull, event.TypePoolDataFull)
		m.checkPoolDimension(p, sc, "metadata", p.MetaPercent,
			event.TypePoolMetadataNearFull, event.TypePoolMetadataFull)

		m.level(event.Event{
			Type:     event.TypePoolOutOfSpace,
			Severity: event.SeverityCritical,
			Resource: p.Name,
			Node:     p.Node,
			Details: map[string]string{
				"thin_pool":        p.ThinPool,
				"data_percent":     formatPercent(p.DataPercent),
				"metadata_percent": formatPercent(p.MetaPercent),
			},
		}, sc, sourcePools, p.OutOfSpace,
			fmt.Sprintf("pool %s on %s is out of data space: writes are failing and any DRBD replica on it will drop to Diskless", p.Name, p.Node),
			fmt.Sprintf("pool %s on %s is no longer out of data space", p.Name, p.Node))
	}
}

// checkPoolDimension raises the near-full and full conditions for one
// utilisation dimension of one pool.
//
// The two are mutually exclusive by construction: near-full is only active
// below the critical threshold, so crossing it resolves the warning in the same
// poll that raises the critical. Reporting both at once would double every
// notification at the moment it matters most.
func (m *Monitor) checkPoolDimension(p PoolStatusInfo, sc *pollScope, dimension string, percent float64, nearType, fullType event.Type) {
	details := func() map[string]string {
		return map[string]string{
			"thin_pool": p.ThinPool,
			"dimension": dimension,
			"percent":   formatPercent(percent),
			"threshold": formatPercent(m.nearFull) + "/" + formatPercent(m.full),
		}
	}

	m.level(event.Event{
		Type:     fullType,
		Severity: event.SeverityCritical,
		Resource: p.Name,
		Node:     p.Node,
		Details:  details(),
	}, sc, sourcePools, percent >= m.full,
		fmt.Sprintf("pool %s on %s is %s%% %s used: extend it or free space now — a full DRBD resync of the volumes it holds reallocates every block and may not fit",
			p.Name, p.Node, formatPercent(percent), dimension),
		fmt.Sprintf("pool %s on %s %s usage is back under %s%%", p.Name, p.Node, dimension, formatPercent(m.full)))

	m.level(event.Event{
		Type:     nearType,
		Severity: event.SeverityWarning,
		Resource: p.Name,
		Node:     p.Node,
		Details:  details(),
	}, sc, sourcePools, percent >= m.nearFull && percent < m.full,
		fmt.Sprintf("pool %s on %s is %s%% %s used: plan an extension", p.Name, p.Node, formatPercent(percent), dimension),
		fmt.Sprintf("pool %s on %s %s usage is back under %s%%", p.Name, p.Node, dimension, formatPercent(m.nearFull)))
}

// formatPercent renders a utilisation figure the way LVM reports it, to two
// decimal places, so an event repeats what an operator will see in `lvs`.
func formatPercent(v float64) string {
	return strconv.FormatFloat(v, 'f', 2, 64)
}

// poolKey identifies a pool by name and node. A pool name is only unique within
// a node — every node in this cluster has an "sds_sdspool" — so the node is
// part of the identity, not a label on it.
func poolKey(name, node string) string { return name + "@" + node }

// resolveVanished clears alerts whose subject no longer exists.
//
// A level condition only resolves when something evaluates it and finds it
// false. If the thing it describes disappears, nothing evaluates it again and
// the alert stays raised forever. Three ways that happens, all reachable from
// normal operations:
//
//   - the resource is deleted;
//   - a replica is removed from a resource that still exists (remove-replica) —
//     and a replica being degraded is a common reason to remove it, so the
//     stuck alert lands on exactly the workflow that provokes it;
//   - a node is unregistered while unreachable.
//
// Keying this off "the resource is gone" only covered the first. Keying it off
// "nothing evaluated this condition" covers all three, and needs no separate
// knowledge of what removal looks like.
//
// The safety condition is that silence must come from a source that actually
// answered. A failed list produces no keys at all, which would otherwise read
// as every condition it owns having cleared — resolving every alert in the
// cluster on a transient database error.
func (m *Monitor) resolveVanished(sc *pollScope) {
	type vanished struct{ key, message string }

	m.mu.Lock()
	var stale []vanished
	for key, on := range m.firing {
		if !on || sc.seen[key] {
			continue
		}
		// Key layout is "<type>|<resource>|<node>".
		parts := strings.Split(key, "|")
		if len(parts) != 3 {
			continue
		}
		resource, node := parts[1], parts[2]

		// Only prune from a source that reported. Which source owns a condition
		// is recorded when it starts firing rather than guessed from the key:
		// pool conditions carry both a name and a node, so the old "empty
		// resource means node" rule would have misattributed every one of them.
		var msg string
		switch m.owners[key] {
		case sourceNodes:
			if !sc.nodesOK {
				continue
			}
			msg = fmt.Sprintf("node %s is no longer registered; clearing its outstanding alert", node)
		case sourcePools:
			if !sc.poolsOK {
				continue
			}
			if sc.livePools[poolKey(resource, node)] {
				msg = fmt.Sprintf("pool %s on %s no longer holds a thin pool; clearing its outstanding alert", resource, node)
			} else {
				msg = fmt.Sprintf("pool %s on %s no longer exists; clearing its outstanding alert", resource, node)
			}
		default:
			if !sc.resourcesOK {
				continue
			}
			switch {
			case !sc.live[resource]:
				msg = fmt.Sprintf("resource %s no longer exists; clearing its outstanding alert", resource)
			default:
				msg = fmt.Sprintf("%s is no longer part of resource %s; clearing its outstanding alert", node, resource)
			}
		}
		stale = append(stale, vanished{key: key, message: msg})
	}
	for _, v := range stale {
		delete(m.firing, v.key)
		delete(m.owners, v.key)
	}
	m.mu.Unlock()

	sort.Slice(stale, func(i, j int) bool { return stale[i].key < stale[j].key })
	for _, v := range stale {
		parts := strings.Split(v.key, "|")
		m.publish(event.Event{
			Type:     event.Type(parts[0]),
			Severity: event.SeverityInfo,
			Status:   event.StatusResolved,
			Resource: parts[1],
			Node:     parts[2],
			Message:  v.message,
		})
	}
}

// level reports a condition that is either true or false at each poll, emitting
// exactly one firing event when it starts and one resolved event when it ends.
// The severity on tmpl applies to the firing event; a recovery is informational.
//
// src names the lister that owns the condition, and is recorded for as long as
// it fires so that resolveVanished can tell "the subject is gone" from "the
// source that would have reported it did not answer".
func (m *Monitor) level(tmpl event.Event, sc *pollScope, src source, active bool, firingMsg, resolvedMsg string) {
	key := tmpl.Key()
	sc.mark(key)

	m.mu.Lock()
	was := m.firing[key]
	switch {
	case active && !was:
		m.firing[key] = true
		m.owners[key] = src
	case !active && was:
		delete(m.firing, key)
		delete(m.owners, key)
	default:
		m.mu.Unlock()
		return
	}
	m.mu.Unlock()

	if active {
		tmpl.Status = event.StatusFiring
		tmpl.Message = firingMsg
	} else {
		tmpl.Status = event.StatusResolved
		tmpl.Severity = event.SeverityInfo
		tmpl.Message = resolvedMsg
	}
	m.publish(tmpl)
}

func (m *Monitor) publish(e event.Event) {
	if m.bus == nil {
		return
	}
	published := m.bus.Publish(e)
	m.log.Info("alert",
		zap.Uint64("id", published.ID),
		zap.String("type", string(published.Type)),
		zap.String("severity", string(published.Severity)),
		zap.String("status", string(published.Status)),
		zap.String("message", published.Message))
}

// fullyConnected reports whether every replica in this view was actually
// reachable, which is what makes an absence in the view evidence of anything.
func fullyConnected(res ResourceStatusInfo) bool {
	for _, st := range res.NodeStates {
		if !st.connected() {
			return false
		}
	}
	return true
}

// primarySet renders the nodes currently holding the Primary role as a stable,
// comparable string. Sorting matters: map iteration order would otherwise make
// a dual-primary resource look like it flapped between every pair of polls.
func primarySet(res ResourceStatusInfo) string {
	var primaries []string
	for node, st := range res.NodeStates {
		if strings.EqualFold(st.Role, "Primary") {
			primaries = append(primaries, node)
		}
	}
	sort.Strings(primaries)
	return strings.Join(primaries, ",")
}

// isDegraded reports whether a replica's DRBD state indicates a fault.
func isDegraded(st NodeStateInfo) (bool, string) {
	// A replica whose link is down is a replica that is not protecting
	// anything, and the connection state is the only thing DRBD says about it —
	// there is no disk state to fall through to. StandAlone in particular is
	// not a transient: DRBD sets it deliberately, most often after refusing to
	// resolve a split brain, and it stays until someone intervenes.
	//
	// This is measured. A replica of the openclaw resource sat StandAlone from
	// 2026-09-20 02:54 after a split-brain disconnect, and nothing fired for a
	// day: the peer carried no entry in the view at all, and a replica that is
	// absent is a replica nobody checks.
	if !st.connected() {
		if strings.EqualFold(st.Connection, "StandAlone") {
			return true, "connection is StandAlone (DRBD disconnected it; a split brain leaves it here until resolved)"
		}
		return true, fmt.Sprintf("connection is %s", st.Connection)
	}
	switch st.DiskState {
	case "Diskless":
		// Expected for tiebreakers and diskless clients; a fault anywhere else,
		// where it means the node lost its local copy.
		if !st.ExpectedDiskless {
			return true, "disk state is Diskless"
		}
	case "Failed", "Detached":
		// Not exempted by ExpectedDiskless: a node with no disk by design still
		// should not be reporting a failed one.
		return true, fmt.Sprintf("disk state is %s", st.DiskState)
	case "Inconsistent":
		// Connected is not recovered. A replica catching up after an outage
		// holds a partial copy until the resync ends, and a second failure in
		// that window leaves one copy of the data. Reporting it recovered the
		// moment the link came back told an operator the redundancy was there
		// while it was still being rebuilt.
		return true, "disk is Inconsistent (resyncing; not yet a complete copy)"
	case "Outdated":
		return true, "disk is Outdated (holds data older than its peers)"
	}
	switch st.ReplicationState {
	case "StandAlone", "Disconnecting", "Unconnected":
		return true, fmt.Sprintf("replication state is %s", st.ReplicationState)
	}
	return false, ""
}

// steadyState is whether a poll found nothing that changes without DRBD saying
// so. A resync or a verify advances silently — events2 reports the start and
// the end, not the progress — so while one runs the monitor keeps polling.
// A replica stuck degraded is not in that set: an Outdated disk or a peer
// that stays away changes only by a state change, which is an event, and
// counting it would keep a cluster with one long-standing fault on the
// short interval for good.
func steadyState(obs Observation) bool {
	if !obs.Resources.OK {
		return false
	}
	for _, r := range obs.Resources.Items {
		for _, st := range r.NodeStates {
			if st.SyncPercent != nil && *st.SyncPercent < 100 {
				return false
			}
			switch {
			case strings.HasPrefix(st.ReplicationState, "Sync"),
				strings.HasPrefix(st.ReplicationState, "PausedSync"),
				strings.HasPrefix(st.ReplicationState, "Verify"):
				return false
			}
		}
	}
	return true
}
