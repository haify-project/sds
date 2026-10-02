package csi

import (
	"context"
	"testing"

	"github.com/container-storage-interface/spec/lib/go/csi"
	sdspb "github.com/haify-project/sds/api/proto/v1"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

type expandFakeBackend struct {
	fakeBackend
	resizedVolume string
	resizedSizeGB uint32
	resizeErr     error
}

func (f *expandFakeBackend) ResizeVolume(_ context.Context, resource string, volumeID uint32, sizeGB uint32) error {
	if f.resizeErr != nil {
		return f.resizeErr
	}
	f.resizedVolume = resource
	f.resizedSizeGB = sizeGB
	return nil
}

func TestControllerExpandVolume(t *testing.T) {
	b := &expandFakeBackend{fakeBackend: *newFakeBackend("n1")}
	ctrl := NewControllerServer(b, zap.NewNop())

	// 1. Missing volume ID
	_, err := ctrl.ControllerExpandVolume(context.Background(), &csi.ControllerExpandVolumeRequest{})
	assert.Error(t, err)

	// 2. Successful expansion
	req := &csi.ControllerExpandVolumeRequest{
		VolumeId: "res1",
		CapacityRange: &csi.CapacityRange{
			RequiredBytes: 10 * giB,
		},
	}
	resp, err := ctrl.ControllerExpandVolume(context.Background(), req)
	require.NoError(t, err)
	assert.Equal(t, int64(10*giB), resp.CapacityBytes)
	assert.True(t, resp.NodeExpansionRequired)
	assert.Equal(t, "res1", b.resizedVolume)
	assert.Equal(t, uint32(10), b.resizedSizeGB)
}

type expandMounter struct {
	recordingMounter
	resizedDevice string
	resizedMount  string
}

func (m *expandMounter) ResizeFS(devicePath, mountPath string) error {
	m.resizedDevice = devicePath
	m.resizedMount = mountPath
	return nil
}

func TestNodeExpandVolume(t *testing.T) {
	b := newFakeBackend("n1")
	require.NoError(t, b.CreateResourceWithPoolAndType(context.Background(), "res1", 0, []string{"n1"}, "C", 1, "vg0", "lvm", nil))

	// Ensure GetResource returns device path
	res, err := b.GetResource(context.Background(), "res1")
	require.NoError(t, err)
	res.Volumes = []*sdspb.VolumeInfo{{VolumeId: 0, Device: "/dev/drbd100"}}

	m := &expandMounter{recordingMounter: *newRecordingMounter()}
	node := newTestNode(b, m)

	// 1. Missing volume ID
	_, err = node.NodeExpandVolume(context.Background(), &csi.NodeExpandVolumeRequest{})
	assert.Error(t, err)

	// 2. Success
	req := &csi.NodeExpandVolumeRequest{
		VolumeId:   "res1",
		VolumePath: "/stage/res1",
		CapacityRange: &csi.CapacityRange{
			RequiredBytes: 20 * giB,
		},
	}
	resp, err := node.NodeExpandVolume(context.Background(), req)
	require.NoError(t, err)
	assert.Equal(t, int64(20*giB), resp.CapacityBytes)
	assert.Equal(t, "/dev/drbd100", m.resizedDevice)
	assert.Equal(t, "/stage/res1", m.resizedMount)
}
