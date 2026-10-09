package controller

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	haifypb "github.com/haify-project/haify/api/proto/v1"
	"github.com/haify-project/haify/pkg/config"
	"github.com/haify-project/haify/pkg/event"
	"github.com/haify-project/haify/pkg/rbac"
)

func approvalFixture(t *testing.T) (*Server, *rbac.Engine) {
	t.Helper()
	engine, err := rbac.New(nil, []rbac.User{
		{Name: "alice", Token: "alice-token-0123456789", Role: "admin"},
		{Name: "bob", Token: "bob-token-0123456789", Role: "security-officer"},
		{Name: "olga", Token: "olga-token-0123456789", Role: "operator"},
	}, nil)
	require.NoError(t, err)
	ctrl := newBasicTestController(&fakeDeploymentClient{})
	ctrl.db = newTestDB(t)
	ctrl.rbac = engine
	ctrl.events = event.NewBus(10)
	ctrl.approvals = newApprovalGate(config.ApprovalConfig{Enabled: true, TTLMinutes: 60}, ctrl.db, ctrl.events, zap.NewNop())
	return &Server{ctrl: ctrl, resources: ctrl.resources}, engine
}

func as(user string) context.Context {
	return context.WithValue(context.Background(), ctxUserKey{}, user)
}

// callThrough runs one call the way the server does: approval interceptor,
// then a handler that records that it ran.
func callThrough(g *approvalGate, ctx context.Context, method string, req any, ran *int) error {
	_, err := approvalUnaryInterceptor(g)(ctx, req, &grpc.UnaryServerInfo{FullMethod: "/v1.HaifyController/" + method},
		func(context.Context, any) (any, error) { *ran++; return nil, nil })
	return err
}

func pendingID(t *testing.T, err error) string {
	t.Helper()
	require.Equal(t, codes.FailedPrecondition, status.Code(err), "%v", err)
	msg := status.Convert(err).Message()
	i := strings.Index(msg, "request ")
	require.GreaterOrEqual(t, i, 0, msg)
	return strings.Fields(msg[i+len("request "):])[0]
}

// One stolen admin token cannot delete a backup target: the call waits for a
// different user's approval, runs once when repeated, and only as approved.
func TestTwoPersonApproval(t *testing.T) {
	srv, _ := approvalFixture(t)
	g := srv.ctrl.approvals
	ran := 0
	del := &haifypb.DeleteBackupTargetRequest{Name: "offsite"}

	id := pendingID(t, callThrough(g, as("alice"), "DeleteBackupTarget", del, &ran))
	assert.Zero(t, ran)
	assert.Equal(t, id, pendingID(t, callThrough(g, as("alice"), "DeleteBackupTarget", del, &ran)),
		"repeating it before approval finds the same request")
	assert.Equal(t, event.TypeApprovalRequested, srv.ctrl.events.Recent(event.Filter{}, 0, 10)[0].Type)

	resp, err := srv.ApproveRequest(as("alice"), &haifypb.ApproveRequestRequest{Id: id})
	require.NoError(t, err)
	assert.False(t, resp.Success)
	assert.Contains(t, resp.Message, "cannot be approved by the user who made it")
	_, err = srv.ApproveRequest(as("olga"), &haifypb.ApproveRequestRequest{Id: id})
	assert.Equal(t, codes.PermissionDenied, status.Code(err), "an operator has no approve right")

	resp, err = srv.ApproveRequest(as("bob"), &haifypb.ApproveRequestRequest{Id: id})
	require.NoError(t, err)
	require.True(t, resp.Success, resp.Message)

	// The approval is for that exact call, by that caller.
	assert.Error(t, callThrough(g, as("alice"), "DeleteBackupTarget", &haifypb.DeleteBackupTargetRequest{Name: "other"}, &ran))
	assert.Error(t, callThrough(g, as("bob"), "DeleteBackupTarget", del, &ran))
	assert.Zero(t, ran)
	require.NoError(t, callThrough(g, as("alice"), "DeleteBackupTarget", del, &ran))
	assert.Equal(t, 1, ran)
	assert.Error(t, callThrough(g, as("alice"), "DeleteBackupTarget", del, &ran), "and it runs once")
	assert.Equal(t, 1, ran)

	// Calls off the list are untouched.
	require.NoError(t, callThrough(g, as("olga"), "CreateBackup", &haifypb.CreateBackupRequest{Resource: "r"}, &ran))
}

func TestApprovalExpiresAndRejects(t *testing.T) {
	srv, _ := approvalFixture(t)
	g := srv.ctrl.approvals
	ran := 0
	req := &haifypb.DeleteBackupRequest{Id: "b1"}
	id := pendingID(t, callThrough(g, as("alice"), "DeleteBackup", req, &ran))
	resp, err := srv.RejectRequest(as("bob"), &haifypb.RejectRequestRequest{Id: id})
	require.NoError(t, err)
	require.True(t, resp.Success)
	newID := pendingID(t, callThrough(g, as("alice"), "DeleteBackup", req, &ran))
	assert.NotEqual(t, id, newID, "a rejected request is closed; asking again opens a new one")

	g.now = func() time.Time { return time.Now().Add(2 * time.Hour) }
	resp2, err := srv.ApproveRequest(as("bob"), &haifypb.ApproveRequestRequest{Id: newID})
	require.NoError(t, err)
	assert.False(t, resp2.Success)
	assert.Contains(t, resp2.Message, "expired")

	list, err := srv.ListApprovals(as("bob"), &haifypb.ListApprovalsRequest{IncludeClosed: true})
	require.NoError(t, err)
	assert.Len(t, list.Approvals, 2)
	open, _ := srv.ListApprovals(as("bob"), &haifypb.ListApprovalsRequest{})
	assert.Empty(t, open.Approvals)
}

// The REST user routes call the RBAC engine directly; they are held back too,
// or they would be the way to mint a second approver.
func TestApprovalHoldsBackRESTUserChanges(t *testing.T) {
	srv, engine := approvalFixture(t)
	h := rbacCreateUserHandler(engine, srv.ctrl.approvals)
	w, body := doReq(h, http.MethodPost, "alice-token-0123456789", `{"name":"mallory","role":"admin"}`, nil)
	assert.Equal(t, http.StatusPreconditionFailed, w.Code)
	assert.Contains(t, body["error"], "needs a second person's approval")
	assert.Equal(t, "", engine.RoleOf("mallory"))
}

func TestMaskedRequestHidesSecrets(t *testing.T) {
	out := maskedRequest(&haifypb.AddBackupTargetRequest{Name: "t", Secret: "s3cr3t", User: "k"})
	assert.NotContains(t, out, "s3cr3t")
	assert.Contains(t, out, `"name":"t"`)
}

// A method on the default list that the API does not have would leave a
// destructive call unguarded without anyone noticing.
func TestDefaultApprovalMethodsExist(t *testing.T) {
	methods := map[string]bool{}
	for _, m := range haifypb.HaifyController_ServiceDesc.Methods {
		methods[m.MethodName] = true
	}
	for _, m := range config.DefaultApprovalMethods {
		assert.True(t, methods[m], m)
	}
}
