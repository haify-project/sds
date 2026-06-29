package csi

import (
	"context"
	"testing"

	"github.com/container-storage-interface/spec/lib/go/csi"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

func newTestController(b SDSBackend) *controllerServer {
	return NewControllerServer(b, zap.NewNop()).(*controllerServer)
}

func validCreateReq(name string) *csi.CreateVolumeRequest {
	return &csi.CreateVolumeRequest{
		Name:               name,
		CapacityRange:      &csi.CapacityRange{RequiredBytes: 2 << 30}, // 2 GiB
		Parameters:         map[string]string{"pool": "vg0"},
		VolumeCapabilities: []*csi.VolumeCapability{{AccessMode: &csi.VolumeCapability_AccessMode{Mode: csi.VolumeCapability_AccessMode_SINGLE_NODE_WRITER}}},
	}
}

func TestCreateVolumeCreatesResource(t *testing.T) {
	b := newFakeBackend("n1", "n2", "n3")
	resp, err := newTestController(b).CreateVolume(context.Background(), validCreateReq("pvc-abc"))
	require.NoError(t, err)
	assert.Equal(t, "pvc_abc", resp.Volume.VolumeId)
	assert.Equal(t, int64(2<<30), resp.Volume.CapacityBytes)
	assert.Len(t, b.createCalls, 1)
	assert.Equal(t, uint32(2), b.createCalls[0].sizeGB)
	assert.Equal(t, uint32(0), b.createCalls[0].port, "port must be auto (0) for controller to allocate")
	assert.Len(t, resp.Volume.AccessibleTopology, 2)
}

func TestCreateVolumeIdempotent(t *testing.T) {
	b := newFakeBackend("n1", "n2", "n3")
	c := newTestController(b)
	_, err := c.CreateVolume(context.Background(), validCreateReq("pvc-abc"))
	require.NoError(t, err)
	_, err = c.CreateVolume(context.Background(), validCreateReq("pvc-abc"))
	require.NoError(t, err)
	assert.Len(t, b.createCalls, 1, "second call must not create again")
}

func TestCreateVolumeHonorsRequisiteTopology(t *testing.T) {
	b := newFakeBackend("n1", "n2", "n3")
	req := validCreateReq("pvc-top")
	req.AccessibilityRequirements = &csi.TopologyRequirement{
		Requisite: []*csi.Topology{{Segments: map[string]string{TopologyKeyNode: "n3"}}},
	}
	_, err := newTestController(b).CreateVolume(context.Background(), req)
	require.NoError(t, err)
	assert.Equal(t, "n3", b.createCalls[0].nodes[0])
}

func TestDeleteVolumeIdempotent(t *testing.T) {
	b := newFakeBackend("n1", "n2")
	c := newTestController(b)
	_, err := c.DeleteVolume(context.Background(), &csi.DeleteVolumeRequest{VolumeId: "does-not-exist"})
	require.NoError(t, err, "deleting a missing volume must succeed")
}

func TestCreateVolumeMissingPool(t *testing.T) {
	b := newFakeBackend("n1", "n2")
	req := validCreateReq("pvc-x")
	req.Parameters = map[string]string{}
	_, err := newTestController(b).CreateVolume(context.Background(), req)
	assert.Error(t, err)
}
