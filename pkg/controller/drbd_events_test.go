package controller

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"go.uber.org/zap"
)

func TestDRBDEventKind(t *testing.T) {
	cases := map[string]eventsLineKind{
		"2026-09-29T14:15:11.400926+08:00 exists -":                                                      eventsInitialDone,
		"2026-09-29T14:15:14.414179+08:00 exists connection name:r peer-node-id:2 connection:Connected":  eventsIgnore,
		"2026-09-29T14:15:14.414179+08:00 change connection name:r peer-node-id:2 connection:Connecting": eventsChange,
		"2026-09-29T14:15:14.414179+08:00 call helper name:r peer-node-id:1 helper:split-brain":          eventsChange,
		"change peer-device name:r peer-node-id:1 volume:0 replication:SyncSource":                       eventsChange,
		"": eventsIgnore,
	}
	for line, want := range cases {
		if got := drbdEventKind(line); got != want {
			t.Errorf("%q: got %d, want %d", line, got, want)
		}
	}
}

type fakeMonitor struct {
	kicks atomic.Int32
	mu    sync.Mutex
	live  []bool
}

func (m *fakeMonitor) Kick() { m.kicks.Add(1) }
func (m *fakeMonitor) SetEventDriven(on bool) {
	m.mu.Lock()
	m.live = append(m.live, on)
	m.mu.Unlock()
}
func (m *fakeMonitor) last() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.live) > 0 && m.live[len(m.live)-1]
}

// fakeStreamer plays each host's lines, then holds the stream open until the
// test releases that host.
type fakeStreamer struct {
	lines   map[string][]string
	release map[string]chan struct{}
}

func (f *fakeStreamer) StreamLines(ctx context.Context, host, _ string, onLine func(string)) error {
	for _, l := range f.lines[host] {
		onLine(l)
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-f.release[host]:
		return nil
	}
}

func TestDRBDEventWatcherDrivesMonitor(t *testing.T) {
	ready := "2026-09-29T14:15:11+08:00 exists -"
	change := "2026-09-29T14:15:12+08:00 change resource name:r role:Primary"
	fs := &fakeStreamer{
		lines:   map[string][]string{"a": {ready, change}, "b": {ready}},
		release: map[string]chan struct{}{"a": make(chan struct{}), "b": make(chan struct{})},
	}
	mon := &fakeMonitor{}
	w := &drbdEventWatcher{streamer: fs, monitor: mon, hosts: func() []string { return []string{"a", "b"} },
		log: zap.NewNop(), streams: map[string]*drbdEventStream{}}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	w.reconcile(ctx)
	waitFor(t, func() bool { return mon.last() }, "event-driven once both nodes printed their state")
	waitFor(t, func() bool { return mon.kicks.Load() >= 1 }, "a change kicks the monitor")

	before := mon.kicks.Load()
	close(fs.release["b"]) // b's stream ends: its changes are no longer seen
	waitFor(t, func() bool { return !mon.last() }, "not event-driven while a node's stream is down")
	waitFor(t, func() bool { return mon.kicks.Load() > before }, "a lost stream asks for a look now")
}

func waitFor(t *testing.T, cond func() bool, what string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatal(what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}
