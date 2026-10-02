package controller

import (
	"context"
	"encoding/json"
	"testing"

	pb "github.com/haify-project/sds/api/proto/v1"
	"github.com/haify-project/sds/pkg/database"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func drStatus(t *testing.T, raw string) *drbdsetupStatus {
	t.Helper()
	var all []drbdsetupStatus
	require.NoError(t, json.Unmarshal([]byte(raw), &all))
	return &all[0]
}

func TestFailbackReadsWhetherThePrimarySiteCaughtUp(t *testing.T) {
	st := drStatus(t, `[{"name":"wan1","role":"Primary","devices":[{"volume":0,"disk-state":"UpToDate"}],
	  "connections":[
	    {"name":"sdt2","connection-state":"Connected","peer-role":"Secondary",
	     "peer_devices":[{"volume":0,"replication-state":"SyncSource","peer-disk-state":"Inconsistent","done":42.7}]},
	    {"name":"sdt4","connection-state":"Connected","peer-role":"Secondary",
	     "peer_devices":[{"volume":0,"replication-state":"Established","peer-disk-state":"UpToDate"}]},
	    {"name":"sdt5","connection-state":"StandAlone"}]}]`)
	assert.False(t, drPeerInSync(st, "sdt2"))
	assert.True(t, drPeerInSync(st, "sdt4"))
	assert.False(t, drPeerInSync(st, "sdt5"))
	assert.False(t, drPeerInSync(st, "nobody"))
	assert.True(t, connected(st, "sdt2"))
	assert.False(t, connected(st, "sdt5"))

	lag := laggingPeers(st, []string{"sdt2", "sdt4", "sdt5"}, func(n string) string { return n })
	assert.Equal(t, []string{"sdt2 43%", "sdt5 StandAlone"}, lag)
}

func TestFailbackRefusesWhatIsNotAWANFailover(t *testing.T) {
	ctrl := newBasicTestController(&fakeDeploymentClient{})
	ctrl.db = newTestDB(t)
	ctx := context.Background()
	require.NoError(t, ctrl.db.SaveResource(ctx, &database.Resource{Name: "lan1", Nodes: "a,b", Port: 7000}))
	require.NoError(t, ctrl.db.SaveResource(ctx, &database.Resource{Name: "wan1", Nodes: "a,d", Port: 7001, WANMode: true, DRNode: "d"}))
	srv := &Server{ctrl: ctrl, resources: ctrl.resources}
	for _, req := range []*pb.DRFailbackRequest{
		{Name: "lan1"},
		{Name: "nope"},
		{Name: "wan1", Node: "d"},     // the DR is not a primary-site node
		{Name: "wan1", Node: "other"}, // not a node of the resource
	} {
		resp, err := srv.DRFailback(ctx, req)
		require.NoError(t, err)
		assert.False(t, resp.Success, "%+v", req)
	}
}
