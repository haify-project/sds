package controller

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestPoolInfo(t *testing.T) {
	pool := &PoolInfo{
		Name:    "sds_vg0",
		Type:    "vg",
		Node:    "node1",
		TotalGB: 1000,
		FreeGB:  500,
		Devices: []string{"/dev/sda", "/dev/sdb"},
		Thin:    false,
	}

	assert.Equal(t, "sds_vg0", pool.Name)
	assert.Equal(t, "vg", pool.Type)
	assert.Equal(t, "node1", pool.Node)
	assert.Equal(t, uint64(1000), pool.TotalGB)
	assert.Equal(t, uint64(500), pool.FreeGB)
	assert.Len(t, pool.Devices, 2)
}

func TestPoolInfoZFS(t *testing.T) {
	pool := &PoolInfo{
		Name:        "sds_pool0",
		Type:        "zfs",
		Node:        "node2",
		TotalGB:     2000,
		FreeGB:      1500,
		Devices:     []string{"/dev/nvme0n1"},
		Thin:        true,
		Compression: "lz4",
	}

	assert.Equal(t, "sds_pool0", pool.Name)
	assert.Equal(t, "zfs", pool.Type)
	assert.Equal(t, "lz4", pool.Compression)
	assert.True(t, pool.Thin)
}

func TestSnapshotInfo(t *testing.T) {
	snap := &SnapshotInfo{
		Name:      "snap_20240101",
		Volume:    "data",
		SizeGB:    100,
		CreatedAt: "2024-01-01 12:00:00",
	}

	assert.Equal(t, "snap_20240101", snap.Name)
	assert.Equal(t, "data", snap.Volume)
	assert.Equal(t, uint64(100), snap.SizeGB)
	assert.Equal(t, "2024-01-01 12:00:00", snap.CreatedAt)
}

func TestResourceInfo(t *testing.T) {
	res := &ResourceInfo{
		Name:     "data",
		Port:     7000,
		Nodes:    []string{"node1", "node2"},
		Volumes:  []*ResourceVolumeInfo{},
		Protocol: "C",
	}

	assert.Equal(t, "data", res.Name)
	assert.Equal(t, uint32(7000), res.Port)
	assert.Len(t, res.Nodes, 2)
	assert.Equal(t, "C", res.Protocol)
}

func TestResourceVolumeInfo(t *testing.T) {
	vol := &ResourceVolumeInfo{
		VolumeID: 0,
		Device:   "/dev/drbd0",
	}

	assert.Equal(t, uint32(0), vol.VolumeID)
	assert.Equal(t, "/dev/drbd0", vol.Device)
}

func TestResourceNodeState(t *testing.T) {
	state := &ResourceNodeState{
		Role:        "Primary",
		DiskState:   "UpToDate",
		Replication: "Established",
	}

	assert.Equal(t, "Primary", state.Role)
	assert.Equal(t, "UpToDate", state.DiskState)
	assert.Equal(t, "Established", state.Replication)
}

func TestNodeInfo(t *testing.T) {
	node := &NodeInfo{
		Name:     "node1",
		Address:  "192.168.1.100",
		Hostname: "node1.local",
		State:    NodeStateOnline,
		Version:  "1.0.0",
	}

	assert.Equal(t, "node1", node.Name)
	assert.Equal(t, "192.168.1.100", node.Address)
	assert.Equal(t, NodeStateOnline, node.State)
	assert.Equal(t, "1.0.0", node.Version)
}

func TestNodeStateConstants(t *testing.T) {
	// Test that NodeState constants exist and have expected values
	assert.Equal(t, NodeState("online"), NodeStateOnline)
	assert.Equal(t, NodeState("offline"), NodeStateOffline)
}

func TestPoolInfoThinPool(t *testing.T) {
	pool := &PoolInfo{
		Name:    "sds_thin_pool",
		Type:    "thin_pool",
		Node:    "node1",
		TotalGB: 1000,
		FreeGB:  800,
		Thin:    true,
	}

	assert.Equal(t, "thin_pool", pool.Type)
	assert.True(t, pool.Thin)
}

func TestResourceInfoMultipleVolumes(t *testing.T) {
	res := &ResourceInfo{
		Name: "multi-vol",
		Volumes: []*ResourceVolumeInfo{
			{VolumeID: 0, Device: "/dev/drbd0"},
			{VolumeID: 1, Device: "/dev/drbd1"},
		},
	}

	assert.Len(t, res.Volumes, 2)
	assert.Equal(t, uint32(0), res.Volumes[0].VolumeID)
	assert.Equal(t, uint32(1), res.Volumes[1].VolumeID)
}

func TestNodeInfoWithCapacity(t *testing.T) {
	node := &NodeInfo{
		Name:     "node1",
		Address:  "192.168.1.100",
		State:    NodeStateOnline,
		Capacity: map[string]interface{}{"cpu": 8, "memory": "32Gi"},
	}

	assert.Equal(t, "node1", node.Name)
	assert.NotNil(t, node.Capacity)
	assert.Equal(t, 8, node.Capacity["cpu"])
}

func TestNodeInfoLastSeen(t *testing.T) {
	now := time.Now()
	node := &NodeInfo{
		Name:     "node1",
		LastSeen: now,
	}

	assert.Equal(t, now, node.LastSeen)
}

func TestResourceInfoWithNodeStates(t *testing.T) {
	res := &ResourceInfo{
		Name:  "data",
		Port:  7000,
		Nodes: []string{"node1", "node2"},
		NodeStates: map[string]*ResourceNodeState{
			"node1": {Role: "Primary", DiskState: "UpToDate"},
			"node2": {Role: "Secondary", DiskState: "UpToDate"},
		},
	}

	assert.NotNil(t, res.NodeStates)
	assert.Len(t, res.NodeStates, 2)
	assert.Equal(t, "Primary", res.NodeStates["node1"].Role)
	assert.Equal(t, "Secondary", res.NodeStates["node2"].Role)
}

func TestNodeHealthInfo(t *testing.T) {
	health := &NodeHealthInfo{
		DrbdInstalled:           true,
		DrbdVersion:             "9.2.5",
		DrbdReactorInstalled:    true,
		DrbdReactorVersion:      "1.2.0",
		DrbdReactorRunning:      true,
		ResourceAgentsInstalled: true,
		AvailableAgents:         []string{"Filesystem", "IPaddr2", "nfsserver"},
	}

	assert.True(t, health.DrbdInstalled)
	assert.Equal(t, "9.2.5", health.DrbdVersion)
	assert.True(t, health.DrbdReactorRunning)
	assert.Len(t, health.AvailableAgents, 3)
}

func TestResourceInfoRole(t *testing.T) {
	res := &ResourceInfo{
		Name:  "data",
		Role:  "Primary",
		Nodes: []string{"node1"},
	}

	assert.Equal(t, "Primary", res.Role)
}
