package csi

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/container-storage-interface/spec/lib/go/csi"
	sdspb "github.com/haify-project/sds/api/proto/v1"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// seedSnapshotSource registers a resource whose volume 0 has a backing volume,
// which is what CreateSnapshot needs to locate the LV to snapshot.
func seedSnapshotSource(b *fakeBackend, name string, nodes ...string) {
	b.resources[name] = &sdspb.ResourceInfo{
		Name:  name,
		Nodes: nodes,
		Volumes: []*sdspb.VolumeInfo{{
			VolumeId:      0,
			SizeGb:        2,
			Pool:          "sds_vg0",
			BackingVolume: name + "_data",
		}},
	}
}

func TestSnapshotIDRoundTrip(t *testing.T) {
	id := makeSnapshotID("pvc_abc", "n1", "snap1")
	assert.Equal(t, "pvc_abc/n1/snap1", id)

	res, node, snap, err := parseSnapshotID(id)
	require.NoError(t, err)
	assert.Equal(t, "pvc_abc", res)
	assert.Equal(t, "n1", node)
	assert.Equal(t, "snap1", snap)

	for _, bad := range []string{"", "only-one", "a/b", "a//c", "/b/c", "a/b/"} {
		_, _, _, err := parseSnapshotID(bad)
		assert.Errorf(t, err, "%q should be rejected", bad)
	}
}

func TestCreateSnapshotTakesSnapshotOnReplicaNode(t *testing.T) {
	b := newFakeBackend("n1", "n2")
	seedSnapshotSource(b, "pvc_abc", "n1", "n2")
	s := newTestController(b)

	resp, err := s.CreateSnapshot(context.Background(), &csi.CreateSnapshotRequest{
		Name:           "snapshot-xyz",
		SourceVolumeId: "pvc_abc",
	})
	require.NoError(t, err)

	snap := resp.GetSnapshot()
	assert.Equal(t, "pvc_abc", snap.GetSourceVolumeId())
	assert.True(t, snap.GetReadyToUse())
	assert.Equal(t, int64(2)*giB, snap.GetSizeBytes(), "restore size is the source volume size")
	assert.NotNil(t, snap.GetCreationTime())

	// The ID must carry the node, so a later delete targets the right replica.
	assert.Equal(t, "pvc_abc/n1/sdssnap_snapshot_xyz", snap.GetSnapshotId())

	// The backend was asked to snapshot the backing LV on a diskful node.
	require.Len(t, b.snapCreated, 1)
	assert.Equal(t, "sds_vg0/pvc_abc_data/sdssnap_snapshot_xyz@n1", b.snapCreated[0])
}

// Regression: LVM refuses any LV whose name starts with "snapshot"
// ("Names starting \"snapshot\" are reserved"), and Kubernetes names every
// VolumeSnapshot "snapshot-<uuid>". Found on a real cluster: every CreateSnapshot
// failed on the node until the name was prefixed.
func TestSnapshotNameAvoidsLVMReservedPrefix(t *testing.T) {
	for _, in := range []string{"snapshot-55074c86-2484", "snapshot", "SNAPSHOT-x"} {
		got := sanitizeSnapshotName(in)
		assert.Falsef(t, strings.HasPrefix(strings.ToLower(got), "snapshot"),
			"sanitizeSnapshotName(%q) = %q must not start with LVM's reserved \"snapshot\"", in, got)
	}

	b := newFakeBackend("n1")
	seedSnapshotSource(b, "pvc_abc", "n1")
	s := newTestController(b)

	// The name that reaches the backend (and thus lvcreate) must be safe.
	_, err := s.CreateSnapshot(context.Background(), &csi.CreateSnapshotRequest{
		Name: "snapshot-55074c86-2484-4228-bd90-37aa31ae54dd", SourceVolumeId: "pvc_abc",
	})
	require.NoError(t, err)
	require.Len(t, b.snapCreated, 1)
	sent := b.snapCreated[0]
	name := sent[strings.LastIndex(sent, "/")+1 : strings.LastIndex(sent, "@")]
	assert.False(t, strings.HasPrefix(name, "snapshot"),
		"name sent to lvcreate (%q) must not start with the reserved word", name)
}

func TestCreateSnapshotIsIdempotent(t *testing.T) {
	b := newFakeBackend("n1")
	seedSnapshotSource(b, "pvc_abc", "n1")
	s := newTestController(b)

	req := &csi.CreateSnapshotRequest{Name: "snapshot-xyz", SourceVolumeId: "pvc_abc"}
	first, err := s.CreateSnapshot(context.Background(), req)
	require.NoError(t, err)
	second, err := s.CreateSnapshot(context.Background(), req)
	require.NoError(t, err)

	assert.Equal(t, first.GetSnapshot().GetSnapshotId(), second.GetSnapshot().GetSnapshotId())
	assert.Len(t, b.snapCreated, 1, "the second call must not create a second snapshot")
}

