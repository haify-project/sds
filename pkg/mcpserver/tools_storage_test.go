package mcpserver

import (
	"context"
	"testing"

	"go.uber.org/zap"

	haifypb "github.com/haify-project/haify/api/proto/v1"
)

type storageClient struct {
	ControllerClient
	moved string
}

func (c *storageClient) ListPoolDisks(context.Context, string, string) ([]*haifypb.PoolDiskInfo, error) {
	return []*haifypb.PoolDiskInfo{{Node: "n1", Pool: "haify_fast", Device: "/dev/sdb", SizeBytes: 1 << 30, UsedBytes: 1 << 29,
		Health: "fail", HealthDetail: "12 uncorrectable sectors"}}, nil
}

func (c *storageClient) ListStorageJobs(_ context.Context, all bool) ([]*haifypb.StorageJobInfo, error) {
	return []*haifypb.StorageJobInfo{{Id: "j1", Kind: "move-volume", State: "running", StartedUnix: 1790917200}}, nil
}

func (c *storageClient) MoveVolume(_ context.Context, res string, vol int32, pool string) (*haifypb.StorageJobResponse, error) {
	c.moved = res + "/" + pool
	return &haifypb.StorageJobResponse{Success: true, JobId: "j2", Message: "moving"}, nil
}

func TestStorageTools(t *testing.T) {
	mock := &storageClient{}
	session := connect(t, mock, false)

	var disks poolDisksOut
	callJSON(t, session, "haify_pool_disks", map[string]any{}, &disks)
	if len(disks.Disks) != 1 || disks.Disks[0].Health != "fail" || disks.Disks[0].Size != "1.00 GiB" {
		t.Fatalf("disks: %+v", disks)
	}

	var jobs storageJobsOut
	callJSON(t, session, "haify_storage_jobs", map[string]any{}, &jobs)
	if len(jobs.Jobs) != 1 || jobs.Jobs[0].Started != "2026-10-02T05:00:00Z" {
		t.Fatalf("jobs: %+v", jobs)
	}

	var job jobStartOut
	callJSON(t, session, "haify_resource_move_volume", map[string]any{"resource": "db", "volume": 0, "pool": "fast"}, &job)
	if job.JobID != "j2" || mock.moved != "db/fast" {
		t.Fatalf("move: %+v, %q", job, mock.moved)
	}
}

// Moving a volume deletes its old snapshots, so the remote (operate) endpoint
// does not offer it; the disk tools only move data and stay available.
func TestStorageToolTiers(t *testing.T) {
	operate := toolNames(t, New(&storageClient{}, zap.NewNop(), Options{NoDestructive: true}))
	if operate["haify_resource_move_volume"] {
		t.Error("haify_resource_move_volume must not be available to operate")
	}
	for _, name := range []string{"haify_pool_replace_disk", "haify_pool_remove_disk", "haify_pool_trim", "haify_pool_disks"} {
		if !operate[name] {
			t.Errorf("%s must be available to operate", name)
		}
	}
	read := toolNames(t, New(&storageClient{}, zap.NewNop(), Options{ReadOnly: true}))
	if !read["haify_pool_disks"] || !read["haify_storage_jobs"] || read["haify_pool_trim"] {
		t.Errorf("read tier: %v", read)
	}
}
