package controller

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/grpc-ecosystem/grpc-gateway/v2/runtime"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"

	pb "github.com/liliang-cn/sds/api/proto/v1"
	"github.com/liliang-cn/sds/pkg/config"
	"github.com/liliang-cn/sds/pkg/event"
)

func eventTestController(bus *event.Bus) *Controller {
	ctx, cancel := context.WithCancel(context.Background())
	return &Controller{
		logger: zap.NewNop(),
		config: &config.Config{},
		events: bus,
		ctx:    ctx,
		cancel: cancel,
	}
}

func degradeEvent(resource, node string) event.Event {
	return event.Event{
		Type:     event.TypeResourceDegraded,
		Severity: event.SeverityWarning,
		Status:   event.StatusFiring,
		Resource: resource,
		Node:     node,
		Message:  "degraded",
		Details:  map[string]string{"disk_state": "Diskless"},
	}
}

func TestListEventsReturnsHistory(t *testing.T) {
	bus := event.NewBus(10)
	bus.Publish(degradeEvent("res1", "n1"))
	bus.Publish(event.Event{Type: event.TypeResourceFailover, Severity: event.SeverityWarning, Resource: "res2"})

	srv := NewServer(eventTestController(bus))
	resp, err := srv.ListEvents(context.Background(), &pb.ListEventsRequest{})
	require.NoError(t, err)
	require.True(t, resp.Success)
	require.Len(t, resp.Events, 2)

	assert.Equal(t, uint64(1), resp.Events[0].Id, "oldest first")
	assert.Equal(t, string(event.TypeResourceDegraded), resp.Events[0].Type)
	assert.Equal(t, "Diskless", resp.Events[0].Details["disk_state"])
	assert.NotZero(t, resp.Events[0].TimestampUnixMs)
	assert.Equal(t, uint64(2), resp.Published)
}

func TestListEventsFilters(t *testing.T) {
	bus := event.NewBus(10)
	bus.Publish(degradeEvent("res1", "n1"))
	bus.Publish(event.Event{Type: event.TypeNodeUnreachable, Severity: event.SeverityCritical, Node: "n9"})
	srv := NewServer(eventTestController(bus))
	ctx := context.Background()

	resp, err := srv.ListEvents(ctx, &pb.ListEventsRequest{MinSeverity: "critical"})
	require.NoError(t, err)
	require.Len(t, resp.Events, 1)
	assert.Equal(t, string(event.TypeNodeUnreachable), resp.Events[0].Type)

	resp, err = srv.ListEvents(ctx, &pb.ListEventsRequest{Types: []string{string(event.TypeResourceDegraded)}})
	require.NoError(t, err)
	require.Len(t, resp.Events, 1)
	assert.Equal(t, "res1", resp.Events[0].Resource)

	resp, err = srv.ListEvents(ctx, &pb.ListEventsRequest{Resource: "res1"})
	require.NoError(t, err)
	require.Len(t, resp.Events, 1)

	resp, err = srv.ListEvents(ctx, &pb.ListEventsRequest{SinceId: 1})
	require.NoError(t, err)
	require.Len(t, resp.Events, 1)
	assert.Equal(t, uint64(2), resp.Events[0].Id)
}

// With notifications off the API has to say so, not return an empty list that
// reads as "nothing has gone wrong".
func TestListEventsReportsDisabled(t *testing.T) {
	srv := NewServer(eventTestController(nil))
	resp, err := srv.ListEvents(context.Background(), &pb.ListEventsRequest{})
	require.NoError(t, err)
	assert.False(t, resp.Success)
	assert.Contains(t, resp.Message, "disabled")
	assert.Empty(t, resp.Events)
}

func TestWatchEventsDisabledIsFailedPrecondition(t *testing.T) {
	srv := NewServer(eventTestController(nil))
	err := srv.WatchEvents(&pb.WatchEventsRequest{}, &fakeWatchStream{ctx: context.Background()})
	assert.Equal(t, codes.FailedPrecondition, status.Code(err))
}

// fakeWatchStream captures what WatchEvents sends.
type fakeWatchStream struct {
	grpc.ServerStream
	ctx context.Context

	mu   sync.Mutex
	sent []*pb.Event
	err  error
}

func (f *fakeWatchStream) Context() context.Context { return f.ctx }

func (f *fakeWatchStream) Send(e *pb.Event) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return f.err
	}
	f.sent = append(f.sent, e)
	return nil
}

func (f *fakeWatchStream) SendMsg(any) error           { return nil }
func (f *fakeWatchStream) RecvMsg(any) error           { return nil }
func (f *fakeWatchStream) SetHeader(metadata.MD) error { return nil }
func (f *fakeWatchStream) SendHeader(metadata.MD) error {
	return nil
}
func (f *fakeWatchStream) SetTrailer(metadata.MD) {}

func (f *fakeWatchStream) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.sent)
}

func (f *fakeWatchStream) at(i int) *pb.Event {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.sent[i]
}

