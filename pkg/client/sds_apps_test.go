package client

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	sdspb "github.com/haify-project/sds/api/proto/v1"
)

func (m *mockServer) CreateApp(_ context.Context, req *sdspb.CreateAppRequest) (*sdspb.CreateAppResponse, error) {
	if req.Name == "taken" {
		return &sdspb.CreateAppResponse{Success: false, Message: "app taken already exists"}, nil
	}
	return &sdspb.CreateAppResponse{Success: true, Password: "pw", App: &sdspb.AppInfo{Name: req.Name,
		Engine: req.Engine, Resource: req.Resource, Vector: req.Vector}}, nil
}

func (m *mockServer) ListApps(context.Context, *sdspb.ListAppsRequest) (*sdspb.ListAppsResponse, error) {
	return &sdspb.ListAppsResponse{Success: true, Apps: []*sdspb.AppInfo{{Name: "orders"}}}, nil
}

func (m *mockServer) GetAppStatus(_ context.Context, req *sdspb.GetAppStatusRequest) (*sdspb.GetAppStatusResponse, error) {
	return &sdspb.GetAppStatusResponse{Success: true, State: "running", PrimaryNode: "node1",
		App: &sdspb.AppInfo{Name: req.Name}}, nil
}

func (m *mockServer) DeleteApp(_ context.Context, req *sdspb.DeleteAppRequest) (*sdspb.DeleteAppResponse, error) {
	if req.DeleteData {
		return &sdspb.DeleteAppResponse{Success: true, Message: "app and resource deleted"}, nil
	}
	return &sdspb.DeleteAppResponse{Success: true, Message: "app deleted"}, nil
}

func (m *mockServer) FailoverApp(context.Context, *sdspb.FailoverAppRequest) (*sdspb.FailoverAppResponse, error) {
	return &sdspb.FailoverAppResponse{Success: true, FromNode: "node1", ToNode: "node2"}, nil
}

func (m *mockServer) SnapshotApp(context.Context, *sdspb.SnapshotAppRequest) (*sdspb.SnapshotAppResponse, error) {
	return &sdspb.SnapshotAppResponse{Success: true, Frozen: true}, nil
}

func TestSDSClientApps(t *testing.T) {
	c, cleanup := setupMockClient(t)
	defer cleanup()
	ctx := context.Background()

	created, err := c.CreateApp(ctx, AppCreateRequest{Name: "orders", Engine: "postgres", Resource: "res1", Vector: true})
	require.NoError(t, err)
	assert.Equal(t, "pw", created.Password)
	assert.True(t, created.App.Vector)
	_, err = c.CreateApp(ctx, AppCreateRequest{Name: "taken"})
	assert.ErrorContains(t, err, "already exists")

	apps, err := c.ListApps(ctx)
	require.NoError(t, err)
	require.Len(t, apps, 1)

	st, err := c.GetAppStatus(ctx, "orders")
	require.NoError(t, err)
	assert.Equal(t, "running", st.State)

	msg, err := c.DeleteApp(ctx, "orders", true)
	require.NoError(t, err)
	assert.Equal(t, "app and resource deleted", msg)

	fo, err := c.FailoverApp(ctx, "orders")
	require.NoError(t, err)
	assert.Equal(t, "node2", fo.ToNode)

	snap, err := c.SnapshotApp(ctx, "orders", "s1")
	require.NoError(t, err)
	assert.True(t, snap.Frozen)
}
