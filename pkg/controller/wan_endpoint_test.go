package controller

import (
	"context"
	"strings"
	"testing"

	pb "github.com/haify-project/sds/api/proto/v1"
	"github.com/haify-project/sds/pkg/database"
	"github.com/haify-project/sds/pkg/deployment"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestValidDREndpoint(t *testing.T) {
	for _, ok := range []string{"203.0.113.7", "2001:db8::1", "dr.example.com", "dr-1"} {
		assert.NoError(t, validDREndpoint(ok), ok)
	}
	for _, bad := range []string{"203.0.113.7:4000", "tcp://dr.example.com", "dr_1", "-dr", ""} {
		assert.Error(t, validDREndpoint(bad), bad)
	}
}

// wanEndpointFixture is a WAN resource wan1 (primary p1, DR d1) whose DR port
// answers only on the addresses in reachable.
func wanEndpointFixture(t *testing.T, reachable ...string) (*Server, *Controller) {
	t.Helper()
	withTempPKI(t)
	dep := &fakeDeploymentClient{}
	dep.execFunc = func(_ context.Context, hosts []string, cmd string, _ ...deployment.ExecOption) (*deployment.ExecResult, error) {
		if strings.Contains(cmd, "/dev/tcp/") {
			for _, r := range reachable {
				if strings.Contains(cmd, "/dev/tcp/"+r+"/") {
					return successExecResult(hosts, ""), nil
				}
			}
			return failedResult(hosts, ""), nil
		}
		return successExecResult(hosts, ""), nil
	}
	ctrl := newBasicTestController(dep)
	ctrl.db = newTestDB(t)
	ctrl.hostsMap["p1"] = "10.0.0.1"
	ctrl.hostsMap["d1"] = "10.0.0.9"
	require.NoError(t, ctrl.db.SaveResource(context.Background(), &database.Resource{
		Name: "wan1", Nodes: "p1,d1", Port: 7150, WANMode: true, DRNode: "d1",
		DREndpoint: "203.0.113.7", WANPort: 36075,
	}))
	return &Server{ctrl: ctrl, resources: ctrl.resources}, ctrl
}

func TestSetWanEndpointMovesTheTunnel(t *testing.T) {
	srv, ctrl := wanEndpointFixture(t, "203.0.113.7", "198.51.100.4")
	resp, err := srv.SetWanEndpoint(context.Background(), &pb.SetWanEndpointRequest{Name: "wan1", DrEndpoint: "198.51.100.4"})
	require.NoError(t, err)
	require.True(t, resp.Success, resp.Message)
	r, _ := ctrl.db.GetResource(context.Background(), "wan1")
	assert.Equal(t, "198.51.100.4", r.DREndpoint)
}

// A new endpoint that does not answer is a typo until proven otherwise: the
// working one is put back.
func TestSetWanEndpointRestoresTheOldOneWhenTheNewOneDoesNotAnswer(t *testing.T) {
	srv, ctrl := wanEndpointFixture(t, "203.0.113.7")
	resp, err := srv.SetWanEndpoint(context.Background(), &pb.SetWanEndpointRequest{Name: "wan1", DrEndpoint: "198.51.100.4"})
	require.NoError(t, err)
	assert.False(t, resp.Success)
	assert.Contains(t, resp.Message, "still replicates to 203.0.113.7")
	r, _ := ctrl.db.GetResource(context.Background(), "wan1")
	assert.Equal(t, "203.0.113.7", r.DREndpoint)

	// Unless the operator says the DR is not reachable yet.
	resp, err = srv.SetWanEndpoint(context.Background(), &pb.SetWanEndpointRequest{
		Name: "wan1", DrEndpoint: "198.51.100.4", SkipReachabilityCheck: true})
	require.NoError(t, err)
	assert.True(t, resp.Success, resp.Message)
	r, _ = ctrl.db.GetResource(context.Background(), "wan1")
	assert.Equal(t, "198.51.100.4", r.DREndpoint)
}

func TestSetWanEndpointRefusesBadRequests(t *testing.T) {
	srv, ctrl := wanEndpointFixture(t, "203.0.113.7")
	require.NoError(t, ctrl.db.SaveResource(context.Background(), &database.Resource{Name: "lan1", Nodes: "p1,d1", Port: 7151}))
	for _, req := range []*pb.SetWanEndpointRequest{
		{Name: "wan1"},
		{Name: "wan1", DrEndpoint: "198.51.100.4:4000"},
		{Name: "wan1", EgressAddress: "not-an-ip"},
		{Name: "wan1", EgressAddress: "10.0.0.1", ClearEgress: true},
		{Name: "lan1", DrEndpoint: "198.51.100.4"},
		{Name: "nope", DrEndpoint: "198.51.100.4"},
	} {
		resp, err := srv.SetWanEndpoint(context.Background(), req)
		require.NoError(t, err)
		assert.False(t, resp.Success, "%+v", req)
	}
}