func TestWatchEventsReplaysThenStreamsLive(t *testing.T) {
	bus := event.NewBus(10)
	bus.Publish(degradeEvent("res1", "n1"))

	srv := NewServer(eventTestController(bus))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	stream := &fakeWatchStream{ctx: ctx}

	done := make(chan error, 1)
	go func() { done <- srv.WatchEvents(&pb.WatchEventsRequest{}, stream) }()

	// The retained event arrives first.
	require.Eventually(t, func() bool { return stream.count() == 1 }, 3*time.Second, 5*time.Millisecond)
	assert.Equal(t, uint64(1), stream.at(0).Id)

	bus.Publish(event.Event{Type: event.TypeResourceFailover, Severity: event.SeverityWarning, Resource: "res1"})
	require.Eventually(t, func() bool { return stream.count() == 2 }, 3*time.Second, 5*time.Millisecond)
	assert.Equal(t, uint64(2), stream.at(1).Id)
	assert.Equal(t, string(event.TypeResourceFailover), stream.at(1).Type)

	cancel()
	select {
	case err := <-done:
		assert.NoError(t, err)
	case <-time.After(3 * time.Second):
		t.Fatal("WatchEvents did not return after the client disconnected")
	}
}

// An event published between the history replay and the live subscription must
// not fall through the gap, and one present in both must not be sent twice.
func TestWatchEventsDoesNotDuplicateAcrossReplay(t *testing.T) {
	bus := event.NewBus(10)
	for range 3 {
		bus.Publish(degradeEvent("res1", "n1"))
	}

	srv := NewServer(eventTestController(bus))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	stream := &fakeWatchStream{ctx: ctx}
	go func() { _ = srv.WatchEvents(&pb.WatchEventsRequest{}, stream) }()

	require.Eventually(t, func() bool { return stream.count() == 3 }, 3*time.Second, 5*time.Millisecond)

	bus.Publish(degradeEvent("res1", "n1"))
	require.Eventually(t, func() bool { return stream.count() == 4 }, 3*time.Second, 5*time.Millisecond)

	seen := map[uint64]bool{}
	for i := range 4 {
		id := stream.at(i).Id
		assert.False(t, seen[id], "event %d delivered twice", id)
		seen[id] = true
	}
}

func TestWatchEventsHonoursSinceID(t *testing.T) {
	bus := event.NewBus(10)
	for range 3 {
		bus.Publish(degradeEvent("res1", "n1"))
	}

	srv := NewServer(eventTestController(bus))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	stream := &fakeWatchStream{ctx: ctx}
	go func() { _ = srv.WatchEvents(&pb.WatchEventsRequest{SinceId: 2}, stream) }()

	require.Eventually(t, func() bool { return stream.count() == 1 }, 3*time.Second, 5*time.Millisecond)
	assert.Equal(t, uint64(3), stream.at(0).Id)
}

// The controller shutting down has to end the stream; otherwise Stop blocks on
// every connected watcher.
func TestWatchEventsStopsWithController(t *testing.T) {
	bus := event.NewBus(10)
	ctrl := eventTestController(bus)
	srv := NewServer(ctrl)

	stream := &fakeWatchStream{ctx: context.Background()}
	done := make(chan error, 1)
	go func() { done <- srv.WatchEvents(&pb.WatchEventsRequest{}, stream) }()

	time.Sleep(20 * time.Millisecond)
	ctrl.cancel()

	select {
	case err := <-done:
		assert.NoError(t, err)
	case <-time.After(3 * time.Second):
		t.Fatal("WatchEvents outlived the controller")
	}
}

// ==================== SSE ====================

func sseServer(t *testing.T, ctrl *Controller) *httptest.Server {
	t.Helper()
	mux := runtime.NewServeMux()
	ctrl.registerEventRoutes(mux, nil)
	ts := httptest.NewServer(mux)
	t.Cleanup(ts.Close)
	return ts
}

// readSSEFrames reads count "data:" payloads or fails.
func readSSEFrames(t *testing.T, body *bufio.Reader, count int) []event.Event {
	t.Helper()
	var out []event.Event
	for len(out) < count {
		line, err := body.ReadString('\n')
		require.NoError(t, err)
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		var e event.Event
		require.NoError(t, json.Unmarshal([]byte(strings.TrimPrefix(strings.TrimSpace(line), "data: ")), &e))
		out = append(out, e)
	}
	return out
}

func TestEventStreamSSE(t *testing.T) {
	bus := event.NewBus(10)
	bus.Publish(degradeEvent("res1", "n1"))
	ctrl := eventTestController(bus)
	defer ctrl.cancel()
	ts := sseServer(t, ctrl)

	req, err := http.NewRequest("GET", ts.URL+"/v1/events/stream", nil)
	require.NoError(t, err)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	resp, err := http.DefaultClient.Do(req.WithContext(ctx))
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()

	require.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Equal(t, "text/event-stream", resp.Header.Get("Content-Type"))
	// Without this, nginx buffers the whole stream and the UI shows nothing.
	assert.Equal(t, "no", resp.Header.Get("X-Accel-Buffering"))

	reader := bufio.NewReader(resp.Body)
	got := readSSEFrames(t, reader, 1)
	assert.Equal(t, uint64(1), got[0].ID)

	bus.Publish(event.Event{Type: event.TypeResourceFailover, Severity: event.SeverityWarning, Resource: "res1"})
	got = readSSEFrames(t, reader, 1)
	assert.Equal(t, event.TypeResourceFailover, got[0].Type)
}

