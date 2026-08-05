// Package alert provides background health monitoring and Webhook notification
// for DRBD degrade/fault states across managed resources.
package alert

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"

	"net/http"
	"sync"
	"time"

	"go.uber.org/zap"
)

// ResourceLister is satisfied by *controller.ResourceManager or a mock in tests.
type NodeStateInfo struct {
	DiskState        string
	ReplicationState string
}

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

type ResourceLister interface {
	GetResourceStatusList(ctx context.Context) ([]ResourceStatusInfo, error)
}

// AlertPayload is the JSON structure posted to the WebhookURL.
type AlertPayload struct {
	Event     string    `json:"event"` // "degraded" or "resolved"
	Resource  string    `json:"resource"`
	Node      string    `json:"node,omitempty"`
	DiskState string    `json:"disk_state,omitempty"`
	ReplState string    `json:"repl_state,omitempty"`
	Message   string    `json:"message"`
	Timestamp time.Time `json:"timestamp"`
}

// Monitor periodically polls managed DRBD resources and fires Webhook
// alerts when a volume drops to a degraded or stand-alone state.
type Monitor struct {
	url      string
	interval time.Duration
	lister   ResourceLister
	log      *zap.Logger

	mu     sync.Mutex
	firing map[string]bool // key: "resource/node"
	stop   chan struct{}
	client *http.Client
}

// NewMonitor creates an AlertMonitor. Pass interval = 0 to use 30s default.
func NewMonitor(url string, interval time.Duration, lister ResourceLister, log *zap.Logger) *Monitor {
	if interval <= 0 {
		interval = 30 * time.Second
	}
	if log == nil {
		log = zap.NewNop()
	}
	return &Monitor{
		url:      url,
		interval: interval,
		lister:   lister,
		log:      log,
		firing:   make(map[string]bool),
		stop:     make(chan struct{}),
		client:   &http.Client{Timeout: 10 * time.Second},
	}
}

// Start begins background polling until context cancellation or Stop().
func (m *Monitor) Start(ctx context.Context) {
	ticker := time.NewTicker(m.interval)
	go func() {
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-m.stop:
				return
			case <-ticker.C:
				m.poll(ctx)
			}
		}
	}()
}

// Stop halts background polling.
func (m *Monitor) Stop() {
	close(m.stop)
}

func (m *Monitor) poll(ctx context.Context) {
	if m.url == "" {
		return
	}
	resources, err := m.lister.GetResourceStatusList(ctx)
	if err != nil {
		m.log.Warn("alert monitor: list resources failed", zap.Error(err))
		return
	}

	for _, res := range resources {
		for node, state := range res.NodeStates {
			key := res.Name + "/" + node
			degraded, reason := isDegraded(state)

			m.mu.Lock()
			wasFiring := m.firing[key]

			if degraded && !wasFiring {
				m.firing[key] = true
				m.mu.Unlock()
				m.sendAlert(AlertPayload{
					Event:     "degraded",
					Resource:  res.Name,
					Node:      node,
					DiskState: state.DiskState,
					ReplState: state.ReplicationState,
					Message:   fmt.Sprintf("resource %s on %s degraded: %s", res.Name, node, reason),
					Timestamp: time.Now(),
				})
			} else if !degraded && wasFiring {
				delete(m.firing, key)
				m.mu.Unlock()
				m.sendAlert(AlertPayload{
					Event:     "resolved",
					Resource:  res.Name,
					Node:      node,
					DiskState: state.DiskState,
					ReplState: state.ReplicationState,
					Message:   fmt.Sprintf("resource %s on %s recovered to normal state", res.Name, node),
					Timestamp: time.Now(),
				})
			} else {
				m.mu.Unlock()
			}
		}

		// WAN replication health is tracked as a per-resource alert (keyed
		// "<resource>/wan") separate from the per-node DRBD states, so a broken
		// cross-site link is surfaced even when every local replica looks fine.
		if res.WANEnabled {
			key := res.Name + "/wan"
			m.mu.Lock()
			wasFiring := m.firing[key]
			if !res.WANHealthy && !wasFiring {
				m.firing[key] = true
				m.mu.Unlock()
				m.sendAlert(AlertPayload{
					Event:     "degraded",
					Resource:  res.Name,
					Node:      "wan",
					Message:   fmt.Sprintf("resource %s WAN replication degraded: %s", res.Name, res.WANMessage),
					Timestamp: time.Now(),
				})
			} else if res.WANHealthy && wasFiring {
				delete(m.firing, key)
				m.mu.Unlock()
				m.sendAlert(AlertPayload{
					Event:     "resolved",
					Resource:  res.Name,
					Node:      "wan",
					Message:   fmt.Sprintf("resource %s WAN replication recovered", res.Name),
					Timestamp: time.Now(),
				})
			} else {
				m.mu.Unlock()
			}
		}
	}
}

// isDegraded checks if a node's DRBD disk or replication state indicates a fault.
func isDegraded(st NodeStateInfo) (bool, string) {
	disk := st.DiskState
	repl := st.ReplicationState

	if disk == "Diskless" || disk == "Failed" || disk == "Detached" {
		return true, fmt.Sprintf("disk state is %s", disk)
	}
	if repl == "StandAlone" || repl == "Disconnecting" || repl == "Unconnected" {
		return true, fmt.Sprintf("replication state is %s", repl)
	}
	return false, ""
}

func (m *Monitor) sendAlert(p AlertPayload) {
	data, err := json.Marshal(p)
	if err != nil {
		m.log.Error("alert: marshal payload error", zap.Error(err))
		return
	}
	req, err := http.NewRequest("POST", m.url, bytes.NewReader(data))
	if err != nil {
		m.log.Error("alert: create request error", zap.Error(err))
		return
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := m.client.Do(req)
	if err != nil {
		m.log.Warn("alert: webhook send error", zap.String("url", m.url), zap.Error(err))
		return
	}
	_ = resp.Body.Close()
	if resp.StatusCode >= 400 {
		m.log.Warn("alert: webhook HTTP error", zap.Int("code", resp.StatusCode))
	} else {
		m.log.Info("alert: webhook delivered", zap.String("event", p.Event), zap.String("resource", p.Resource))
	}
}
