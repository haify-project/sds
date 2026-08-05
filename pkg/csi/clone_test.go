package csi

import (
	"context"
	"errors"
	"testing"

	"github.com/container-storage-interface/spec/lib/go/csi"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func restoreReq(name, snapshotID string) *csi.CreateVolumeRequest {
	return &csi.CreateVolumeRequest{
		Name:               name,
		CapacityRange:      &csi.CapacityRange{RequiredBytes: 2 << 30},
		Parameters:         map[string]string{"pool": "vg0"},
		VolumeCapabilities: []*csi.VolumeCapability{{AccessMode: &csi.VolumeCapability_AccessMode{Mode: csi.VolumeCapability_AccessMode_SINGLE_NODE_WRITER}}},
		VolumeContentSource: &csi.VolumeContentSource{Type: &csi.VolumeContentSource_Snapshot{
			Snapshot: &csi.VolumeContentSource_SnapshotSource{SnapshotId: snapshotID},
		}},
	}
}

func cloneReq(name, sourceVolumeID string) *csi.CreateVolumeRequest {
	return &csi.CreateVolumeRequest{
		Name:               name,
		CapacityRange:      &csi.CapacityRange{RequiredBytes: 2 << 30},
		Parameters:         map[string]string{"pool": "vg0"},
		VolumeCapabilities: []*csi.VolumeCapability{{AccessMode: &csi.VolumeCapability_AccessMode{Mode: csi.VolumeCapability_AccessMode_SINGLE_NODE_WRITER}}},
		VolumeContentSource: &csi.VolumeContentSource{Type: &csi.VolumeContentSource_Volume{
			Volume: &csi.VolumeContentSource_VolumeSource{VolumeId: sourceVolumeID},
		}},
	}
}

func TestRestoreFromSnapshotPopulatesNewVolume(t *testing.T) {
	b := newFakeBackend("n1", "n2", "n3")
	seedSnapshotSource(b, "pvc_src", "n2", "n3")
	s := newTestController(b)

	// The snapshot lives on n3.
	resp, err := s.CreateVolume(context.Background(), restoreReq("pvc-new", "pvc_src/n3/sdssnap_s1"))
	require.NoError(t, err)

	vol := resp.GetVolume()
	assert.Equal(t, "pvc_new", vol.GetVolumeId())
	// The response must echo the content source back per the CSI spec.
	require.NotNil(t, vol.GetContentSource())
	assert.Equal(t, "pvc_src/n3/sdssnap_s1", vol.GetContentSource().GetSnapshot().GetSnapshotId())

	// The new volume must be placed on the node that holds the snapshot, so the
	// copy can run locally.
	require.Len(t, b.createCalls, 1)
	assert.Contains(t, b.createCalls[0].nodes, "n3")

	// And it must actually have been filled from the snapshot device.
	require.Len(t, b.populated, 1)
	assert.Equal(t, "pvc_new/0<-/dev/sds_vg0/sdssnap_s1@n3", b.populated[0])
}

func TestRestoreRejectsUnknownSnapshot(t *testing.T) {
	b := newFakeBackend("n1")
	s := newTestController(b)
	ctx := context.Background()

	// Malformed id.
	_, err := s.CreateVolume(ctx, restoreReq("pvc-new", "garbage"))
	assert.Equal(t, codes.NotFound, status.Code(err))

	// Well-formed, but the source volume is gone.
	_, err = s.CreateVolume(ctx, restoreReq("pvc-new", "ghost/n1/snap"))
	assert.Equal(t, codes.NotFound, status.Code(err))

	assert.Empty(t, b.createCalls, "no volume may be created for an unresolvable source")
}

// A failed copy must not leave a volume that looks ready but is empty.
func TestRestoreRollsBackWhenPopulateFails(t *testing.T) {
	b := newFakeBackend("n1", "n2")
	seedSnapshotSource(b, "pvc_src", "n1", "n2")
	b.populateErr = errors.New("dd: input/output error")
	s := newTestController(b)

	_, err := s.CreateVolume(context.Background(), restoreReq("pvc-new", "pvc_src/n1/sdssnap_s1"))
	require.Error(t, err)
	assert.Equal(t, codes.Internal, status.Code(err))

	_, gerr := b.GetResource(context.Background(), "pvc_new")
	assert.Error(t, gerr, "the half-written volume must be destroyed, not left behind")
}

func TestCloneSnapshotsSourceForConsistency(t *testing.T) {
	b := newFakeBackend("n1", "n2")
	seedSnapshotSource(b, "pvc_src", "n2")
	s := newTestController(b)

	resp, err := s.CreateVolume(context.Background(), cloneReq("pvc-clone", "pvc_src"))
	require.NoError(t, err)
	assert.Equal(t, "pvc_src", resp.GetVolume().GetContentSource().GetVolume().GetVolumeId())

	// Cloning a possibly-live volume must copy from a snapshot, not from the
	// backing store directly, or the copy could be torn.
	require.Len(t, b.snapCreated, 1, "clone must snapshot the source first")
	require.Len(t, b.populated, 1)
	assert.Contains(t, b.populated[0], "sdssnap_clone", "clone must copy from the temporary snapshot")

	// The temporary snapshot is not left behind.
	assert.Len(t, b.snapDeleted, 1, "the temporary clone snapshot must be cleaned up")
}

func TestCloneRejectsUnknownSource(t *testing.T) {
	b := newFakeBackend("n1")
	s := newTestController(b)

	_, err := s.CreateVolume(context.Background(), cloneReq("pvc-clone", "ghost"))
	assert.Equal(t, codes.NotFound, status.Code(err))
	assert.Empty(t, b.createCalls)
}

// The source node must end up hosting a replica; otherwise the copy has no
// local device to read.
func TestRestoreFailsWhenSourceNodeCannotHostReplica(t *testing.T) {
	b := newFakeBackend("n1", "n2", "n3")
	seedSnapshotSource(b, "pvc_src", "n3")
	// n3 loses its pool, so it cannot host a replica of the restored volume.
	b.onlyPoolOnNodes("n1", "n2")
	s := newTestController(b)

	_, err := s.CreateVolume(context.Background(), restoreReq("pvc-new", "pvc_src/n3/sdssnap_s1"))
	require.Error(t, err)
	assert.Equal(t, codes.ResourceExhausted, status.Code(err))
	assert.Empty(t, b.populated)
}

func TestCreateVolumeWithoutContentSourceDoesNotPopulate(t *testing.T) {
	b := newFakeBackend("n1", "n2")
	s := newTestController(b)

	_, err := s.CreateVolume(context.Background(), validCreateReq("pvc-plain"))
	require.NoError(t, err)
	assert.Empty(t, b.populated, "an ordinary volume must not be copied into")
	assert.Empty(t, b.snapCreated)
}
