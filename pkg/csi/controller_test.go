package csi

import (
	"context"
	"testing"

	"github.com/container-storage-interface/spec/lib/go/csi"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
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

func TestCreateVolumeOnlyPlacesOnPoolNodes(t *testing.T) {
	b := newFakeBackend("n1", "n2", "n3")
	b.onlyPoolOnNodes("n1", "n2") // n3 has no backing pool
	_, err := newTestController(b).CreateVolume(context.Background(), validCreateReq("pvc-pool"))
	require.NoError(t, err)
	require.Len(t, b.createCalls, 1)
	for _, n := range b.createCalls[0].nodes {
		assert.NotEqual(t, "n3", n, "must not place a replica on a node without the pool")
	}
	assert.Len(t, b.createCalls[0].nodes, 2)
}

func TestCreateVolumeSkipsPoollessRequisiteNode(t *testing.T) {
	b := newFakeBackend("n1", "n2", "n3")
	b.onlyPoolOnNodes("n1", "n2")
	req := validCreateReq("pvc-req")
	// The scheduler prefers n3, but n3 lacks the pool: it must be dropped, not
	// placed on and failed at LV-creation time.
	req.AccessibilityRequirements = &csi.TopologyRequirement{
		Preferred: []*csi.Topology{{Segments: map[string]string{TopologyKeyNode: "n3"}}},
	}
	_, err := newTestController(b).CreateVolume(context.Background(), req)
	require.NoError(t, err)
	for _, n := range b.createCalls[0].nodes {
		assert.NotEqual(t, "n3", n)
	}
}

func TestCreateVolumeInsufficientPoolNodes(t *testing.T) {
	b := newFakeBackend("n1", "n2", "n3")
	b.onlyPoolOnNodes("n1") // only one node has the pool, but replicas default to 2
	_, err := newTestController(b).CreateVolume(context.Background(), validCreateReq("pvc-few"))
	require.Error(t, err)
	assert.Equal(t, codes.ResourceExhausted, status.Code(err))
	assert.Empty(t, b.createCalls, "no resource must be created when the pool lacks enough nodes")
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