// EventSource replays Last-Event-ID on reconnect; honouring it is what makes
// browser reconnection lossless instead of a source of duplicates.
func TestEventStreamHonoursLastEventID(t *testing.T) {
	bus := event.NewBus(10)
	for range 3 {
		bus.Publish(degradeEvent("res1", "n1"))
	}
	ctrl := eventTestController(bus)
	defer ctrl.cancel()
	ts := sseServer(t, ctrl)

	req, err := http.NewRequest("GET", ts.URL+"/v1/events/stream", nil)
	require.NoError(t, err)
	req.Header.Set("Last-Event-ID", "2")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	resp, err := http.DefaultClient.Do(req.WithContext(ctx))
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()

	got := readSSEFrames(t, bufio.NewReader(resp.Body), 1)
	assert.Equal(t, uint64(3), got[0].ID, "events the browser already saw must not be replayed")
}

func TestEventStreamFiltersByQuery(t *testing.T) {
	bus := event.NewBus(10)
	bus.Publish(degradeEvent("res1", "n1"))
	bus.Publish(event.Event{Type: event.TypeNodeUnreachable, Severity: event.SeverityCritical, Node: "n9"})
	ctrl := eventTestController(bus)
	defer ctrl.cancel()
	ts := sseServer(t, ctrl)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, "GET", ts.URL+"/v1/events/stream?min_severity=critical", nil)
	require.NoError(t, err)

	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()

	got := readSSEFrames(t, bufio.NewReader(resp.Body), 1)
	assert.Equal(t, event.TypeNodeUnreachable, got[0].Type)
}

func TestEventStreamDisabled(t *testing.T) {
	ctrl := eventTestController(nil)
	defer ctrl.cancel()
	ts := sseServer(t, ctrl)

	resp, err := http.Get(ts.URL + "/v1/events/stream")
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	assert.Equal(t, http.StatusServiceUnavailable, resp.StatusCode)
}

// The SSE route bypasses the gRPC interceptors, so it has to enforce the static
// token itself — otherwise enabling [auth] leaves this one endpoint wide open.
func TestEventStreamRequiresToken(t *testing.T) {
	bus := event.NewBus(10)
	ctrl := eventTestController(bus)
	defer ctrl.cancel()
	ctrl.config = &config.Config{Auth: config.AuthConfig{Enabled: true, Token: "0123456789abcdef"}}
	ts := sseServer(t, ctrl)

	resp, err := http.Get(ts.URL + "/v1/events/stream")
	require.NoError(t, err)
	_ = resp.Body.Close()
	assert.Equal(t, http.StatusUnauthorized, resp.StatusCode)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, "GET", ts.URL+"/v1/events/stream", nil)
	require.NoError(t, err)
	req.Header.Set("Authorization", "Bearer 0123456789abcdef")

	resp, err = http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	assert.Equal(t, http.StatusOK, resp.StatusCode)
}

func TestEventToProtoRoundTrip(t *testing.T) {
	now := time.UnixMilli(1700000000123)
	got := eventToProto(event.Event{
		ID: 7, Type: event.TypeResourceFailover, Severity: event.SeverityWarning,
		Status: event.StatusFiring, Resource: "res1", Node: "n2",
		Message: "moved", Details: map[string]string{"from": "n1"}, Timestamp: now,
	})
	assert.Equal(t, uint64(7), got.Id)
	assert.Equal(t, "resource.failover", got.Type)
	assert.Equal(t, "warning", got.Severity)
	assert.Equal(t, "firing", got.Status)
	assert.Equal(t, int64(1700000000123), got.TimestampUnixMs)
	assert.Equal(t, "n1", got.Details["from"])
}

func TestFilterFromRequestDropsEmptyTypes(t *testing.T) {
	f := filterFromRequest("warning", []string{"", "resource.failover"}, "res1")
	assert.Equal(t, event.SeverityWarning, f.MinSeverity)
	assert.Equal(t, []event.Type{event.TypeResourceFailover}, f.Types)
	assert.Equal(t, "res1", f.Resource)
}

func TestWriteSSEFraming(t *testing.T) {
	var sb strings.Builder
	ok := writeSSE(&sb, event.Event{ID: 5, Type: event.TypeNodeUnreachable, Message: "down"})
	require.True(t, ok)

	out := sb.String()
	assert.True(t, strings.HasPrefix(out, "id: 5\n"), "the id field drives Last-Event-ID reconnection")
	assert.Contains(t, out, "event: node.unreachable\n")
	assert.True(t, strings.HasSuffix(out, "\n\n"), "a frame must end with a blank line")
}

func TestWriteSSEReportsClosedClient(t *testing.T) {
	assert.False(t, writeSSE(errWriter{}, event.Event{ID: 1}))
}

type errWriter struct{}

func (errWriter) Write([]byte) (int, error) { return 0, fmt.Errorf("client gone") }
