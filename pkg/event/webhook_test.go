package event

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// recorder is a Webhook receiver that captures decoded bodies.
type recorder struct {
	mu       sync.Mutex
	got      []Event
	headers  []http.Header
	attempts atomic.Int32
	status   atomic.Int32 // response code to return; 0 means 200
}

func (r *recorder) serve(w http.ResponseWriter, req *http.Request) {
	r.attempts.Add(1)
	if code := r.status.Load(); code != 0 {
		w.WriteHeader(int(code))
		return
	}
	var e Event
	if err := json.NewDecoder(req.Body).Decode(&e); err == nil {
		r.mu.Lock()
		r.got = append(r.got, e)
		r.headers = append(r.headers, req.Header.Clone())
		r.mu.Unlock()
	}
	w.WriteHeader(http.StatusOK)
}

func (r *recorder) events() []Event {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]Event(nil), r.got...)
}

func TestWebhookDeliversMatchingEvents(t *testing.T) {
	rec := &recorder{}
	ts := httptest.NewServer(http.HandlerFunc(rec.serve))
	defer ts.Close()

	bus := NewBus(10)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	NewWebhook(WebhookConfig{
		URL:     ts.URL,
		Headers: map[string]string{"X-Token": "s3cret"},
	}, nil).Start(ctx, bus)

	bus.Publish(Event{
		Type:     TypeResourceFailover,
		Severity: SeverityWarning,
		Status:   StatusFiring,
		Resource: "res1",
		Node:     "n2",
		Message:  "resource res1 failed over: Primary moved from n1 to n2",
		Details:  map[string]string{"from": "n1", "to": "n2"},
	})

	require.Eventually(t, func() bool { return len(rec.events()) == 1 }, 3*time.Second, 10*time.Millisecond)

	got := rec.events()[0]
	assert.Equal(t, TypeResourceFailover, got.Type)
	assert.Equal(t, StatusFiring, got.Status)
	assert.Equal(t, "n1", got.Details["from"])
	assert.Equal(t, uint64(1), got.ID)

	rec.mu.Lock()
	assert.Equal(t, "s3cret", rec.headers[0].Get("X-Token"))
	assert.Equal(t, "application/json", rec.headers[0].Get("Content-Type"))
	rec.mu.Unlock()
}

// A pager configured for critical must not be woken by a warning.
func TestWebhookHonoursSeverityFilter(t *testing.T) {
	rec := &recorder{}
	ts := httptest.NewServer(http.HandlerFunc(rec.serve))
	defer ts.Close()

	bus := NewBus(10)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	NewWebhook(WebhookConfig{
		URL:    ts.URL,
		Filter: Filter{MinSeverity: SeverityCritical},
	}, nil).Start(ctx, bus)

	bus.Publish(Event{Type: TypeResourceDegraded, Severity: SeverityWarning})
	bus.Publish(Event{Type: TypeNodeUnreachable, Severity: SeverityCritical})

	require.Eventually(t, func() bool { return len(rec.events()) == 1 }, 3*time.Second, 10*time.Millisecond)
	assert.Equal(t, TypeNodeUnreachable, rec.events()[0].Type)
}

func TestWebhookRetriesThenGivesUp(t *testing.T) {
	rec := &recorder{}
	rec.status.Store(http.StatusInternalServerError)
	ts := httptest.NewServer(http.HandlerFunc(rec.serve))
	defer ts.Close()

	bus := NewBus(10)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	wh := NewWebhook(WebhookConfig{URL: ts.URL, Retries: 2}, nil)
	wh.Start(ctx, bus)
	bus.Publish(Event{Type: TypeNodeUnreachable, Severity: SeverityCritical})

	// Three attempts total: the initial one plus two retries.
	require.Eventually(t, func() bool { return rec.attempts.Load() == 3 }, 5*time.Second, 10*time.Millisecond)

	// And then it stops, rather than retrying forever.
	time.Sleep(300 * time.Millisecond)
	assert.Equal(t, int32(3), rec.attempts.Load())

	cancel()
	wh.Wait()
}

