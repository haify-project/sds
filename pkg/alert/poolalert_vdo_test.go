package alert

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/haify-project/sds/pkg/event"
)

func vdoPoolAt(data, physical float64, known bool) PoolStatusInfo {
	p := poolAt(data)
	p.VDO = true
	p.VDOPhysicalKnown = known
	p.VDOPhysicalPercent = physical
	return p
}

func TestVDOPhysicalFullFiresWhileThinPoolLooksEmpty(t *testing.T) {
	// The case VDO adds: the thin pool has handed out little, but the
	// physical space under it is nearly gone.
	lister := &mockPoolLister{list: []PoolStatusInfo{vdoPoolAt(20, 99, true)}}
	mon, drain := poolHarness(t, lister)

	mon.Poll(context.Background())
	evts := drain()
	require.Len(t, evts, 1)
	assert.Equal(t, event.TypePoolVDOPhysicalFull, evts[0].Type)
	assert.Equal(t, event.SeverityCritical, evts[0].Severity)
	assert.Equal(t, "VDO physical", evts[0].Details["dimension"])
}

func TestVDOPhysicalUnknownHoldsTheCondition(t *testing.T) {
	lister := &mockPoolLister{list: []PoolStatusInfo{vdoPoolAt(20, 99, true)}}
	mon, drain := poolHarness(t, lister)
	mon.Poll(context.Background())
	require.Len(t, drain(), 1)

	// A poll that could not read the VDO figure neither clears the alert
	// nor reports the pool as having lost its thin pool.
	lister.list = []PoolStatusInfo{vdoPoolAt(20, 0, false)}
	mon.Poll(context.Background())
	assert.Empty(t, drain())

	lister.list = []PoolStatusInfo{vdoPoolAt(20, 10, true)}
	mon.Poll(context.Background())
	evts := drain()
	require.Len(t, evts, 1)
	assert.Equal(t, event.StatusResolved, evts[0].Status)
}

func TestNonVDOPoolHasNoVDOConditions(t *testing.T) {
	p := poolAt(20)
	p.VDOPhysicalPercent = 99 // ignored: not a VDO pool
	mon, drain := poolHarness(t, &mockPoolLister{list: []PoolStatusInfo{p}})
	mon.Poll(context.Background())
	assert.Empty(t, drain())
}
