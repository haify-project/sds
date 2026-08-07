package client

import (
	"context"
	"net"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"

	sdspb "github.com/liliang-cn/sds/api/proto/v1"
)

// backupMockServer answers the backup RPCs. It is separate from mockServer so
// the failure half (a Success=false response) can be exercised without
// disturbing the shared fixture.
type backupMockServer struct {
	sdspb.UnimplementedSDSControllerServer
	fail bool
}

func (m *backupMockServer) AddBackupTarget(ctx context.Context, req *sdspb.AddBackupTargetRequest) (*sdspb.AddBackupTargetResponse, error) {
	if m.fail {
		return &sdspb.AddBackupTargetResponse{Success: false, Message: "target rejected"}, nil
	}
	return &sdspb.AddBackupTargetResponse{Success: true}, nil
}

func (m *backupMockServer) ListBackupTargets(ctx context.Context, req *sdspb.ListBackupTargetsRequest) (*sdspb.ListBackupTargetsResponse, error) {
	return &sdspb.ListBackupTargetsResponse{
		Success: true,
		Targets: []*sdspb.BackupTargetInfo{{Name: "offsite", Kind: "s3", User: "AKIA"}},
	}, nil
}

func (m *backupMockServer) DeleteBackupTarget(ctx context.Context, req *sdspb.DeleteBackupTargetRequest) (*sdspb.DeleteBackupTargetResponse, error) {
	return &sdspb.DeleteBackupTargetResponse{Success: true}, nil
}

func (m *backupMockServer) CreateBackup(ctx context.Context, req *sdspb.CreateBackupRequest) (*sdspb.CreateBackupResponse, error) {
	if m.fail {
		return &sdspb.CreateBackupResponse{Success: false, Message: "uploaded short"}, nil
	}
	return &sdspb.CreateBackupResponse{
		Success: true,
		Backup:  &sdspb.BackupInfo{Id: "data_1", Resource: req.Resource, State: "completed"},
	}, nil
}

func (m *backupMockServer) ListBackups(ctx context.Context, req *sdspb.ListBackupsRequest) (*sdspb.ListBackupsResponse, error) {
	return &sdspb.ListBackupsResponse{
		Success: true,
		Backups: []*sdspb.BackupInfo{{Id: "data_1", State: "completed"}},
	}, nil
}

func (m *backupMockServer) RestoreBackup(ctx context.Context, req *sdspb.RestoreBackupRequest) (*sdspb.RestoreBackupResponse, error) {
	if m.fail {
		return &sdspb.RestoreBackupResponse{Success: false, Message: "resource is in use"}, nil
	}
	return &sdspb.RestoreBackupResponse{Success: true, Backup: &sdspb.BackupInfo{Id: req.Id}}, nil
}

func (m *backupMockServer) DeleteBackup(ctx context.Context, req *sdspb.DeleteBackupRequest) (*sdspb.DeleteBackupResponse, error) {
	return &sdspb.DeleteBackupResponse{Success: true}, nil
}

func startBackupServer(t *testing.T, fail bool) *SDSClient {
	t.Helper()
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)

	srv := grpc.NewServer()
	sdspb.RegisterSDSControllerServer(srv, &backupMockServer{fail: fail})
	go func() { _ = srv.Serve(lis) }()
	t.Cleanup(srv.Stop)

	c, err := NewSDSClient(lis.Addr().String())
	require.NoError(t, err)
	t.Cleanup(func() { _ = c.Close() })
	return c
}

func TestBackupClientHappyPath(t *testing.T) {
	c := startBackupServer(t, false)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	require.NoError(t, c.AddBackupTarget(ctx, &sdspb.AddBackupTargetRequest{Name: "offsite", Kind: "s3"}))

	targets, err := c.ListBackupTargets(ctx)
	require.NoError(t, err)
	require.Len(t, targets, 1)
	assert.Equal(t, "offsite", targets[0].GetName())

	info, err := c.CreateBackup(ctx, "data", "offsite", "")
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

	err := c.AddBackupTarget(ctx, &sdspb.AddBackupTargetRequest{Name: "x", Kind: "s3"})
	require.EqualError(t, err, "target rejected")

	_, err = c.CreateBackup(ctx, "data", "offsite", "")
	require.EqualError(t, err, "uploaded short")

	_, err = c.RestoreBackup(ctx, "data_1", "data", "")
	require.EqualError(t, err, "resource is in use")
}