// OnResult hears the final outcome once per event: the last error after every
// retry failed, nil once one got through.
func TestWebhookReportsFinalOutcome(t *testing.T) {
	rec := &recorder{}
	rec.status.Store(http.StatusNotFound)
	ts := httptest.NewServer(http.HandlerFunc(rec.serve))
	defer ts.Close()

	var mu sync.Mutex
	results := map[uint64]error{}
	calls := 0
	bus := NewBus(10)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	wh := NewWebhook(WebhookConfig{URL: ts.URL, Retries: 1, OnResult: func(e Event, err error) {
		mu.Lock()
		defer mu.Unlock()
		results[e.ID] = err
		calls++
	}}, nil)
	wh.Start(ctx, bus)

	failed := bus.Publish(Event{Type: TypeResourceNoPrimary, Severity: SeverityCritical})
	require.Eventually(t, func() bool { mu.Lock(); defer mu.Unlock(); return calls == 1 }, 5*time.Second, 10*time.Millisecond)
	rec.status.Store(0)
	ok := bus.Publish(Event{Type: TypeResourcePromoted})
	require.Eventually(t, func() bool { mu.Lock(); defer mu.Unlock(); return calls == 2 }, 5*time.Second, 10*time.Millisecond)

	mu.Lock()
	defer mu.Unlock()
	assert.ErrorContains(t, results[failed.ID], "HTTP 404")
	assert.NoError(t, results[ok.ID])
	assert.Equal(t, int32(3), rec.attempts.Load(), "two attempts for the failure, one for the success")
}

func TestWebhookSucceedsOnRetry(t *testing.T) {
	rec := &recorder{}
	rec.status.Store(http.StatusBadGateway)
	ts := httptest.NewServer(http.HandlerFunc(rec.serve))
	defer ts.Close()

	bus := NewBus(10)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	NewWebhook(WebhookConfig{URL: ts.URL}, nil).Start(ctx, bus)
	bus.Publish(Event{Type: TypeNodeUnreachable, Severity: SeverityCritical})

	require.Eventually(t, func() bool { return rec.attempts.Load() >= 1 }, 3*time.Second, 10*time.Millisecond)
	rec.status.Store(0) // receiver comes back

	require.Eventually(t, func() bool { return len(rec.events()) == 1 }, 5*time.Second, 10*time.Millisecond)
}

// One unreachable receiver must not stop a healthy one from being notified.
func TestUnreachableWebhookDoesNotBlockOthers(t *testing.T) {
	rec := &recorder{}
	ts := httptest.NewServer(http.HandlerFunc(rec.serve))
	defer ts.Close()

	bus := NewBus(10)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Port 0 on the loopback address never accepts, so this one always fails.
	NewWebhook(WebhookConfig{URL: "http://127.0.0.1:0/hook", Retries: 5}, nil).Start(ctx, bus)
	NewWebhook(WebhookConfig{URL: ts.URL}, nil).Start(ctx, bus)

	bus.Publish(Event{Type: TypeNodeUnreachable, Severity: SeverityCritical})
	require.Eventually(t, func() bool { return len(rec.events()) == 1 }, 3*time.Second, 10*time.Millisecond)
}

func TestWebhookStopsWithContext(t *testing.T) {
	bus := NewBus(10)
	ctx, cancel := context.WithCancel(context.Background())

	wh := NewWebhook(WebhookConfig{URL: "http://127.0.0.1:0/hook"}, nil)
	wh.Start(ctx, bus)
	cancel()

	done := make(chan struct{})
	go func() { wh.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("webhook did not stop when its context was cancelled")
	}

	// Its subscription must be released too, or a cancelled receiver leaks a
	// slot on the bus for the life of the process.
	_, _, subs := bus.Stats()
	assert.Zero(t, subs)
}

func TestWebhookConfigDefaults(t *testing.T) {
	wh := NewWebhook(WebhookConfig{URL: "http://example.invalid"}, nil)
	assert.Equal(t, 10*time.Second, wh.cfg.Timeout)
	assert.Equal(t, 2, wh.cfg.Retries)
	assert.NotNil(t, wh.log, "a nil logger must be replaced, not dereferenced")
}
