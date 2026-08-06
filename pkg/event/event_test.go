package event

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func warn(resource string) Event {
	return Event{Type: TypeResourceDegraded, Severity: SeverityWarning, Resource: resource}
}

func TestPublishStampsIDAndTime(t *testing.T) {
	b := NewBus(10)

	first := b.Publish(warn("res1"))
	second := b.Publish(warn("res2"))

	assert.Equal(t, uint64(1), first.ID, "ids start at 1 so zero means unset")
	assert.Equal(t, uint64(2), second.ID)
	assert.False(t, first.Timestamp.IsZero())

	// An explicit timestamp is preserved; only a zero one is filled in.
	fixed := time.Unix(1700000000, 0)
	got := b.Publish(Event{Type: TypeResourceFailover, Timestamp: fixed})
	assert.True(t, got.Timestamp.Equal(fixed))
}

func TestPublishDefaultsSeverityAndStatus(t *testing.T) {
	got := NewBus(10).Publish(Event{Type: TypeResourcePromoted})
	assert.Equal(t, SeverityInfo, got.Severity)
	assert.Equal(t, StatusInfo, got.Status)
}

func TestSubscriberReceivesMatchingEvents(t *testing.T) {
	b := NewBus(10)
	ch, cancel := b.Subscribe(Filter{MinSeverity: SeverityWarning})
	defer cancel()

	b.Publish(Event{Type: TypeResourcePromoted, Severity: SeverityInfo})
	b.Publish(warn("res1"))

	select {
	case e := <-ch:
		assert.Equal(t, TypeResourceDegraded, e.Type, "the info event must have been filtered out")
	case <-time.After(time.Second):
		t.Fatal("no event delivered")
	}
}

func TestCancelClosesChannelAndIsIdempotent(t *testing.T) {
	b := NewBus(10)
	ch, cancel := b.Subscribe(Filter{})

	cancel()
	_, open := <-ch
	assert.False(t, open, "cancel must close the channel so range loops terminate")

	// Double cancel used to close an already-closed channel.
	assert.NotPanics(t, cancel)

	// The subscription is gone, so publishing must not send on a closed channel.
	assert.NotPanics(t, func() { b.Publish(warn("res1")) })
	_, _, subs := b.Stats()
	assert.Zero(t, subs)
}

// A subscriber that stops reading must lose its own events, not wedge the
// detector that is publishing them.
func TestSlowSubscriberDropsInsteadOfBlocking(t *testing.T) {
	b := NewBus(10)
	_, cancel := b.Subscribe(Filter{})
	defer cancel()

	done := make(chan struct{})
	go func() {
		defer close(done)
		for range subscriberBuffer + 50 {
			b.Publish(warn("res1"))
		}
	}()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Publish blocked on a subscriber that never reads")
	}

	published, dropped, _ := b.Stats()
	assert.Equal(t, uint64(subscriberBuffer+50), published)
	assert.Equal(t, uint64(50), dropped, "everything past the buffer must be counted as dropped")
}

func TestRecentReturnsOldestFirstAndTrimsToNewest(t *testing.T) {
	b := NewBus(10)
	for range 5 {
		b.Publish(warn("res1"))
	}

	all := b.Recent(Filter{}, 0, 0)
	require.Len(t, all, 5)
	assert.Equal(t, uint64(1), all[0].ID)
	assert.Equal(t, uint64(5), all[4].ID)

	// A limit keeps the newest, which is what an operator wants to see first.
	last2 := b.Recent(Filter{}, 0, 2)
	require.Len(t, last2, 2)
	assert.Equal(t, uint64(4), last2[0].ID)
	assert.Equal(t, uint64(5), last2[1].ID)
}

func TestRecentSinceIDResumes(t *testing.T) {
	b := NewBus(10)
	for range 5 {
		b.Publish(warn("res1"))
	}
	got := b.Recent(Filter{}, 3, 0)
	require.Len(t, got, 2)
	assert.Equal(t, uint64(4), got[0].ID)
}

// The history is a ring: once it wraps, the oldest entries are gone but the
// order of what remains must still be correct.
func TestRecentSurvivesWrapAround(t *testing.T) {
	b := NewBus(3)
	for range 5 {
		b.Publish(warn("res1"))
	}
	got := b.Recent(Filter{}, 0, 0)
	require.Len(t, got, 3)
	assert.Equal(t, []uint64{3, 4, 5}, []uint64{got[0].ID, got[1].ID, got[2].ID})
}

func TestRecentIgnoresUnwrittenSlots(t *testing.T) {
	b := NewBus(10)
	b.Publish(warn("res1"))
	assert.Len(t, b.Recent(Filter{}, 0, 0), 1, "empty ring slots must not be returned as events")
}

func TestFilterMatch(t *testing.T) {
	e := Event{Type: TypeResourceFailover, Severity: SeverityWarning, Resource: "res1"}

	assert.True(t, Filter{}.Match(e), "the zero filter keeps everything")
	assert.True(t, Filter{MinSeverity: SeverityWarning}.Match(e))
	assert.False(t, Filter{MinSeverity: SeverityCritical}.Match(e))
	assert.True(t, Filter{Types: []Type{TypeResourceFailover}}.Match(e))
	assert.False(t, Filter{Types: []Type{TypeNodeUnreachable}}.Match(e))
	assert.True(t, Filter{Resource: "res1"}.Match(e))
	assert.False(t, Filter{Resource: "other"}.Match(e))
}

func TestSeverityOrdering(t *testing.T) {
	assert.True(t, SeverityCritical.AtLeast(SeverityWarning))
	assert.True(t, SeverityWarning.AtLeast(SeverityWarning))
	assert.False(t, SeverityInfo.AtLeast(SeverityWarning))

	// A typo in a config threshold must not silently swallow critical alerts,
	// so an unrecognised value has to behave as the most permissive one.
	assert.Equal(t, SeverityInfo, ParseSeverity("WARN"))
	assert.True(t, SeverityCritical.AtLeast(ParseSeverity("nonsense")))

	assert.Equal(t, SeverityCritical, ParseSeverity("critical"))
	assert.Equal(t, SeverityWarning, ParseSeverity("warning"))
}

func TestEventKeyPairsFiringWithResolved(t *testing.T) {
	firing := Event{Type: TypeResourceDegraded, Resource: "res1", Node: "n2", Status: StatusFiring}
	resolved := Event{Type: TypeResourceDegraded, Resource: "res1", Node: "n2", Status: StatusResolved}
	other := Event{Type: TypeResourceDegraded, Resource: "res1", Node: "n3"}

	assert.Equal(t, firing.Key(), resolved.Key())
	assert.NotEqual(t, firing.Key(), other.Key())
}

func TestNewBusDefaultsHistory(t *testing.T) {
	assert.Len(t, NewBus(0).hist, DefaultHistory)
	assert.Len(t, NewBus(-1).hist, DefaultHistory)
}
