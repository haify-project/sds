package csi

import (
	"context"
	"testing"

	"github.com/container-storage-interface/spec/lib/go/csi"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

// recordingMounter captures mount calls.
type recordingMounter struct {
	formatted []string
	bind      []string
	unmounted []string
	mounted   map[string]bool
}

func newRecordingMounter() *recordingMounter { return &recordingMounter{mounted: map[string]bool{}} }

func (m *recordingMounter) FormatAndMount(source, target, fsType string, _ []string) error {
	m.formatted = append(m.formatted, source+"->"+target+":"+fsType)
	m.mounted[target] = true
	return nil
}
func (m *recordingMounter) Mount(source, target, _ string, _ []string) error {
	m.bind = append(m.bind, source+"->"+target)
	m.mounted[target] = true
	return nil
}
func (m *recordingMounter) Unmount(target string) error {
	m.unmounted = append(m.unmounted, target)
	delete(m.mounted, target)
	return nil
}
func (m *recordingMounter) IsMountPoint(target string) (bool, error) { return m.mounted[target], nil }
func (m *recordingMounter) EnsureDir(string) error                   { return nil }

func newTestNode(b SDSBackend, m Mounter) *nodeServer {
	return NewNodeServer(b, m, "n1", "10.0.0.1", zap.NewNop()).(*nodeServer)
}

func TestNodeGetInfoReportsTopology(t *testing.T) {
	resp, err := newTestNode(newFakeBackend(), newRecordingMounter()).NodeGetInfo(context.Background(), &csi.NodeGetInfoRequest{})
	require.NoError(t, err)
	assert.Equal(t, "n1", resp.NodeId)
	assert.Equal(t, "n1", resp.AccessibleTopology.Segments[TopologyKeyNode])
}

func TestNodeStagePromotesAndFormats(t *testing.T) {
	b := newFakeBackend("n1", "n2")
	require.NoError(t, b.CreateResourceWithPoolAndType(context.Background(), "pvc_x", 0, []string{"n1", "n2"}, "C", 1, "vg0", "lvm", nil))
	m := newRecordingMounter()
	req := &csi.NodeStageVolumeRequest{
		VolumeId:          "pvc_x",
		StagingTargetPath: "/stage/pvc_x",
		VolumeCapability:  &csi.VolumeCapability{AccessType: &csi.VolumeCapability_Mount{Mount: &csi.VolumeCapability_MountVolume{FsType: "ext4"}}},
	}
	_, err := newTestNode(b, m).NodeStageVolume(context.Background(), req)
	require.NoError(t, err)
	assert.Equal(t, "n1", b.primary["pvc_x"], "must promote this node to Primary")
	assert.Equal(t, []string{"/dev/drbd100->/stage/pvc_x:ext4"}, m.formatted)
}

func TestNodeUnstageDemotes(t *testing.T) {
	b := newFakeBackend("n1", "n2")
	require.NoError(t, b.CreateResourceWithPoolAndType(context.Background(), "pvc_x", 0, []string{"n1"}, "C", 1, "vg0", "lvm", nil))
	b.primary["pvc_x"] = "n1"
	m := newRecordingMounter()
	m.mounted["/stage/pvc_x"] = true
	_, err := newTestNode(b, m).NodeUnstageVolume(context.Background(), &csi.NodeUnstageVolumeRequest{VolumeId: "pvc_x", StagingTargetPath: "/stage/pvc_x"})
	require.NoError(t, err)
	assert.Contains(t, m.unmounted, "/stage/pvc_x")
	_, isPrimary := b.primary["pvc_x"]
	assert.False(t, isPrimary, "must demote to Secondary")
}

func TestNodePublishBindMounts(t *testing.T) {
	m := newRecordingMounter()
	req := &csi.NodePublishVolumeRequest{
		VolumeId:          "pvc_x",
		StagingTargetPath: "/stage/pvc_x",
		TargetPath:        "/pods/pvc_x",
		VolumeCapability:  &csi.VolumeCapability{AccessType: &csi.VolumeCapability_Mount{Mount: &csi.VolumeCapability_MountVolume{}}},
	}
	_, err := newTestNode(newFakeBackend(), m).NodePublishVolume(context.Background(), req)
	require.NoError(t, err)
	assert.Equal(t, []string{"/stage/pvc_x->/pods/pvc_x"}, m.bind)
}
