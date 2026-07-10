package csi

import (
	"context"
	"testing"

	"github.com/container-storage-interface/spec/lib/go/csi"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

func mountReq(volumeID, staging string, ctx map[string]string) *csi.NodeStageVolumeRequest {
	return &csi.NodeStageVolumeRequest{
		VolumeId:          volumeID,
		StagingTargetPath: staging,
		VolumeContext:     ctx,
		VolumeCapability: &csi.VolumeCapability{AccessType: &csi.VolumeCapability_Mount{
			Mount: &csi.VolumeCapability_MountVolume{FsType: "ext4"}}},
	}
}

// A Pod on a non-replica node with remote access enabled must trigger a
// diskless-client attach before the promote.
func TestNodeStageAttachesDisklessClientOffReplica(t *testing.T) {
	b := newFakeBackend("n1", "n2", "n3")
	// Resource lives on n2+n3; the node under test (n1) holds no replica.
	require.NoError(t, b.CreateResourceWithPoolAndType(context.Background(), "pvc_x", 0, []string{"n2", "n3"}, "C", 1, "vg0", "lvm", nil))
	m := newRecordingMounter()

	_, err := newTestNode(b, m).NodeStageVolume(context.Background(),
		mountReq("pvc_x", "/stage/pvc_x", map[string]string{paramAllowRemoteVolumeAccess: "true"}))
	require.NoError(t, err)

	assert.Equal(t, []string{"pvc_x/n1"}, b.attachCalls, "must attach a diskless client for the off-replica node")
	assert.Equal(t, []string{"pvc_x"}, b.promoteCalls, "must still promote after attaching")
	assert.Equal(t, "n1", b.primary["pvc_x"])
}

// A Pod on a non-replica node WITHOUT remote access must be rejected, and must
// not attach anything or format the volume.
func TestNodeStageRejectsOffReplicaWithoutRemoteAccess(t *testing.T) {
	b := newFakeBackend("n1", "n2", "n3")
	require.NoError(t, b.CreateResourceWithPoolAndType(context.Background(), "pvc_x", 0, []string{"n2", "n3"}, "C", 1, "vg0", "lvm", nil))
	m := newRecordingMounter()

	_, err := newTestNode(b, m).NodeStageVolume(context.Background(),
		mountReq("pvc_x", "/stage/pvc_x", nil))
	require.Error(t, err)
	assert.Empty(t, b.attachCalls, "must not attach when remote access is off")
	assert.Empty(t, b.promoteCalls, "must not promote when the node is not allowed")
	assert.Empty(t, m.formatted)
}

// A replica node must never attach a diskless client — it already holds data.
func TestNodeStageOnReplicaSkipsDisklessAttach(t *testing.T) {
	b := newFakeBackend("n1", "n2")
	require.NoError(t, b.CreateResourceWithPoolAndType(context.Background(), "pvc_x", 0, []string{"n1", "n2"}, "C", 1, "vg0", "lvm", nil))
	m := newRecordingMounter()

	_, err := newTestNode(b, m).NodeStageVolume(context.Background(),
		mountReq("pvc_x", "/stage/pvc_x", map[string]string{paramAllowRemoteVolumeAccess: "true"}))
	require.NoError(t, err)
	assert.Empty(t, b.attachCalls, "a replica node must not attach a diskless client")
}

// Unstaging on a non-replica node must detach the diskless client it added.
func TestNodeUnstageDetachesDisklessClient(t *testing.T) {
	b := newFakeBackend("n1", "n2", "n3")
	require.NoError(t, b.CreateResourceWithPoolAndType(context.Background(), "pvc_x", 0, []string{"n2", "n3"}, "C", 1, "vg0", "lvm", nil))
	b.primary["pvc_x"] = "n1"
	m := newRecordingMounter()
	m.mounted["/stage/pvc_x"] = true

	_, err := newTestNode(b, m).NodeUnstageVolume(context.Background(),
		&csi.NodeUnstageVolumeRequest{VolumeId: "pvc_x", StagingTargetPath: "/stage/pvc_x"})
	require.NoError(t, err)
	assert.Equal(t, []string{"pvc_x/n1"}, b.detachCalls, "off-replica unstage must detach the diskless client")
}

// Unstaging on a replica node must NOT detach (it is a real replica).
func TestNodeUnstageOnReplicaKeepsReplica(t *testing.T) {
	b := newFakeBackend("n1", "n2")
	require.NoError(t, b.CreateResourceWithPoolAndType(context.Background(), "pvc_x", 0, []string{"n1", "n2"}, "C", 1, "vg0", "lvm", nil))
	b.primary["pvc_x"] = "n1"
	m := newRecordingMounter()
	m.mounted["/stage/pvc_x"] = true

	_, err := newTestNode(b, m).NodeUnstageVolume(context.Background(),
		&csi.NodeUnstageVolumeRequest{VolumeId: "pvc_x", StagingTargetPath: "/stage/pvc_x"})
	require.NoError(t, err)
	assert.Empty(t, b.detachCalls, "a replica node must not be detached on unstage")
}

// CreateVolume with remote access enabled must leave topology unconstrained and
// stamp the flag into the volume context so the node service can read it back.
func TestCreateVolumeRemoteAccessRelaxesTopology(t *testing.T) {
	b := newFakeBackend("n1", "n2", "n3")
	s := NewControllerServer(b, zap.NewNop())
	resp, err := s.CreateVolume(context.Background(), &csi.CreateVolumeRequest{
		Name:               "pvc_x",
		VolumeCapabilities: []*csi.VolumeCapability{{AccessMode: &csi.VolumeCapability_AccessMode{Mode: csi.VolumeCapability_AccessMode_SINGLE_NODE_WRITER}}},
		CapacityRange:      &csi.CapacityRange{RequiredBytes: giB},
		Parameters:         map[string]string{"pool": "vg0", "replicas": "2", paramAllowRemoteVolumeAccess: "true"},
	})
	require.NoError(t, err)
	assert.Empty(t, resp.Volume.AccessibleTopology, "remote-access volume must be schedulable anywhere")
	assert.Equal(t, "true", resp.Volume.VolumeContext[paramAllowRemoteVolumeAccess])
}

// The default (no remote access) still pins the volume to its replica nodes.
func TestCreateVolumeDefaultPinsTopology(t *testing.T) {
	b := newFakeBackend("n1", "n2", "n3")
	s := NewControllerServer(b, zap.NewNop())
	resp, err := s.CreateVolume(context.Background(), &csi.CreateVolumeRequest{
		Name:               "pvc_y",
		VolumeCapabilities: []*csi.VolumeCapability{{AccessMode: &csi.VolumeCapability_AccessMode{Mode: csi.VolumeCapability_AccessMode_SINGLE_NODE_WRITER}}},
		CapacityRange:      &csi.CapacityRange{RequiredBytes: giB},
		Parameters:         map[string]string{"pool": "vg0", "replicas": "2"},
	})
	require.NoError(t, err)
	assert.NotEmpty(t, resp.Volume.AccessibleTopology, "default volume must be pinned to replica nodes")
	assert.Empty(t, resp.Volume.VolumeContext[paramAllowRemoteVolumeAccess])
}
