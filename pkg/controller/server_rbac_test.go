package controller

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	pb "github.com/haify-project/haify/api/proto/v1"
	"github.com/haify-project/haify/pkg/rbac"
)

const (
	adminToken  = "alice-token-0123456789"
	viewerToken = "victor-token-0123456789"
)

func rbacServer(engine *rbac.Engine) *Server {
	return &Server{ctrl: &Controller{rbac: engine}}
}

func TestRbacRPCsReportDisabledWithoutEngine(t *testing.T) {
	s := rbacServer(nil)
	ctx := context.Background()

	who, err := s.GetRbacWhoami(ctx, &pb.GetRbacWhoamiRequest{})
	require.NoError(t, err)
	assert.False(t, who.Enabled)

	pol, err := s.ListRbacPolicies(ctx, &pb.ListRbacPoliciesRequest{})
	require.NoError(t, err)
	assert.False(t, pol.Enabled)

	add, err := s.CreateRbacUser(ctx, &pb.CreateRbacUserRequest{Name: "bob", Role: "viewer"})
	require.NoError(t, err)
	assert.False(t, add.Enabled)
	assert.False(t, add.Success)
}

func TestRbacWhoamiReportsCaller(t *testing.T) {
	s := rbacServer(testEngine(t))

	who, err := s.GetRbacWhoami(ctxWithToken(viewerToken), &pb.GetRbacWhoamiRequest{})
	require.NoError(t, err)
	assert.True(t, who.Enabled)
	assert.Equal(t, "victor", who.User)
	assert.Equal(t, "viewer", who.Role)
	assert.False(t, who.CanAdmin)

	_, err = s.GetRbacWhoami(ctxWithToken("not-a-known-token-xxxx"), &pb.GetRbacWhoamiRequest{})
	assert.Equal(t, codes.Unauthenticated, status.Code(err))
}

// A viewer holds system:read, so the interceptor lets ListRbacPolicies
// through; the handler is what keeps the user list from them.
func TestListRbacPoliciesIsAdminOnly(t *testing.T) {
	s := rbacServer(testEngine(t))

	_, err := s.ListRbacPolicies(ctxWithToken(viewerToken), &pb.ListRbacPoliciesRequest{})
	assert.Equal(t, codes.PermissionDenied, status.Code(err))

	res, err := s.ListRbacPolicies(ctxWithToken(adminToken), &pb.ListRbacPoliciesRequest{})
	require.NoError(t, err)
	assert.True(t, res.Enabled)
	assert.NotEmpty(t, res.Policies)
	names := map[string]string{}
	for _, u := range res.Users {
		names[u.Name] = u.Role
	}
	assert.Equal(t, map[string]string{"alice": "admin", "victor": "viewer"}, names)
}

func TestRbacUserLifecycleOverGRPC(t *testing.T) {
	engine := testEngine(t)
	s := rbacServer(engine)
	admin := ctxWithToken(adminToken)

	_, err := s.CreateRbacUser(ctxWithToken(viewerToken), &pb.CreateRbacUserRequest{Name: "bob", Role: "operator"})
	assert.Equal(t, codes.PermissionDenied, status.Code(err))

	add, err := s.CreateRbacUser(admin, &pb.CreateRbacUserRequest{Name: "bob", Role: "operator"})
	require.NoError(t, err)
	require.True(t, add.Success, add.Message)
	require.NotEmpty(t, add.Token)
	user, ok := engine.ResolveUser(add.Token)
	require.True(t, ok)
	assert.Equal(t, "bob", user)

	dup, err := s.CreateRbacUser(admin, &pb.CreateRbacUserRequest{Name: "bob", Role: "operator"})
	require.NoError(t, err)
	assert.False(t, dup.Success)
	assert.Contains(t, dup.Message, "already exists")

	set, err := s.SetRbacUserRole(admin, &pb.SetRbacUserRoleRequest{Name: "bob", Role: "viewer"})
	require.NoError(t, err)
	require.True(t, set.Success, set.Message)
	assert.Equal(t, "viewer", engine.RoleOf("bob"))

	del, err := s.DeleteRbacUser(admin, &pb.DeleteRbacUserRequest{Name: "bob"})
	require.NoError(t, err)
	require.True(t, del.Success, del.Message)
	_, ok = engine.ResolveUser(add.Token)
	assert.False(t, ok)

	pinned, err := s.DeleteRbacUser(admin, &pb.DeleteRbacUserRequest{Name: "alice"})
	require.NoError(t, err)
	assert.False(t, pinned.Success, "a config-declared user must not be removable")
}

// Through the real interceptor chain: the mutating RBAC RPCs classify as
// system:write, which only admin holds by default.
func TestRbacMutationsBlockedByInterceptorForNonAdmin(t *testing.T) {
	engine := testEngine(t)
	s := rbacServer(engine)
	identity := rbacIdentityUnaryInterceptor(engine)
	authz := rbacAuthzUnaryInterceptor(engine)

	call := func(token, method string) error {
		info := &grpc.UnaryServerInfo{FullMethod: "/v1.HaifyController/" + method}
		_, err := identity(ctxWithToken(token), &pb.CreateRbacUserRequest{Name: "eve", Role: "admin"}, info,
			func(ctx context.Context, req any) (any, error) {
				return authz(ctx, req, info, func(ctx context.Context, req any) (any, error) {
					return s.CreateRbacUser(ctx, req.(*pb.CreateRbacUserRequest))
				})
			})
		return err
	}

	for _, m := range []string{"CreateRbacUser", "DeleteRbacUser", "SetRbacUserRole"} {
		obj, act := rbac.Classify("/v1.HaifyController/" + m)
		assert.Equal(t, "system", obj, m)
		assert.Equal(t, rbac.ActWrite, act, m)
	}
	assert.Equal(t, codes.PermissionDenied, status.Code(call(viewerToken, "CreateRbacUser")))
	assert.NoError(t, call(adminToken, "CreateRbacUser"))
}
