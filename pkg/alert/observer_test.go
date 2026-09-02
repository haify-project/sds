package alert

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// recordingObserver captures what a poll published.
type recordingObserver struct {
	seen []Observation
}

func (r *recordingObserver) Observed(obs Observation) { r.seen = append(r.seen, obs) }

// The observer exists so one poll can serve both events and metrics. Nothing
// else proves the monitor actually calls it: an Observer that is declared,
// implemented and never invoked leaves /metrics silently empty, which is
// indistinguishable from a healthy idle cluster.
func TestPollPublishesToObserver(t *testing.T) {
	obs := &recordingObserver{}
	mon, _ := newHarness(t, Options{
		Resources: &mockLister{list: []ResourceStatusInfo{{
			Name:       "res1",
			NodeStates: map[string]NodeStateInfo{"n1": healthy("Primary")},
		}}},
		Nodes:    &mockNodeLister{list: []NodeStatusInfo{{Name: "n1", Reachable: true}}},
		Observer: obs,
	})

	mon.Poll(context.Background())

	require.Len(t, obs.seen, 1, "each poll must reach the observer exactly once")
	got := obs.seen[0]
	assert.True(t, got.Resources.Enabled)
	require.Len(t, got.Resources.Items, 1)
	assert.Equal(t, "res1", got.Resources.Items[0].Name)
	assert.True(t, got.Nodes.Enabled)
	require.Len(t, got.Nodes.Items, 1)
	assert.Equal(t, "n1", got.Nodes.Items[0].Name)

	mon.Poll(context.Background())
	assert.Len(t, obs.seen, 2, "a second poll publishes a second observation")
}

// A source that failed must still reach the observer, marked as not having
// answered. Dropping the observation instead would let the consumer's gauges go
// stale with no way to tell that they had.
func TestPollPublishesFailedSourcesToObserver(t *testing.T) {
	obs := &recordingObserver{}
	mon, _ := newHarness(t, Options{
		Resources: &mockLister{err: context.DeadlineExceeded},
		Observer:  obs,
	})

	mon.Poll(context.Background())

	require.Len(t, obs.seen, 1)
	assert.True(t, obs.seen[0].Resources.Enabled, "the source was configured")
	assert.False(t, obs.seen[0].Resources.OK, "and it did not answer")
	assert.False(t, obs.seen[0].Nodes.Enabled, "an unconfigured source is not an unanswered one")
}

// A nil observer is the default and must stay harmless.
func TestPollWithoutObserverDoesNotPanic(t *testing.T) {
	mon, _ := newHarness(t, Options{
		Resources: &mockLister{list: []ResourceStatusInfo{}},
	})
	assert.NotPanics(t, func() { mon.Poll(context.Background()) })
}
