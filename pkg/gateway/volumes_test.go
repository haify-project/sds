package gateway

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
)

// Tests for device-path resolution and volume roles. Both are checked against
// the failure they exist to prevent — a gateway that comes up happily while
// serving the wrong block device — so the assertions are about which device
// ends up where, not about return values being non-empty.

func TestGetDRBDDeviceForVolume(t *testing.T) {
	tests := []struct {
		baseDevice   string
		volumeNumber int
		expected     string
	}{
		{"/dev/drbd0", 0, "/dev/drbd0"},
		{"/dev/drbd0", 1, "/dev/drbd1"},
		{"/dev/drbd0", 2, "/dev/drbd2"},
		{"/dev/drbd10", 0, "/dev/drbd10"},
		{"/dev/drbd10", 1, "/dev/drbd11"},
		{"/dev/drbd100", 5, "/dev/drbd105"},
	}

	for _, tt := range tests {
		t.Run(fmt.Sprintf("%s+%d", tt.baseDevice, tt.volumeNumber), func(t *testing.T) {
			result := getDRBDDeviceForVolume(tt.baseDevice, tt.volumeNumber)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestParseDeviceMinorFromConfig(t *testing.T) {
	tests := []struct {
		name     string
		config   string
		expected int
	}{
		{
			name: "single volume",
			config: `
resource test {
    volume 0 {
        device minor 10;
        disk /dev/vg/lv;
    }
}
`,
			expected: 10,
		},
		{
			name: "no volume",
			config: `
resource test {
    net {
        protocol C;
    }
}
`,
			expected: -1,
		},
		{
			name: "device without minor",
			config: `
resource test {
    volume 0 {
        device /dev/drbd0;
    }
}
`,
			expected: -1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := parseDeviceMinorFromConfig(tt.config)
			assert.Equal(t, tt.expected, result)
		})
	}
}

// The gateway must export the operator's volume, not its own scratch volume.
//
// Haify creates the data volume first ("<res>_data", volume 0) and APPENDS the
// cluster-private state volume afterwards, which is the opposite of the
// linstor-gateway layout the templates were written against. Choosing by
// position therefore exported the 1 GiB state volume and formatted the
// operator's data volume as gateway scratch: a share that mounts, is the wrong
// size, and holds none of their storage.
func TestClusterPrivateIsTheStateVolumeNotVolumeZero(t *testing.T) {
	volumes := []*ResourceVolumeInfo{
		{VolumeID: 0, Device: "/dev/drbd26", SizeGB: 2, BackingVolume: "winblk_data"},
		{VolumeID: 1, Device: "/dev/drbd27", SizeGB: 1, BackingVolume: "winblk_state1"},
	}
	private, payload := clusterPrivateAndPayload(volumes, "/dev/drbd26")

	if private != "/dev/drbd27" {
		t.Errorf("cluster-private = %q, want the state volume /dev/drbd27", private)
	}
	if len(payload) != 1 || payload[0].Device != "/dev/drbd26" {
		t.Fatalf("payload = %+v, want the data volume /dev/drbd26", payload)
	}
}

// Order must not matter: a resource whose state volume happens to come first
// has to resolve the same way.
func TestVolumeRolesDoNotDependOnOrder(t *testing.T) {
	volumes := []*ResourceVolumeInfo{
		{VolumeID: 1, Device: "/dev/drbd27", BackingVolume: "res_state1"},
		{VolumeID: 0, Device: "/dev/drbd26", BackingVolume: "res_data"},
	}
	private, payload := clusterPrivateAndPayload(volumes, "/dev/drbd26")
	if private != "/dev/drbd27" || len(payload) != 1 || payload[0].Device != "/dev/drbd26" {
		t.Errorf("private=%q payload=%+v", private, payload)
	}
}

// Several data volumes all get exported; only the state volume is withheld.
func TestEveryNonStateVolumeIsExported(t *testing.T) {
	volumes := []*ResourceVolumeInfo{
		{VolumeID: 0, Device: "/dev/drbd10", BackingVolume: "res_data"},
		{VolumeID: 1, Device: "/dev/drbd11", BackingVolume: "res_extra"},
		{VolumeID: 2, Device: "/dev/drbd12", BackingVolume: "res_state2"},
	}
	private, payload := clusterPrivateAndPayload(volumes, "/dev/drbd10")
	if private != "/dev/drbd12" {
		t.Errorf("cluster-private = %q, want /dev/drbd12", private)
	}
	if len(payload) != 2 {
		t.Fatalf("payload = %+v, want both data volumes", payload)
	}
}

// A resource built by hand in the linstor layout has no "_state" volume, and
// guessing at it would break a working gateway. The original convention holds.
func TestLinstorStyleResourceKeepsTheOldLayout(t *testing.T) {
	volumes := []*ResourceVolumeInfo{
		{VolumeID: 0, Device: "/dev/drbd30", BackingVolume: "gw_private"},
		{VolumeID: 1, Device: "/dev/drbd31", BackingVolume: "gw_payload"},
	}
	private, payload := clusterPrivateAndPayload(volumes, "/dev/drbd30")
	if private != "/dev/drbd30" {
		t.Errorf("cluster-private = %q, want volume 0 /dev/drbd30", private)
	}
	if len(payload) != 1 || payload[0].Device != "/dev/drbd31" {
		t.Errorf("payload = %+v, want /dev/drbd31", payload)
	}
}
