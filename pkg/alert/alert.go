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
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"go.uber.org/zap"

	"github.com/liliang-cn/sds/pkg/event"
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

	hold time.Duration
	now  func() time.Time
	mu   sync.Mutex
	// firing tracks which level conditions are currently raised, keyed by
	// event.Event.Key(), so each is reported once when it starts and once when
	// it clears rather than on every poll.
	firing map[string]bool
	// owners records which lister raised each firing condition. Kept alongside
	// firing rather than derived from the key, so resolveVanished can tell a
	// subject that disappeared from a source that failed to answer.
	// pending holds when each warning condition was first seen, while it
	// waits out the hold.
	// raised is what each firing condition is, for the metrics.
	raised  map[string]FiringCondition
	pending map[string]time.Time
	owners  map[string]source
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
	// WarningHold is how long a warning condition must last before it is
	// raised; zero raises it on the first poll that sees it. See alert_hold.go.
	WarningHold time.Duration
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
		hold:         opts.WarningHold,
		pending:      make(map[string]time.Time),
		raised:       make(map[string]FiringCondition),
		now:          time.Now,
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
	if m.eventDriven.Load() && m.steady.Load() && !m.holding() {
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
	m.dropUnseenPending(sc)
	obs.Firing = m.firingConditions()
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
		delete(m.raised, v.key)
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
	if !active {
		delete(m.pending, key)
	}
	if active && !was && !m.heldLongEnough(key, tmpl.Severity) {
		m.mu.Unlock()
		return
	}
	switch {
	case active && !was:
		m.firing[key] = true
		m.owners[key] = src
		m.raised[key] = FiringCondition{Type: string(tmpl.Type), Severity: string(tmpl.Severity)}
	case !active && was:
		delete(m.firing, key)
		delete(m.owners, key)
		delete(m.raised, key)
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
