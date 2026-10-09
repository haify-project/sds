package client

import (
	"context"
	"net"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"

	haifypb "github.com/haify-project/haify/api/proto/v1"
)

// backupMockServer answers the backup RPCs. It is separate from mockServer so
// the failure half (a Success=false response) can be exercised without
// disturbing the shared fixture.
type backupMockServer struct {
	haifypb.UnimplementedHaifyControllerServer
	fail bool
}

func (m *backupMockServer) AddBackupTarget(ctx context.Context, req *haifypb.AddBackupTargetRequest) (*haifypb.AddBackupTargetResponse, error) {
	if m.fail {
		return &haifypb.AddBackupTargetResponse{Success: false, Message: "target rejected"}, nil
	}
	return &haifypb.AddBackupTargetResponse{Success: true}, nil
}

func (m *backupMockServer) ListBackupTargets(ctx context.Context, req *haifypb.ListBackupTargetsRequest) (*haifypb.ListBackupTargetsResponse, error) {
	return &haifypb.ListBackupTargetsResponse{
		Success: true,
		Targets: []*haifypb.BackupTargetInfo{{Name: "offsite", Kind: "s3", User: "AKIA"}},
	}, nil
}

func (m *backupMockServer) DeleteBackupTarget(ctx context.Context, req *haifypb.DeleteBackupTargetRequest) (*haifypb.DeleteBackupTargetResponse, error) {
	return &haifypb.DeleteBackupTargetResponse{Success: true}, nil
}

func (m *backupMockServer) CreateBackup(ctx context.Context, req *haifypb.CreateBackupRequest) (*haifypb.CreateBackupResponse, error) {
	if m.fail {
		return &haifypb.CreateBackupResponse{Success: false, Message: "uploaded short"}, nil
	}
	return &haifypb.CreateBackupResponse{
		Success: true,
		Backup:  &haifypb.BackupInfo{Id: "data_1", Resource: req.Resource, State: "completed"},
	}, nil
}

func (m *backupMockServer) ListBackups(ctx context.Context, req *haifypb.ListBackupsRequest) (*haifypb.ListBackupsResponse, error) {
	return &haifypb.ListBackupsResponse{
		Success: true,
		Backups: []*haifypb.BackupInfo{{Id: "data_1", State: "completed"}},
	}, nil
}

func (m *backupMockServer) RestoreBackup(ctx context.Context, req *haifypb.RestoreBackupRequest) (*haifypb.RestoreBackupResponse, error) {
	if m.fail {
		return &haifypb.RestoreBackupResponse{Success: false, Message: "resource is in use"}, nil
	}
	return &haifypb.RestoreBackupResponse{Success: true, Backup: &haifypb.BackupInfo{Id: req.Id}}, nil
}

func (m *backupMockServer) DeleteBackup(ctx context.Context, req *haifypb.DeleteBackupRequest) (*haifypb.DeleteBackupResponse, error) {
	return &haifypb.DeleteBackupResponse{Success: true}, nil
}

func startBackupServer(t *testing.T, fail bool) *HaifyClient {
	t.Helper()
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)

	srv := grpc.NewServer()
	haifypb.RegisterHaifyControllerServer(srv, &backupMockServer{fail: fail})
	go func() { _ = srv.Serve(lis) }()
	t.Cleanup(srv.Stop)

	c, err := NewHaifyClient(lis.Addr().String())
	require.NoError(t, err)
	t.Cleanup(func() { _ = c.Close() })
	return c
}

func TestBackupClientHappyPath(t *testing.T) {
	c := startBackupServer(t, false)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	require.NoError(t, c.AddBackupTarget(ctx, &haifypb.AddBackupTargetRequest{Name: "offsite", Kind: "s3"}))

	targets, err := c.ListBackupTargets(ctx)
	require.NoError(t, err)
	require.Len(t, targets, 1)
	assert.Equal(t, "offsite", targets[0].GetName())

	info, err := c.CreateBackup(ctx, "data", "offsite", "", false)
	require.NoError(t, err)
	assert.Equal(t, "completed", info.GetState())

	backups, err := c.ListBackups(ctx, "data", "")
	require.NoError(t, err)
	assert.Len(t, backups, 1)

	restored, err := c.RestoreBackup(ctx, "data_1", "data", "")
	require.NoError(t, err)
	assert.Equal(t, "data_1", restored.GetId())

	require.NoError(t, c.DeleteBackup(ctx, "data_1", "", false))
	require.NoError(t, c.DeleteBackupTarget(ctx, "offsite", false))
}

// The controller reports refusals as Success=false with a reason rather than a
// gRPC error; the client has to turn that into a Go error, or a failed backup
// looks like a successful one to every caller.
func TestBackupClientSurfacesServerRefusals(t *testing.T) {
	c := startBackupServer(t, true)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	err := c.AddBackupTarget(ctx, &haifypb.AddBackupTargetRequest{Name: "x", Kind: "s3"})
	require.EqualError(t, err, "target rejected")

	_, err = c.CreateBackup(ctx, "data", "offsite", "", false)
	require.EqualError(t, err, "uploaded short")

	_, err = c.RestoreBackup(ctx, "data_1", "data", "")
	require.EqualError(t, err, "resource is in use")
}
