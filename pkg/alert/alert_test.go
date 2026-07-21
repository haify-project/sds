package alert

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type mockLister struct {
	list []ResourceStatusInfo
}

func (m *mockLister) GetResourceStatusList(ctx context.Context) ([]ResourceStatusInfo, error) {
	return m.list, nil
}

func TestAlertMonitorFiresWebhook(t *testing.T) {
	var mu sync.Mutex
	var received []AlertPayload

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var p AlertPayload
		if err := json.NewDecoder(r.Body).Decode(&p); err == nil {
			mu.Lock()
			received = append(received, p)
			mu.Unlock()
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer ts.Close()

	lister := &mockLister{
		list: []ResourceStatusInfo{
			{
				Name: "res1",
				NodeStates: map[string]NodeStateInfo{
					"n1": {DiskState: "UpToDate", ReplicationState: "Established"},
					"n2": {DiskState: "Diskless", ReplicationState: "StandAlone"},
				},
			},
		},
	}

	mon := NewMonitor(ts.URL, 50*time.Millisecond, lister, nil)
	mon.poll(context.Background())

	mu.Lock()
	require.Len(t, received, 1)
	assert.Equal(t, "degraded", received[0].Event)
	assert.Equal(t, "res1", received[0].Resource)
	assert.Equal(t, "n2", received[0].Node)
	mu.Unlock()

	// Now recover n2
	lister.list[0].NodeStates["n2"] = NodeStateInfo{
		DiskState:        "UpToDate",
		ReplicationState: "Established",
	}

	mon.poll(context.Background())

	mu.Lock()
	require.Len(t, received, 2)
	assert.Equal(t, "resolved", received[1].Event)
	assert.Equal(t, "res1", received[1].Resource)
	assert.Equal(t, "n2", received[1].Node)
	mu.Unlock()
}

func TestAlertMonitorFiresOnWANDegraded(t *testing.T) {
	var mu sync.Mutex
	var received []AlertPayload

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var p AlertPayload
		if err := json.NewDecoder(r.Body).Decode(&p); err == nil {
			mu.Lock()
			received = append(received, p)
			mu.Unlock()
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer ts.Close()

	// All local replicas are healthy, but the WAN leg is down.
	lister := &mockLister{
		list: []ResourceStatusInfo{
			{
				Name: "res1",
				NodeStates: map[string]NodeStateInfo{
					"n1": {DiskState: "UpToDate", ReplicationState: "Established"},
				},
				WANEnabled: true,
				WANHealthy: false,
				WANMessage: "DR WAN endpoint unreachable",
			},
		},
	}

	mon := NewMonitor(ts.URL, 50*time.Millisecond, lister, nil)
	mon.poll(context.Background())

	mu.Lock()
	require.Len(t, received, 1)
	assert.Equal(t, "degraded", received[0].Event)
	assert.Equal(t, "res1", received[0].Resource)
	assert.Equal(t, "wan", received[0].Node)
	assert.Contains(t, received[0].Message, "WAN replication degraded")
	assert.Contains(t, received[0].Message, "unreachable")
	mu.Unlock()

	// A second poll while still degraded must NOT re-fire (edge-triggered).
	mon.poll(context.Background())
	mu.Lock()
	require.Len(t, received, 1, "degraded alert must fire only on the edge")
	mu.Unlock()

	// Recover the WAN link -> a single "resolved" alert.
	lister.list[0].WANHealthy = true
	lister.list[0].WANMessage = ""
	mon.poll(context.Background())

	mu.Lock()
	require.Len(t, received, 2)
	assert.Equal(t, "resolved", received[1].Event)
	assert.Equal(t, "wan", received[1].Node)
	mu.Unlock()
}

func TestAlertMonitorStartStop(t *testing.T) {
	lister := &mockLister{}
	mon := NewMonitor("http://localhost:9999", 10*time.Millisecond, lister, nil)
	mon.Start(context.Background())
	time.Sleep(30 * time.Millisecond)
	mon.Stop()
}

func TestAlertMonitorHTTPError(t *testing.T) {
	// Server returning 500 error
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer ts.Close()

	lister := &mockLister{
		list: []ResourceStatusInfo{
			{
				Name: "res1",
				NodeStates: map[string]NodeStateInfo{
					"n1": {DiskState: "Diskless", ReplicationState: "StandAlone"},
				},
			},
		},
	}

	mon := NewMonitor(ts.URL, 50*time.Millisecond, lister, nil)
	mon.poll(context.Background())
}

func TestIsDegradedStates(t *testing.T) {
	deg, reason := isDegraded(NodeStateInfo{DiskState: "Failed"})
	assert.True(t, deg)
	assert.Contains(t, reason, "Failed")

	deg, reason = isDegraded(NodeStateInfo{DiskState: "Detached"})
	assert.True(t, deg)
	assert.Contains(t, reason, "Detached")

	deg, reason = isDegraded(NodeStateInfo{DiskState: "UpToDate", ReplicationState: "Disconnecting"})
	assert.True(t, deg)
	assert.Contains(t, reason, "Disconnecting")

	deg, reason = isDegraded(NodeStateInfo{DiskState: "UpToDate", ReplicationState: "Unconnected"})
	assert.True(t, deg)
	assert.Contains(t, reason, "Unconnected")
}
