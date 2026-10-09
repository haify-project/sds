package controller

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const moveTestConfig = `resource a1 {
    volume 0 {
        device    minor 31;
        disk      /dev/haify_tp/a1_data;
        meta-disk internal;
        disk {
            rs-discard-granularity 65536;
        }
    }

    on sdt1 {
        address   192.168.123.205:7212;
        node-id   0;
    }

    on sdt2 {
        address   192.168.123.206:7212;
        node-id   1;
    }

    on sdt3 {
        address   192.168.123.217:7212;
        node-id   2;
        volume 0 {
            device    minor 31;
            disk      none;
        }
    }
}`

// A node that has moved gets its own disk; the others keep the shared one,
// the tiebreaker keeps `disk none`, and the override can be taken back.
func TestHostVolumeDiskOverride(t *testing.T) {
	out, err := setHostVolumeDisk(moveTestConfig, "sdt2", 0, 31, "/dev/haify_ssd/a1_data")
	require.NoError(t, err)
	vols := parseResourceConfigVolumes(out)
	require.Len(t, vols, 1)
	assert.Equal(t, "/dev/haify_tp/a1_data", vols[0].DiskPath, "the shared path is unchanged")

	lines := strings.Split(out, "\n")
	s, e := onSectionSpan(lines, "sdt2")
	sect := strings.Join(lines[s:e+1], "\n")
	assert.Contains(t, sect, "disk      /dev/haify_ssd/a1_data;")
	assert.Contains(t, sect, "device    minor 31;")
	s1, e1 := onSectionSpan(lines, "sdt1")
	assert.NotContains(t, strings.Join(lines[s1:e1+1], "\n"), "volume")

	// Setting it again replaces rather than duplicates.
	again, err := setHostVolumeDisk(out, "sdt2", 0, 31, "/dev/haify_ssd/a1_data")
	require.NoError(t, err)
	assert.Equal(t, out, again)

	back := removeHostVolumeDisk(out, "sdt2", 0)
	assert.Equal(t, moveTestConfig, back)
	assert.Equal(t, moveTestConfig, removeHostVolumeDisk(moveTestConfig, "sdt3", 0), "a diskless `disk none` is not a moved disk")

	_, err = setHostVolumeDisk(moveTestConfig, "nosuch", 0, 31, "/dev/x")
	assert.Error(t, err)
}

func TestSetResourceVolumeDisk(t *testing.T) {
	out, err := setResourceVolumeDisk(moveTestConfig, 0, "/dev/haify_ssd/a1_data")
	require.NoError(t, err)
	assert.Equal(t, "/dev/haify_ssd/a1_data", parseResourceConfigVolumes(out)[0].DiskPath)
	assert.Contains(t, out, "rs-discard-granularity 65536;", "the disk { } options block is not the disk line")
	assert.Contains(t, out, "disk      none;", "the tiebreaker keeps its override")
	_, err = setResourceVolumeDisk(moveTestConfig, 3, "/dev/x")
	assert.Error(t, err)
}
