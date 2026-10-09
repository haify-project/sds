package controller

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/grpc-ecosystem/grpc-gateway/v2/runtime"
	haifypb "github.com/haify-project/haify/api/proto/v1"
	"github.com/haify-project/haify/pkg/config"
	"github.com/haify-project/haify/pkg/database"
	"github.com/haify-project/haify/pkg/gateway"
	"github.com/haify-project/haify/pkg/rbac"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"
)

func newControllerRBACEngine(t *testing.T) *rbac.Engine {
	t.Helper()
	engine, err := rbac.New(nil, []rbac.User{
		{Name: "admin", Token: "admin-token", Role: "admin"},
		{Name: "viewer", Token: "viewer-token", Role: "viewer"},
	}, nil)
	require.NoError(t, err)
	return engine
}

func TestRBACAdaptersStoreAndStreamInterceptors(t *testing.T) {
	users := toRBACUsers([]config.RBACUser{{Name: "a", Token: "t", Role: "admin"}})
	policies := toRBACPolicies([]config.RBACPolicy{{Role: "custom", Object: "node", Action: "read"}})
	assert.Equal(t, "a", users[0].Name)
	assert.Equal(t, "node", policies[0].Object)

	db := newTestDB(t)
	store := dbRBACStore{db: db}
	loaded, err := store.Load()
	require.NoError(t, err)
	assert.Nil(t, loaded)
	snapshot := &rbac.Snapshot{Policies: [][]string{{"admin", "*", "*"}}, Tokens: map[string]string{"digest": "admin"}}
	require.NoError(t, store.Save(snapshot))
	loaded, err = store.Load()
	require.NoError(t, err)
	assert.Equal(t, snapshot.Policies, loaded.Policies)

	engine := newControllerRBACEngine(t)
	ctx := metadata.NewIncomingContext(context.Background(), metadata.Pairs("authorization", "Bearer admin-token"))
	stream := &fakeServerStream{ctx: ctx}
	identity := rbacIdentityStreamInterceptor(engine)
	called := false
	err = identity(nil, stream, &grpc.StreamServerInfo{FullMethod: "/haify.v1.HaifyController/ListNodes"}, func(_ interface{}, ss grpc.ServerStream) error {
		called = true
		assert.Equal(t, "admin", userFromContext(ss.Context()))
		return nil
	})
	require.NoError(t, err)
	assert.True(t, called)

	authz := rbacAuthzStreamInterceptor(engine)
	adminCtx := context.WithValue(context.Background(), ctxUserKey{}, "admin")
	require.NoError(t, authz(nil, &fakeServerStream{ctx: adminCtx}, &grpc.StreamServerInfo{FullMethod: "/haify.v1.HaifyController/DeleteResource"}, func(interface{}, grpc.ServerStream) error { return nil }))
	viewerCtx := context.WithValue(context.Background(), ctxUserKey{}, "viewer")
	err = authz(nil, &fakeServerStream{ctx: viewerCtx}, &grpc.StreamServerInfo{FullMethod: "/haify.v1.HaifyController/DeleteResource"}, func(interface{}, grpc.ServerStream) error { return nil })
	assert.ErrorIs(t, err, errPermissionDenied)

	healthCalled := false
	require.NoError(t, identity(nil, &fakeServerStream{ctx: context.Background()}, &grpc.StreamServerInfo{FullMethod: healthServicePrefix + "Check"}, func(interface{}, grpc.ServerStream) error {
		healthCalled = true
		return nil
	}))
	assert.True(t, healthCalled)
}

