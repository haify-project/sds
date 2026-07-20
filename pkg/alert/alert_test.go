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
