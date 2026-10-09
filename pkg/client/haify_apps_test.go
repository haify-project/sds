package client

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	haifypb "github.com/haify-project/haify/api/proto/v1"
)

func (m *mockServer) CreateApp(_ context.Context, req *haifypb.CreateAppRequest) (*haifypb.CreateAppResponse, error) {
	if req.Name == "taken" {
		return &haifypb.CreateAppResponse{Success: false, Message: "app taken already exists"}, nil
	}
	return &haifypb.CreateAppResponse{Success: true, Password: "pw", App: &haifypb.AppInfo{Name: req.Name,
		Engine: req.Engine, Resource: req.Resource, Vector: req.Vector}}, nil
}

func (m *mockServer) ListApps(context.Context, *haifypb.ListAppsRequest) (*haifypb.ListAppsResponse, error) {
	return &haifypb.ListAppsResponse{Success: true, Apps: []*haifypb.AppInfo{{Name: "orders"}}}, nil
}

func (m *mockServer) GetAppStatus(_ context.Context, req *haifypb.GetAppStatusRequest) (*haifypb.GetAppStatusResponse, error) {
	return &haifypb.GetAppStatusResponse{Success: true, State: "running", PrimaryNode: "node1",
		App: &haifypb.AppInfo{Name: req.Name}}, nil
}

func (m *mockServer) DeleteApp(_ context.Context, req *haifypb.DeleteAppRequest) (*haifypb.DeleteAppResponse, error) {
	if req.DeleteData {
		return &haifypb.DeleteAppResponse{Success: true, Message: "app and resource deleted"}, nil
	}
	return &haifypb.DeleteAppResponse{Success: true, Message: "app deleted"}, nil
}

func (m *mockServer) FailoverApp(context.Context, *haifypb.FailoverAppRequest) (*haifypb.FailoverAppResponse, error) {
	return &haifypb.FailoverAppResponse{Success: true, FromNode: "node1", ToNode: "node2"}, nil
}

func (m *mockServer) SnapshotApp(context.Context, *haifypb.SnapshotAppRequest) (*haifypb.SnapshotAppResponse, error) {
	return &haifypb.SnapshotAppResponse{Success: true, Frozen: true}, nil
}

func TestHaifyClientApps(t *testing.T) {
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
