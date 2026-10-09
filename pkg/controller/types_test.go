package controller

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestNodeStateString(t *testing.T) {
	tests := []struct {
		state    NodeState
		expected string
	}{
		{NodeStateOnline, "online"},
		{NodeStateOffline, "offline"},
		{NodeState(""), ""},
	}

	for _, tt := range tests {
		t.Run(string(tt.state), func(t *testing.T) {
			assert.Equal(t, tt.expected, string(tt.state))
		})
	}
}

func TestPoolInfoDevices(t *testing.T) {
	tests := []struct {
		name     string
		devices  []string
		expected int
	}{
		{"single device", []string{"/dev/sda"}, 1},
		{"multiple devices", []string{"/dev/sda", "/dev/sdb", "/dev/sdc"}, 3},
		{"no devices", []string{}, 0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pool := &PoolInfo{Devices: tt.devices}
			assert.Len(t, pool.Devices, tt.expected)
		})
	}
}

func TestResourceInfoNodes(t *testing.T) {
	tests := []struct {
		name     string
		nodes    []string
		expected int
	}{
		{"single node", []string{"node1"}, 1},
		{"two nodes", []string{"node1", "node2"}, 2},
		{"three nodes", []string{"node1", "node2", "node3"}, 3},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			res := &ResourceInfo{Nodes: tt.nodes}
			assert.Len(t, res.Nodes, tt.expected)
		})
	}
}

func TestResourceInfoProtocol(t *testing.T) {
	protocols := []string{"A", "B", "C"}
	for _, proto := range protocols {
		t.Run(proto, func(t *testing.T) {
			res := &ResourceInfo{Protocol: proto}
			assert.Equal(t, proto, res.Protocol)
		})
	}
}

func TestSnapshotInfoFields(t *testing.T) {
	snap := &SnapshotInfo{
		Name:      "daily-backup",
		Volume:    "haify_vg0/data",
		SizeGB:    500,
		CreatedAt: "2024-01-15 10:30:00",
	}

	assert.Equal(t, "daily-backup", snap.Name)
	assert.Equal(t, "haify_vg0/data", snap.Volume)
	assert.Equal(t, uint64(500), snap.SizeGB)
	assert.Equal(t, "2024-01-15 10:30:00", snap.CreatedAt)
}

func TestResourceVolumeInfoDevice(t *testing.T) {
	tests := []struct {
		volumeID uint32
		device   string
	}{
		{0, "/dev/drbd0"},
		{1, "/dev/drbd1"},
		{10, "/dev/drbd10"},
	}

	for _, tt := range tests {
		t.Run(tt.device, func(t *testing.T) {
			vol := &ResourceVolumeInfo{
				VolumeID: tt.volumeID,
				Device:   tt.device,
			}
			assert.Equal(t, tt.volumeID, vol.VolumeID)
			assert.Equal(t, tt.device, vol.Device)
		})
	}
}

func TestResourceNodeStateFields(t *testing.T) {
	tests := []struct {
		name        string
		role        string
		diskState   string
		replication string
	}{
		{"primary", "Primary", "UpToDate", "Established"},
		{"secondary", "Secondary", "UpToDate", "Established"},
		{"offline", "Secondary", "Diskless", "Off"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			state := &ResourceNodeState{
				Role:        tt.role,
				DiskState:   tt.diskState,
				Replication: tt.replication,
			}
			assert.Equal(t, tt.role, state.Role)
			assert.Equal(t, tt.diskState, state.DiskState)
			assert.Equal(t, tt.replication, state.Replication)
		})
	}
}

func TestNodeInfoFields(t *testing.T) {
	node := &NodeInfo{
		Name:     "storage-node-1",
		Address:  "10.0.0.100",
		Hostname: "storage-node-1.example.com",
		State:    NodeStateOnline,
		Version:  "1.2.3",
		Capacity: map[string]interface{}{
			"cpu_count":    16,
			"memory_gb":    64,
			"disk_count":   4,
			"drbd_version": "9.2.5",
		},
	}

	assert.Equal(t, "storage-node-1", node.Name)
	assert.Equal(t, "10.0.0.100", node.Address)
	assert.Equal(t, "storage-node-1.example.com", node.Hostname)
	assert.Equal(t, NodeStateOnline, node.State)
	assert.Equal(t, "1.2.3", node.Version)
	assert.NotNil(t, node.Capacity)
	assert.Equal(t, 16, node.Capacity["cpu_count"])
	assert.Equal(t, 64, node.Capacity["memory_gb"])
}

