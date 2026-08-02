package logbuf

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

func loggerWith(ring *Ring, lvl zapcore.Level) *zap.Logger {
	return zap.New(ring.Core(zap.NewAtomicLevelAt(lvl)))
}

func messages(entries []Entry) []string {
	out := make([]string, 0, len(entries))
	for _, e := range entries {
		out = append(out, e.Message)
	}
	return out
}

// Newest first is what a log view wants: the interesting line is the last one
// written, and it should not require paging to reach it.
func TestListReturnsNewestFirst(t *testing.T) {
	ring := New(10)
	log := loggerWith(ring, zapcore.DebugLevel)
	log.Info("first")
	log.Info("second")
	log.Info("third")

	entries, truncated := ring.List(Filter{})
	assert.Equal(t, []string{"third", "second", "first"}, messages(entries))
	assert.False(t, truncated, "a buffer that has not filled up has discarded nothing")
}

// Once the ring wraps it must keep serving the newest entries — not the oldest,
// and not a garbled mix across the wrap point.
func TestRingWrapsAndKeepsNewest(t *testing.T) {
	ring := New(3)
	log := loggerWith(ring, zapcore.DebugLevel)
	for _, m := range []string{"a", "b", "c", "d", "e"} {
		log.Info(m)
	}

	entries, truncated := ring.List(Filter{})
	assert.Equal(t, []string{"e", "d", "c"}, messages(entries))
	assert.True(t, truncated, "callers must be told that older lines were dropped")
}

func TestLimitCapsResults(t *testing.T) {
	ring := New(100)
	log := loggerWith(ring, zapcore.DebugLevel)
	for i := 0; i < 50; i++ {
		log.Info("line")
	}

	entries, _ := ring.List(Filter{Limit: 5})
	assert.Len(t, entries, 5)
}

func TestLevelFilterKeepsSeverityAndAbove(t *testing.T) {
	ring := New(20)
	log := loggerWith(ring, zapcore.DebugLevel)
	log.Debug("noise")
	log.Info("routine")
	log.Warn("suspicious")
	log.Error("broken")

	entries, _ := ring.List(Filter{MinLevel: zapcore.WarnLevel, HasLevel: true})
	assert.Equal(t, []string{"broken", "suspicious"}, messages(entries))
}

func TestContainsFilterIsCaseInsensitive(t *testing.T) {
	ring := New(20)
	log := loggerWith(ring, zapcore.DebugLevel)
	log.Info("Evicting HA resource")
	log.Info("unrelated chatter")

	entries, _ := ring.List(Filter{Contains: "evicting"})
	assert.Equal(t, []string{"Evicting HA resource"}, messages(entries))
}

// Structured fields are the useful part of a zap line — "Found active node" is
// worthless without the node. They must survive into the buffer, including the
// ones bound by With() on a derived logger.
func TestFieldsAndLoggerNameAreCaptured(t *testing.T) {
	ring := New(10)
	log := loggerWith(ring, zapcore.DebugLevel).
		Named("audit").
		With(zap.String("component", "selfha"))
	log.Info("Found active node", zap.String("node", "sds-e"), zap.Int("attempt", 2))

	entries, _ := ring.List(Filter{})
	require.Len(t, entries, 1)
	e := entries[0]
	assert.Equal(t, "audit", e.Logger)
	assert.Equal(t, "info", e.Level)
	assert.Equal(t, "selfha", e.Fields["component"])
	assert.Equal(t, "sds-e", e.Fields["node"])
	assert.Equal(t, "2", e.Fields["attempt"])
}

// Two loggers derived from the same core must not see each other's fields.
// Appending into a shared backing array is the easy way to get this wrong.
func TestDerivedLoggersDoNotShareFields(t *testing.T) {
	ring := New(10)
	base := loggerWith(ring, zapcore.DebugLevel).With(zap.String("shared", "yes"))
	base.With(zap.String("only", "a")).Info("from a")
	base.With(zap.String("only", "b")).Info("from b")

	entries, _ := ring.List(Filter{})
	require.Len(t, entries, 2)
	byMessage := map[string]Entry{}
	for _, e := range entries {
		byMessage[e.Message] = e
	}
	assert.Equal(t, "a", byMessage["from a"].Fields["only"])
	assert.Equal(t, "b", byMessage["from b"].Fields["only"])
	assert.Equal(t, "yes", byMessage["from a"].Fields["shared"])
}

// The ring is written from every goroutine that logs and read by API handlers.
func TestConcurrentWritesAndReads(t *testing.T) {
	ring := New(64)
	log := loggerWith(ring, zapcore.DebugLevel)

	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < 500; i++ {
			log.Info("write")
		}
	}()
	deadline := time.After(5 * time.Second)
	for {
		select {
		case <-done:
			entries, _ := ring.List(Filter{})
			assert.NotEmpty(t, entries)
			return
		case <-deadline:
			t.Fatal("writer did not finish")
		default:
			ring.List(Filter{Limit: 10})
		}
	}
}
