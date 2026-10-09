package controller

import (
	"bufio"
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"

	"github.com/haify-project/haify/pkg/config"
	"github.com/haify-project/haify/pkg/database"
	"github.com/haify-project/haify/pkg/event"
)

func auditEntries(t *testing.T, db *database.DB, methods ...string) {
	t.Helper()
	for _, m := range methods {
		require.NoError(t, db.AppendAuditEvent(context.Background(), &database.AuditEvent{
			Method: m, Result: "OK", Granted: true, Timestamp: time.Now()}))
	}
}

func newTestShipper(db *database.DB, dests ...auditDestination) *auditShipper {
	return &auditShipper{db: db, dests: dests, log: zap.NewNop(), events: event.NewBus(10),
		failingSince: map[string]time.Time{}, alarmed: map[string]bool{}}
}

// Every entry reaches a TCP syslog server, in order, as RFC 5424 with the
// sequence number in the body.
func TestAuditShipsToSyslogOverTCP(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	defer func() { _ = ln.Close() }()
	lines := make(chan string, 10)
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		sc := bufio.NewScanner(conn)
		for sc.Scan() {
			lines <- sc.Text()
		}
	}()

	db := newTestDB(t)
	auditEntries(t, db, "CreateResource", "DeleteSnapshot")
	dests, err := auditDestinations(config.AuditConfig{Syslog: "tcp://" + ln.Addr().String()})
	require.NoError(t, err)
	s := newTestShipper(db, dests...)
	s.drain(context.Background(), dests[0])

	for i, method := range []string{"CreateResource", "DeleteSnapshot"} {
		select {
		case line := <-lines:
			assert.True(t, strings.HasPrefix(line, "<110>1 "), line) // log audit, informational
			assert.Contains(t, line, " haify-controller - audit - ")
			body := line[strings.Index(line, "{"):]
			var rec database.AuditRecord
			require.NoError(t, json.Unmarshal([]byte(body), &rec))
			assert.EqualValues(t, i+1, rec.Seq)
			assert.Equal(t, method, rec.Event.Method)
		case <-time.After(3 * time.Second):
			t.Fatal("syslog line not received")
		}
	}
	cur, _ := db.AuditShipCursor(context.Background(), dests[0].Name())
	assert.EqualValues(t, 2, cur)
}

func TestSyslogSeverity(t *testing.T) {
	denied, _ := syslogMessage("n1", database.AuditRecord{Seq: 1, Event: &database.AuditEvent{Granted: false}})
	assert.True(t, strings.HasPrefix(string(denied), "<108>1 "), "a refused call is a warning")
	failed, _ := syslogMessage("n1", database.AuditRecord{Seq: 1, Event: &database.AuditEvent{Granted: true, Result: "FAILED"}})
	assert.True(t, strings.HasPrefix(string(failed), "<109>1 "), "a failed call is a notice")
}

// A destination that is down misses nothing: the cursor stays until it
// takes a batch, and the outage becomes an event only once it lasts.
func TestAuditShippingResumesAfterAnOutage(t *testing.T) {
	var down atomic.Bool
	down.Store(true)
	var mu sync.Mutex
	var got []string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if down.Load() {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		assert.Equal(t, "Bearer s3cret", r.Header.Get("Authorization"))
		var body struct {
			Records []database.AuditRecord `json:"records"`
		}
		require.NoError(t, json.NewDecoder(r.Body).Decode(&body))
		mu.Lock()
		for _, rec := range body.Records {
			got = append(got, rec.Event.Method)
		}
		mu.Unlock()
	}))
	defer ts.Close()

	db := newTestDB(t)
	auditEntries(t, db, "A", "B")
	dests, err := auditDestinations(config.AuditConfig{WebhookURL: ts.URL, WebhookToken: "s3cret"})
	require.NoError(t, err)
	s := newTestShipper(db, dests...)
	ctx := context.Background()

	s.drain(ctx, dests[0])
	cur, _ := db.AuditShipCursor(ctx, dests[0].Name())
	assert.Zero(t, cur, "nothing was acknowledged")

	// Long enough to be news.
	s.failingSince[dests[0].Name()] = time.Now().Add(-auditShipAlarmAfter - time.Second)
	s.drain(ctx, dests[0])
	assert.Equal(t, event.TypeAuditShippingFailed, s.events.Recent(event.Filter{}, 0, 10)[0].Type)

	down.Store(false)
	auditEntries(t, db, "C")
	s.drain(ctx, dests[0])
	mu.Lock()
	assert.Equal(t, []string{"A", "B", "C"}, got)
	mu.Unlock()
	recent := s.events.Recent(event.Filter{}, 0, 10)
	assert.Equal(t, event.StatusResolved, recent[len(recent)-1].Status)
}

func TestAuditConfigValidation(t *testing.T) {
	for _, bad := range []string{"syslog.example.com", "http://x:514", "tcp://"} {
		_, _, err := config.AuditConfig{Syslog: bad}.SyslogTarget()
		assert.Error(t, err, bad)
	}
	network, addr, err := config.AuditConfig{Syslog: "tls://logs.example.com"}.SyslogTarget()
	require.NoError(t, err)
	assert.Equal(t, "tls", network)
	assert.Equal(t, "logs.example.com:6514", addr)
}