func TestPoolInfoTypes(t *testing.T) {
	types := []string{"vg", "zfs", "thin_pool"}
	for _, poolType := range types {
		t.Run(poolType, func(t *testing.T) {
			pool := &PoolInfo{Type: poolType}
			assert.Equal(t, poolType, pool.Type)
		})
	}
}

func TestResourceInfoWithEmptyVolumes(t *testing.T) {
	res := &ResourceInfo{
		Name:    "empty-resource",
		Volumes: []*ResourceVolumeInfo{},
	}

	assert.Empty(t, res.Volumes)
}

func TestResourceInfoWithNilVolumes(t *testing.T) {
	res := &ResourceInfo{
		Name:    "nil-volumes",
		Volumes: nil,
	}

	assert.Nil(t, res.Volumes)
}

func TestNodeInfoWithEmptyCapacity(t *testing.T) {
	node := &NodeInfo{
		Name:     "empty-capacity",
		Capacity: map[string]interface{}{},
	}

	assert.NotNil(t, node.Capacity)
	assert.Empty(t, node.Capacity)
}

func TestResourceInfoWithEmptyNodeStates(t *testing.T) {
	res := &ResourceInfo{
		Name:       "empty-states",
		NodeStates: map[string]*ResourceNodeState{},
	}

	assert.NotNil(t, res.NodeStates)
	assert.Empty(t, res.NodeStates)
}

func TestPoolInfoWithCompression(t *testing.T) {
	pool := &PoolInfo{
		Name:        "compressed-pool",
		Type:        "zfs",
		Compression: "lz4",
	}

	assert.Equal(t, "compressed-pool", pool.Name)
	assert.Equal(t, "lz4", pool.Compression)
}

func TestPoolInfoWithoutCompression(t *testing.T) {
	pool := &PoolInfo{
		Name:        "uncompressed-pool",
		Type:        "vg",
		Compression: "",
	}

	assert.Empty(t, pool.Compression)
}

func TestResourceInfoPort(t *testing.T) {
	tests := []struct {
		port     uint32
		expected uint32
	}{
		{7000, 7000},
		{7001, 7001},
		{0, 0},
		{65535, 65535},
	}

	for _, tt := range tests {
		t.Run(string(rune(tt.port)), func(t *testing.T) {
			res := &ResourceInfo{Port: tt.port}
			assert.Equal(t, tt.expected, res.Port)
		})
	}
}

func TestSnapshotInfoSize(t *testing.T) {
	tests := []struct {
		size     uint64
		expected uint64
	}{
		{0, 0},
		{100, 100},
		{1000, 1000},
		{1024 * 1024 * 1024, 1024 * 1024 * 1024}, // 1GiB in bytes
	}

	for _, tt := range tests {
		t.Run(string(rune(tt.size)), func(t *testing.T) {
			snap := &SnapshotInfo{SizeGB: tt.size}
			assert.Equal(t, tt.expected, snap.SizeGB)
		})
	}
}

func TestNodeHealthInfoFields(t *testing.T) {
	health := &NodeHealthInfo{
		DrbdInstalled:           true,
		DrbdVersion:             "9.2.5",
		DrbdReactorInstalled:    true,
		DrbdReactorVersion:      "1.2.0",
		DrbdReactorRunning:      true,
		ResourceAgentsInstalled: true,
		AvailableAgents:         []string{"Filesystem", "IPaddr2", "nfsserver", "exportfs"},
	}

	assert.True(t, health.DrbdInstalled)
	assert.Equal(t, "9.2.5", health.DrbdVersion)
	assert.True(t, health.DrbdReactorInstalled)
	assert.Equal(t, "1.2.0", health.DrbdReactorVersion)
	assert.True(t, health.DrbdReactorRunning)
	assert.True(t, health.ResourceAgentsInstalled)
	assert.Len(t, health.AvailableAgents, 4)
	assert.Contains(t, health.AvailableAgents, "Filesystem")
	assert.Contains(t, health.AvailableAgents, "IPaddr2")
}

func TestNodeHealthInfoAllFalse(t *testing.T) {
	health := &NodeHealthInfo{
		DrbdInstalled:           false,
		DrbdReactorInstalled:    false,
		DrbdReactorRunning:      false,
		ResourceAgentsInstalled: false,
		AvailableAgents:         []string{},
	}

	assert.False(t, health.DrbdInstalled)
	assert.False(t, health.DrbdReactorInstalled)
	assert.False(t, health.DrbdReactorRunning)
	assert.False(t, health.ResourceAgentsInstalled)
	assert.Empty(t, health.AvailableAgents)
}