func TestRBACRESTMutationRoutes(t *testing.T) {
	engine := newControllerRBACEngine(t)
	mux := runtime.NewServeMux()
	ctrl := &Controller{logger: zap.NewNop()}
	ctrl.registerRBACRoutes(mux, engine)

	request := func(method, path, token, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, path, bytes.NewBufferString(body))
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		rr := httptest.NewRecorder()
		mux.ServeHTTP(rr, req)
		return rr
	}

	assert.Equal(t, http.StatusOK, request(http.MethodGet, "/v1/rbac/roles", "viewer-token", "").Code)
	assert.Equal(t, http.StatusUnauthorized, request(http.MethodGet, "/v1/rbac/roles", "bad", "").Code)
	assert.Equal(t, http.StatusForbidden, request(http.MethodPost, "/v1/rbac/users", "viewer-token", `{}`).Code)
	assert.Equal(t, http.StatusBadRequest, request(http.MethodPost, "/v1/rbac/users", "admin-token", `{`).Code)
	created := request(http.MethodPost, "/v1/rbac/users", "admin-token", `{"name":"operator1","role":"operator","token":"operator-token-1234"}`)
	assert.Equal(t, http.StatusOK, created.Code)
	assert.Contains(t, created.Body.String(), "operator1")
	assert.Equal(t, http.StatusBadRequest, request(http.MethodPut, "/v1/rbac/users/operator1/role", "admin-token", `{`).Code)
	assert.Equal(t, http.StatusOK, request(http.MethodPut, "/v1/rbac/users/operator1/role", "admin-token", `{"role":"viewer"}`).Code)
	assert.Equal(t, http.StatusOK, request(http.MethodDelete, "/v1/rbac/users/operator1", "admin-token", "").Code)
	assert.Equal(t, http.StatusBadRequest, request(http.MethodDelete, "/v1/rbac/users/operator1", "admin-token", "").Code)
	assert.Equal(t, http.StatusOK, request(http.MethodGet, "/v1/rbac/policies", "admin-token", "").Code)
}

type gatewayAdapterDeployment struct {
	distributed []string
	commands    []string
	err         error
}

func (d *gatewayAdapterDeployment) DistributeConfig(_ context.Context, _ []string, content, path string) error {
	d.distributed = append(d.distributed, path+":"+content)
	return d.err
}
func (d *gatewayAdapterDeployment) Exec(_ context.Context, _ []string, cmd string) error {
	d.commands = append(d.commands, cmd)
	return d.err
}

func TestGatewayServerValidationAndLifecycle(t *testing.T) {
	ctrl := newBasicTestController(&fakeDeploymentClient{})
	ctrl.db = newTestDB(t)
	ctrl.gateway = gateway.New(NewGatewayResourceManager(ctrl.resources, false, 1), &gatewayAdapterDeployment{}, zap.NewNop(), nil)
	srv := NewServer(ctrl)
	ctx := context.Background()

	nfs, err := srv.CreateNFSGateway(ctx, &haifypb.CreateNFSGatewayRequest{Resource: "r", ServiceIp: "invalid"})
	require.Error(t, err)
	assert.False(t, nfs.Success)
	iscsi, err := srv.CreateISCSIGateway(ctx, &haifypb.CreateISCSIGatewayRequest{Resource: "r", ServiceIp: "invalid"})
	require.Error(t, err)
	assert.False(t, iscsi.Success)
	nvme, err := srv.CreateNVMeGateway(ctx, &haifypb.CreateNVMeGatewayRequest{Resource: "r", ServiceIp: "invalid"})
	require.Error(t, err)
	assert.False(t, nvme.Success)

	require.NoError(t, ctrl.db.SaveGateway(ctx, &database.Gateway{Name: "r-nfs", Resource: "r", Type: database.GatewayTypeNFS, Status: "configured"}))
	started, err := srv.StartGateway(ctx, &haifypb.StartGatewayRequest{Id: "r"})
	require.NoError(t, err)
	assert.True(t, started.Success)
	stored, err := ctrl.db.GetGatewayByResource(ctx, "r")
	require.NoError(t, err)
	assert.Equal(t, "started", stored.Status)
	stopped, err := srv.StopGateway(ctx, &haifypb.StopGatewayRequest{Id: "r"})
	require.NoError(t, err)
	assert.True(t, stopped.Success)
	deleted, err := srv.DeleteGateway(ctx, &haifypb.DeleteGatewayRequest{Id: "r"})
	require.NoError(t, err)
	assert.True(t, deleted.Success)
}

func TestGatewayServerWrapper(t *testing.T) {
	grpcServer := grpc.NewServer()
	server := NewGatewayServer(grpcServer, "127.0.0.1:43513", 43514, zap.NewNop())
	require.NotNil(t, server)
	require.NoError(t, server.Start())
	assert.NotNil(t, server.Handler())
	mux := runtime.NewServeMux()
	require.NoError(t, RegisterGatewayHandler(context.Background(), mux, "127.0.0.1:43513", zap.NewNop()))
}
