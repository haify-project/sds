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

// Monitor polls cluster health and publishes state changes to a bus.
type Monitor struct {
	interval  time.Duration
	resources ResourceLister
	nodes     NodeLister
	bus       *event.Bus
	log       *zap.Logger

	mu sync.Mutex
	// firing tracks which level conditions are currently raised, keyed by
	// event.Event.Key(), so each is reported once when it starts and once when
	// it clears rather than on every poll.
	firing map[string]bool
	// primaries remembers each resource's Primary set between polls. A resource
	// absent from this map has not been observed yet, which is what suppresses
	// failover alerts on the first poll.
	primaries map[string]string

	stop     chan struct{}
	stopOnce sync.Once

	pollsMu sync.Mutex
	polls   int
}

// Options configures a Monitor.
type Options struct {
	// Interval between polls. Zero uses 30s.
	Interval time.Duration
	// Resources is required.
	Resources ResourceLister
	// Nodes is optional; nil disables node reachability alerts.
	Nodes NodeLister
	// Logger is optional.
	Logger *zap.Logger
}

// NewMonitor creates a monitor publishing to bus.
func NewMonitor(bus *event.Bus, opts Options) *Monitor {
	if opts.Interval <= 0 {
		opts.Interval = 30 * time.Second
	}
	if opts.Logger == nil {
		opts.Logger = zap.NewNop()
	}
	return &Monitor{
		interval:  opts.Interval,
		resources: opts.Resources,
		nodes:     opts.Nodes,
		bus:       bus,
		log:       opts.Logger,
		firing:    make(map[string]bool),
		primaries: make(map[string]string),
		stop:      make(chan struct{}),
	}
}

// Start begins background polling until ctx is cancelled or Stop is called.
//
// The first poll runs immediately rather than after one interval: a controller
// starting up into an already-degraded cluster should say so now, not in thirty
// seconds.
func (m *Monitor) Start(ctx context.Context) {
	go func() {
		ticker := time.NewTicker(m.interval)
		defer ticker.Stop()
		m.Poll(ctx)
		for {
			select {
			case <-ctx.Done():
				return
			case <-m.stop:
				return
			case <-ticker.C:
				m.Poll(ctx)
			}
		}
	}()
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
	sc := &pollScope{seen: map[string]bool{}, live: map[string]bool{}}
	m.checkResources(ctx, sc)
	m.checkNodes(ctx, sc)
	m.resolveVanished(sc)

	m.pollsMu.Lock()
	m.polls++
	m.pollsMu.Unlock()
}

// pollScope is what one poll observed: the condition keys it evaluated, the
// resources it saw, and whether each source answered at all.
type pollScope struct {
	seen map[string]bool
	live map[string]bool
	// resourcesOK / nodesOK are false when that source could not be listed. A
	// source that failed reports nothing, which must never be mistaken for
	// "every condition it owns has cleared".
	resourcesOK bool
	nodesOK     bool
}

func (s *pollScope) mark(key string) {
	if s != nil {
		s.seen[key] = true
	}
}

func (m *Monitor) checkResources(ctx context.Context, sc *pollScope) {
	if m.resources == nil {
		return
	}
	resources, err := m.resources.GetResourceStatusList(ctx)
	if err != nil {
		m.log.Warn("alert monitor: list resources failed", zap.Error(err))
		return
	}
	sc.resourcesOK = true

	for _, res := range resources {
		sc.live[res.Name] = true
		m.checkReplicas(res, sc)
		m.checkPrimary(res)
		m.checkWAN(res, sc)
	}

	m.mu.Lock()
	for name := range m.primaries {
		if !sc.live[name] {
			delete(m.primaries, name)
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
		}, sc, degraded,
			fmt.Sprintf("resource %s on %s degraded: %s", res.Name, node, reason),
			fmt.Sprintf("resource %s on %s recovered to normal state", res.Name, node))
	}
}

// checkPrimary detects the Primary role moving, disappearing, or appearing.
func (m *Monitor) checkPrimary(res ResourceStatusInfo) {
	cur := primarySet(res)

	m.mu.Lock()
	prev, known := m.primaries[res.Name]
	m.primaries[res.Name] = cur
	m.mu.Unlock()

	// First sighting: record where the Primary is without claiming it just moved
	// there. See the package comment.
	if !known || prev == cur {
		return
	}

	switch {
	case cur == "":
		m.publish(event.Event{
			Type:     event.TypeResourceNoPrimary,
			Severity: event.SeverityCritical,
			Status:   event.StatusFiring,
			Resource: res.Name,
			Node:     prev,
			Message:  fmt.Sprintf("resource %s has no Primary: %s was demoted and nothing took over", res.Name, prev),
			Details:  map[string]string{"from": prev},
		})
	case prev == "":
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
	}, sc, !res.WANHealthy,
		fmt.Sprintf("resource %s WAN replication degraded: %s", res.Name, res.WANMessage),
		fmt.Sprintf("resource %s WAN replication recovered", res.Name))
}

func (m *Monitor) checkNodes(ctx context.Context, sc *pollScope) {
	if m.nodes == nil {
		return
	}
	nodes, err := m.nodes.GetNodeStatusList(ctx)
	if err != nil {
		m.log.Warn("alert monitor: list nodes failed", zap.Error(err))
		return
	}
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
		}, sc, !n.Reachable,
			fmt.Sprintf("node %s is unreachable: %s", n.Name, msg),
			fmt.Sprintf("node %s is reachable again", n.Name))
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

		// Resource-scoped conditions belong to the resource lister, node-scoped
		// ones to the node lister. Only prune from a source that reported.
		if resource != "" && !sc.resourcesOK {
			continue
		}
		if resource == "" && !sc.nodesOK {
			continue
		}

		var msg string
		switch {
		case resource == "":
			msg = fmt.Sprintf("node %s is no longer registered; clearing its outstanding alert", node)
		case !sc.live[resource]:
			msg = fmt.Sprintf("resource %s no longer exists; clearing its outstanding alert", resource)
		default:
			msg = fmt.Sprintf("%s is no longer part of resource %s; clearing its outstanding alert", node, resource)
		}
		stale = append(stale, vanished{key: key, message: msg})
	}
	for _, v := range stale {
		delete(m.firing, v.key)
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
func (m *Monitor) level(tmpl event.Event, sc *pollScope, active bool, firingMsg, resolvedMsg string) {
	key := tmpl.Key()
	sc.mark(key)

	m.mu.Lock()
	was := m.firing[key]
	switch {
	case active && !was:
		m.firing[key] = true
	case !active && was:
		delete(m.firing, key)
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
	}
	switch st.ReplicationState {
	case "StandAlone", "Disconnecting", "Unconnected":
		return true, fmt.Sprintf("replication state is %s", st.ReplicationState)
	}
	return false, ""
}