func TestCreateSnapshotValidation(t *testing.T) {
	b := newFakeBackend("n1")
	seedSnapshotSource(b, "pvc_abc", "n1")
	s := newTestController(b)
	ctx := context.Background()

	_, err := s.CreateSnapshot(ctx, &csi.CreateSnapshotRequest{SourceVolumeId: "pvc_abc"})
	assert.Equal(t, codes.InvalidArgument, status.Code(err), "missing name")

	_, err = s.CreateSnapshot(ctx, &csi.CreateSnapshotRequest{Name: "s"})
	assert.Equal(t, codes.InvalidArgument, status.Code(err), "missing source")

	_, err = s.CreateSnapshot(ctx, &csi.CreateSnapshotRequest{Name: "s", SourceVolumeId: "ghost"})
	assert.Equal(t, codes.NotFound, status.Code(err), "unknown source volume")
}

func TestCreateSnapshotRejectsSourceWithoutBackingVolume(t *testing.T) {
	b := newFakeBackend("n1")
	// A resource with no recorded backing volume cannot be snapshotted.
	b.resources["pvc_bad"] = &sdspb.ResourceInfo{
		Name:    "pvc_bad",
		Nodes:   []string{"n1"},
		Volumes: []*sdspb.VolumeInfo{{VolumeId: 0, SizeGb: 1}},
	}
	s := newTestController(b)

	_, err := s.CreateSnapshot(context.Background(), &csi.CreateSnapshotRequest{
		Name: "snap", SourceVolumeId: "pvc_bad",
	})
	assert.Equal(t, codes.FailedPrecondition, status.Code(err))
	assert.Empty(t, b.snapCreated)
}

func TestCreateSnapshotSurfacesBackendFailure(t *testing.T) {
	b := newFakeBackend("n1")
	seedSnapshotSource(b, "pvc_abc", "n1")
	b.snapCreateErr = errors.New("lvcreate: insufficient free space")
	s := newTestController(b)

	_, err := s.CreateSnapshot(context.Background(), &csi.CreateSnapshotRequest{
		Name: "snap", SourceVolumeId: "pvc_abc",
	})
	require.Error(t, err)
	assert.Equal(t, codes.Internal, status.Code(err))
	assert.Contains(t, err.Error(), "insufficient free space")
}

func TestDeleteSnapshotRemovesFromRecordedNode(t *testing.T) {
	b := newFakeBackend("n1", "n2")
	seedSnapshotSource(b, "pvc_abc", "n1", "n2")
	s := newTestController(b)

	created, err := s.CreateSnapshot(context.Background(), &csi.CreateSnapshotRequest{
		Name: "snapshot-xyz", SourceVolumeId: "pvc_abc",
	})
	require.NoError(t, err)

	_, err = s.DeleteSnapshot(context.Background(), &csi.DeleteSnapshotRequest{
		SnapshotId: created.GetSnapshot().GetSnapshotId(),
	})
	require.NoError(t, err)
	require.Len(t, b.snapDeleted, 1)
	assert.Equal(t, "sds_vg0/pvc_abc_data/sdssnap_snapshot_xyz@n1", b.snapDeleted[0])
}

func TestDeleteSnapshotIsIdempotent(t *testing.T) {
	b := newFakeBackend("n1")
	seedSnapshotSource(b, "pvc_abc", "n1")
	s := newTestController(b)
	ctx := context.Background()

	// Malformed id -> success, nothing attempted.
	_, err := s.DeleteSnapshot(ctx, &csi.DeleteSnapshotRequest{SnapshotId: "not-an-id"})
	require.NoError(t, err)
	assert.Empty(t, b.snapDeleted)

	// Source volume already gone -> success, nothing attempted.
	_, err = s.DeleteSnapshot(ctx, &csi.DeleteSnapshotRequest{SnapshotId: "ghost/n1/snap"})
	require.NoError(t, err)
	assert.Empty(t, b.snapDeleted)

	// Missing id is still an error (the CO must always supply one).
	_, err = s.DeleteSnapshot(ctx, &csi.DeleteSnapshotRequest{})
	assert.Equal(t, codes.InvalidArgument, status.Code(err))
}

func TestDeleteSnapshotSurfacesBackendFailure(t *testing.T) {
	b := newFakeBackend("n1")
	seedSnapshotSource(b, "pvc_abc", "n1")
	b.snapDeleteErr = errors.New("lvremove: device busy")
	s := newTestController(b)

	_, err := s.DeleteSnapshot(context.Background(), &csi.DeleteSnapshotRequest{
		SnapshotId: "pvc_abc/n1/snap1",
	})
	require.Error(t, err)
	assert.Equal(t, codes.Internal, status.Code(err))
}
