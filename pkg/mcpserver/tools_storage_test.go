package mcpserver

import (
	"context"
	"testing"

	"go.uber.org/zap"

	sdspb "github.com/haify-project/sds/api/proto/v1"
)

type storageClient struct {
	ControllerClient
	moved string
}

func (c *storageClient) ListPoolDisks(context.Context, string, string) ([]*sdspb.PoolDiskInfo, error) {
	return []*sdspb.PoolDiskInfo{{Node: "n1", Pool: "sds_fast", Device: "/dev/sdb", SizeBytes: 1 << 30, UsedBytes: 1 << 29,
		Health: "fail", HealthDetail: "12 uncorrectable sectors"}}, nil
}

func (c *storageClient) ListStorageJobs(_ context.Context, all bool) ([]*sdspb.StorageJobInfo, error) {
	return []*sdspb.StorageJobInfo{{Id: "j1", Kind: "move-volume", State: "running", StartedUnix: 1790917200}}, nil
}

func (c *storageClient) MoveVolume(_ context.Context, res string, vol int32, pool string) (*sdspb.StorageJobResponse, error) {
	c.moved = res + "/" + pool
	return &sdspb.StorageJobResponse{Success: true, JobId: "j2", Message: "moving"}, nil
}

func TestStorageTools(t *testing.T) {
	mock := &storageClient{}
	session := connect(t, mock, false)

	var disks poolDisksOut
	callJSON(t, session, "sds_pool_disks", map[string]any{}, &disks)
	if len(disks.Disks) != 1 || disks.Disks[0].Health != "fail" || disks.Disks[0].Size != "1.00 GiB" {
		t.Fatalf("disks: %+v", disks)
	}

	var jobs storageJobsOut
	callJSON(t, session, "sds_storage_jobs", map[string]any{}, &jobs)
	if len(jobs.Jobs) != 1 || jobs.Jobs[0].Started != "2026-10-02T05:00:00Z" {
		t.Fatalf("jobs: %+v", jobs)
	}

	var job jobStartOut
	callJSON(t, session, "sds_resource_move_volume", map[string]any{"resource": "db", "volume": 0, "pool": "fast"}, &job)
	if job.JobID != "j2" || mock.moved != "db/fast" {
		t.Fatalf("move: %+v, %q", job, mock.moved)
	}
}

// Moving a volume deletes its old snapshots, so the remote (operate) endpoint
// does not offer it; the disk tools only move data and stay available.
func TestStorageToolTiers(t *testing.T) {
	operate := toolNames(t, New(&storageClient{}, zap.NewNop(), Options{NoDestructive: true}))
	if operate["sds_resource_move_volume"] {
		t.Error("sds_resource_move_volume must not be available to operate")
	}
	for _, name := range []string{"sds_pool_replace_disk", "sds_pool_remove_disk", "sds_pool_trim", "sds_pool_disks"} {
		if !operate[name] {
			t.Errorf("%s must be available to operate", name)
		}
	}
	read := toolNames(t, New(&storageClient{}, zap.NewNop(), Options{ReadOnly: true}))
	if !read["sds_pool_disks"] || !read["sds_storage_jobs"] || read["sds_pool_trim"] {
		t.Errorf("read tier: %v", read)
	}
}
