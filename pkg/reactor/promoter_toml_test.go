package reactor

import (
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestGenerateNFSPromoterConfig(t *testing.T) {
	resourceName := "test-nfs"
	serviceIP := "192.168.1.100"
	exportPath := "/export/data"
	clients := []NFSClient{
		{Address: "192.168.1.0/24", Options: "rw"},
		{Address: "10.0.0.0/8", Options: "ro"},
	}
	options := "rw,no_root_squash"
	nodes := []string{"node1", "node2"}

	config := GenerateNFSPromoterConfig(resourceName, serviceIP, exportPath, clients, options, nodes)

	// Verify basic structure
	assert.Contains(t, config, "[[promoter.resources."+resourceName+"]]")
	assert.Contains(t, config, "runner = \"systemd\"")
	assert.Contains(t, config, "on-drbd-demote-failure = \"stop-on-error\"")
	assert.Contains(t, config, "secondary-force = true")

	// Verify OCF resources
	assert.Contains(t, config, "ocf:heartbeat:Filesystem")
	assert.Contains(t, config, "ocf:heartbeat:nfsserver")
	assert.Contains(t, config, "ocf:heartbeat:exportfs")
	assert.Contains(t, config, "ocf:heartbeat:IPaddr2")

	// Verify parameters
	assert.Contains(t, config, serviceIP)
	assert.Contains(t, config, exportPath)
	assert.Contains(t, config, "/dev/drbd/by-res/"+resourceName+"/0")

	// Verify nodes
	assert.Contains(t, config, "node1")
	assert.Contains(t, config, "node2")
}

func TestGenerateNFSPromoterConfigWithSingleClient(t *testing.T) {
	resourceName := "single-client"
	serviceIP := "10.0.0.100"
	exportPath := "/data"
	clients := []NFSClient{
		{Address: "0.0.0.0/0", Options: "rw,async"},
	}
	options := "rw"
	nodes := []string{"orange1"}

	config := GenerateNFSPromoterConfig(resourceName, serviceIP, exportPath, clients, options, nodes)

	assert.Contains(t, config, resourceName)
	assert.Contains(t, config, serviceIP)
	assert.Contains(t, config, "0.0.0.0/0(rw,async)")
	assert.Contains(t, config, "orange1")
}

func TestGenerateiSCSIPromoterConfig(t *testing.T) {
	resourceName := "test-iscsi"
	serviceIP := "192.168.1.200"
	iqn := "iqn.2024-01.com.example:sds.test"
	tpgt := 1
	luns := []ISCSILun{
		{LUN: 1, BSType: "rdwr", VolumeNumber: 1},
		{LUN: 2, BSType: "rdwr", VolumeNumber: 2},
	}
	implementation := "lio-t"
	nodes := []string{"node1", "node2"}

	config := GenerateiSCSIPromoterConfig(resourceName, serviceIP, iqn, tpgt, luns, implementation, nodes)

	// Verify basic structure
	assert.Contains(t, config, "[[promoter.resources."+resourceName+"]]")
	assert.Contains(t, config, "runner = \"systemd\"")

	// Verify OCF resources
	assert.Contains(t, config, "ocf:heartbeat:IPaddr2")
	assert.Contains(t, config, "ocf:heartbeat:HA-SDS-iSCSITarget")
	assert.Contains(t, config, "ocf:heartbeat:HA-SDS-iSCSILogicalUnit")

	// Verify parameters
	assert.Contains(t, config, serviceIP)
	assert.Contains(t, config, iqn)
	assert.Contains(t, config, implementation)

	// Verify LUNs
	assert.Contains(t, config, "lun = 1")
	assert.Contains(t, config, "lun = 2")

	// Verify nodes
	assert.Contains(t, config, "node1")
	assert.Contains(t, config, "node2")
}

func TestGenerateiSCSIPromoterConfigWithMultipleLUNs(t *testing.T) {
	resourceName := "multi-lun"
	serviceIP := "10.0.0.50"
	iqn := "iqn.2024-01.com.example:storage"
	tpgt := 1

	// Create multiple LUNs
	luns := make([]ISCSILun, 10)
	for i := 0; i < 10; i++ {
		luns[i] = ISCSILun{
			LUN:          i + 1,
			BSType:       "rdwr",
			VolumeNumber: i + 1,
		}
	}

	implementation := "lio-t"
	nodes := []string{"iscsi-node1", "iscsi-node2", "iscsi-node3"}

	config := GenerateiSCSIPromoterConfig(resourceName, serviceIP, iqn, tpgt, luns, implementation, nodes)

	// Verify all LUNs are present (numbers don't have leading zeros)
	for i := 1; i <= 10; i++ {
		assert.Contains(t, config, "lun = "+strconv.Itoa(i))
	}

	// Verify all nodes
	for _, node := range nodes {
		assert.Contains(t, config, node)
	}
}

func TestGenerateNVMePromoterConfig(t *testing.T) {
	resourceName := "test-nvme"
	serviceIP := "192.168.1.150"
	nqn := "nqn.2024-01.com.example:sds.test"
	namespaces := []NVMeNamespace{
		{NSID: 1, VolumeNumber: 1},
		{NSID: 2, VolumeNumber: 2},
	}
	nodes := []string{"node1", "node2"}

	config := GenerateNVMePromoterConfig(resourceName, serviceIP, nqn, namespaces, nodes)

	// Verify basic structure
	assert.Contains(t, config, "[[promoter.resources."+resourceName+"]]")
	assert.Contains(t, config, "runner = \"systemd\"")
	assert.Contains(t, config, "on-drbd-demote-failure = \"stop-on-error\"")

	// Verify nodes
	assert.Contains(t, config, "node1")
	assert.Contains(t, config, "node2")
}

func TestGenerateGenericPromoterConfig(t *testing.T) {
	resourceName := "generic-resource"
	units := []string{"postgresql.service", "nginx.service"}
	ocfResources := []map[string]string{
		{
			"provider":  "heartbeat",
			"agent":     "Filesystem",
			"name":      "fs_data",
			"device":    "/dev/drbd0",
			"directory": "/mnt/data",
			"fstype":    "ext4",
		},
		{
			"provider":     "heartbeat",
			"agent":        "IPaddr2",
			"name":         "vip",
			"ip":           "192.168.1.100",
			"cidr_netmask": "24",
		},
	}

	config := GenerateGenericPromoterConfig(resourceName, units, ocfResources)

	// Verify basic structure
	assert.Contains(t, config, "[[promoter]]")
	assert.Contains(t, config, "[promoter.resources."+resourceName+"]")
	assert.Contains(t, config, "runner = \"systemd\"")

	// Verify systemd units
	for _, unit := range units {
		assert.Contains(t, config, unit)
	}

	// Verify OCF resources
	assert.Contains(t, config, "ocf:heartbeat:Filesystem")
	assert.Contains(t, config, "ocf:heartbeat:IPaddr2")

	// Verify parameters
	assert.Contains(t, config, "/dev/drbd0")
	assert.Contains(t, config, "/mnt/data")
	assert.Contains(t, config, "192.168.1.100")
}

func TestGenerateGenericPromoterConfigWithMinimalOCF(t *testing.T) {
	resourceName := "minimal-resource"
	units := []string{"myapp.service"}
	ocfResources := []map[string]string{
		{
			"agent": "IPaddr2",
			"ip":    "10.0.0.100",
		},
	}

	config := GenerateGenericPromoterConfig(resourceName, units, ocfResources)

	// Should use default provider (heartbeat)
	assert.Contains(t, config, "ocf:heartbeat:IPaddr2")
	assert.Contains(t, config, "10.0.0.100")
}

func TestGenerateGenericPromoterConfigWithEmptyOCF(t *testing.T) {
	resourceName := "systemd-only"
	units := []string{"app1.service", "app2.service"}
	ocfResources := []map[string]string{}

	config := GenerateGenericPromoterConfig(resourceName, units, ocfResources)

	// Should only contain systemd units
	assert.Contains(t, config, "app1.service")
	assert.Contains(t, config, "app2.service")
	assert.NotContains(t, config, "ocf:")
}

func TestFormatNFSClients(t *testing.T) {
	tests := []struct {
		name     string
		clients  []NFSClient
		expected string
	}{
		{
			name:     "single client",
			clients:  []NFSClient{{Address: "192.168.1.0/24", Options: "rw"}},
			expected: "192.168.1.0/24(rw)",
		},
		{
			name: "multiple clients",
			clients: []NFSClient{
				{Address: "192.168.1.0/24", Options: "rw"},
				{Address: "10.0.0.0/8", Options: "ro"},
			},
			expected: "192.168.1.0/24(rw) 10.0.0.0/8(ro)",
		},
		{
			name:     "empty clients",
			clients:  []NFSClient{},
			expected: "",
		},
		{
			name:     "client with complex options",
			clients:  []NFSClient{{Address: "172.16.0.0/12", Options: "rw,sync,no_root_squash"}},
			expected: "172.16.0.0/12(rw,sync,no_root_squash)",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := formatNFSClients(tt.clients)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestGenerateNFSPromoterConfigDRBDDevice(t *testing.T) {
	resourceName := "drbd-device-test"
	serviceIP := "192.168.1.100"
	exportPath := "/export/data"
	clients := []NFSClient{{Address: "0.0.0.0/0", Options: "rw"}}
	options := "rw"
	nodes := []string{"node1"}

	config := GenerateNFSPromoterConfig(resourceName, serviceIP, exportPath, clients, options, nodes)

	// Verify DRBD device path format
	assert.Contains(t, config, "device = \"/dev/drbd/by-res/"+resourceName+"/0\"")
}

func TestGenerateiSCSIPromoterConfigPortal(t *testing.T) {
	resourceName := "portal-test"
	serviceIP := "192.168.1.200"
	iqn := "iqn.2024-01.com.example:test"
	tpgt := 1
	luns := []ISCSILun{{LUN: 1, BSType: "rdwr", VolumeNumber: 1}}
	implementation := "lio-t"
	nodes := []string{"node1"}

	config := GenerateiSCSIPromoterConfig(resourceName, serviceIP, iqn, tpgt, luns, implementation, nodes)

	// Verify portal format (IP:3260)
	assert.Contains(t, config, serviceIP+":3260")
}

func TestGenerateNVMePromoterConfigEmptyNamespaces(t *testing.T) {
	resourceName := "empty-nvme"
	serviceIP := "192.168.1.150"
	nqn := "nqn.2024-01.com.example:empty"
	namespaces := []NVMeNamespace{}
	nodes := []string{"node1"}

	config := GenerateNVMePromoterConfig(resourceName, serviceIP, nqn, namespaces, nodes)

	// Should still generate valid config
	assert.Contains(t, config, "[[promoter.resources."+resourceName+"]]")
	assert.True(t, strings.Contains(config, "start = []") || strings.Contains(config, "start ="))
}
