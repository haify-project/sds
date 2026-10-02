package controller

import (
	"context"
	"testing"

	"github.com/haify-project/sds/pkg/rbac"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

func testEngine(t *testing.T) *rbac.Engine {
	t.Helper()
	e, err := rbac.New(nil, []rbac.User{
		{Name: "alice", Token: "alice-token-0123456789", Role: "admin"},
		{Name: "victor", Token: "victor-token-0123456789", Role: "viewer"},
	}, nil)
	if err != nil {
		t.Fatalf("rbac.New: %v", err)
	}
	return e
}

func ctxWithToken(token string) context.Context {
	md := metadata.Pairs("authorization", "Bearer "+token)
	return metadata.NewIncomingContext(context.Background(), md)
}

func TestRBACIdentityResolvesUser(t *testing.T) {
	engine := testEngine(t)
	interceptor := rbacIdentityUnaryInterceptor(engine)
	var seenUser string
	handler := func(ctx context.Context, _ interface{}) (interface{}, error) {
		seenUser = userFromContext(ctx)
		return struct{}{}, nil
	}
	info := &grpc.UnaryServerInfo{FullMethod: "/v1.SDSController/ListPools"}
	if _, err := interceptor(ctxWithToken("alice-token-0123456789"), nil, info, handler); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if seenUser != "alice" {
		t.Errorf("handler saw user %q, want alice", seenUser)
	}
}

func TestRBACIdentityRejectsUnknownToken(t *testing.T) {
	engine := testEngine(t)
	interceptor := rbacIdentityUnaryInterceptor(engine)
	handler := func(context.Context, interface{}) (interface{}, error) { return nil, nil }
	info := &grpc.UnaryServerInfo{FullMethod: "/v1.SDSController/ListPools"}
	_, err := interceptor(ctxWithToken("bogus"), nil, info, handler)
	if status.Code(err) != codes.Unauthenticated {
		t.Errorf("got %v, want Unauthenticated", status.Code(err))
	}
}

func TestRBACAuthzEnforcesRole(t *testing.T) {
	engine := testEngine(t)
	authz := rbacAuthzUnaryInterceptor(engine)
	handler := func(context.Context, interface{}) (interface{}, error) { return struct{}{}, nil }

	cases := []struct {
		name   string
		user   string
		method string
		want   codes.Code
	}{
		{"admin write ok", "alice", "/v1.SDSController/CreatePool", codes.OK},
		{"viewer read ok", "victor", "/v1.SDSController/ListPools", codes.OK},
		{"viewer write denied", "victor", "/v1.SDSController/CreatePool", codes.PermissionDenied},
		{"viewer gateway write denied", "victor", "/v1.SDSController/CreateNFSGateway", codes.PermissionDenied},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			ctx := context.WithValue(context.Background(), ctxUserKey{}, c.user)
			info := &grpc.UnaryServerInfo{FullMethod: c.method}
			_, err := authz(ctx, nil, info, handler)
			if status.Code(err) != c.want {
				t.Errorf("got %v, want %v", status.Code(err), c.want)
			}
		})
	}
}

func TestRBACHealthCheckBypassed(t *testing.T) {
	engine := testEngine(t)
	identity := rbacIdentityUnaryInterceptor(engine)
	called := false
	handler := func(context.Context, interface{}) (interface{}, error) {
		called = true
		return struct{}{}, nil
	}
	info := &grpc.UnaryServerInfo{FullMethod: "/grpc.health.v1.Health/Check"}
	// No token at all, but health must pass through.
	if _, err := identity(context.Background(), nil, info, handler); err != nil {
		t.Fatalf("health check should bypass auth: %v", err)
	}
	if !called {
		t.Errorf("handler not invoked for health check")
	}
}
