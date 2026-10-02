package client

import (
	"context"
	"crypto/tls"
	"errors"
	"net"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/metadata"

	sdspb "github.com/liliang-cn/sds/api/proto/v1"
)

// rbacStub answers the RBAC RPCs the way the controller does, recording the
// bearer token each call carried.
type rbacStub struct {
	sdspb.UnimplementedSDSControllerServer
	enabled bool
	auth    []string
}

func (s *rbacStub) seen(ctx context.Context) {
	md, _ := metadata.FromIncomingContext(ctx)
	s.auth = append(s.auth, md.Get("authorization")...)
}

func (s *rbacStub) GetRbacWhoami(ctx context.Context, _ *sdspb.GetRbacWhoamiRequest) (*sdspb.GetRbacWhoamiResponse, error) {
	s.seen(ctx)
	return &sdspb.GetRbacWhoamiResponse{Enabled: s.enabled, User: "alice", Role: "admin", CanAdmin: true}, nil
}

func (s *rbacStub) CreateRbacUser(ctx context.Context, req *sdspb.CreateRbacUserRequest) (*sdspb.CreateRbacUserResponse, error) {
	s.seen(ctx)
	if !s.enabled {
		return &sdspb.CreateRbacUserResponse{Message: "RBAC is not enabled"}, nil
	}
	if req.Name == "taken" {
		return &sdspb.CreateRbacUserResponse{Enabled: true, Message: `user "taken" already exists`}, nil
	}
	return &sdspb.CreateRbacUserResponse{Success: true, Enabled: true, Token: "generated-" + req.Name}, nil
}

func (s *rbacStub) DeleteRbacUser(ctx context.Context, _ *sdspb.DeleteRbacUserRequest) (*sdspb.DeleteRbacUserResponse, error) {
	s.seen(ctx)
	return &sdspb.DeleteRbacUserResponse{Success: s.enabled, Enabled: s.enabled}, nil
}

func startRBACStub(t *testing.T, p *clientTestPKI, stub *rbacStub) string {
	t.Helper()
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	srv := grpc.NewServer(grpc.Creds(credentials.NewTLS(&tls.Config{
		Certificates: []tls.Certificate{p.server},
		MinVersion:   tls.VersionTLS12,
	})))
	sdspb.RegisterSDSControllerServer(srv, stub)
	go func() { _ = srv.Serve(lis) }()
	t.Cleanup(srv.Stop)
	return lis.Addr().String()
}

// The RBAC calls travel the same TLS connection, with the same token, as every
// other RPC; they used to go to the REST port in plain HTTP.
func TestRBACCallsOverTLS(t *testing.T) {
	pki := newClientTestPKI(t)
	stub := &rbacStub{enabled: true}
	addr := startRBACStub(t, pki, stub)

	c, err := NewSDSClient(addr, WithToken("admin-token"),
		WithTLS(TLSOptions{CACert: pki.caFile, ServerName: "sds-controller.test"}))
	require.NoError(t, err)
	defer func() { _ = c.Close() }()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	who, err := c.RbacWhoami(ctx)
	require.NoError(t, err)
	assert.Equal(t, "alice", who.User)

	token, err := c.CreateRbacUser(ctx, "bob", "viewer", "")
	require.NoError(t, err)
	assert.Equal(t, "generated-bob", token)

	_, err = c.CreateRbacUser(ctx, "taken", "viewer", "")
	assert.EqualError(t, err, `user "taken" already exists`)

	assert.Equal(t, []string{"Bearer admin-token", "Bearer admin-token", "Bearer admin-token"}, stub.auth)
}

func TestRBACCallsReportDisabled(t *testing.T) {
	pki := newClientTestPKI(t)
	addr := startRBACStub(t, pki, &rbacStub{enabled: false})

	c, err := NewSDSClient(addr, WithTLS(TLSOptions{CACert: pki.caFile, ServerName: "sds-controller.test"}))
	require.NoError(t, err)
	defer func() { _ = c.Close() }()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	_, err = c.CreateRbacUser(ctx, "bob", "viewer", "")
	assert.True(t, errors.Is(err, ErrRBACDisabled), "%v", err)
	assert.True(t, errors.Is(c.DeleteRbacUser(ctx, "bob"), ErrRBACDisabled))
}
