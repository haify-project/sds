package controller

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/haify-project/haify/pkg/database"
)

func TestVolumeSize(t *testing.T) {
	gb, exact, err := volumeSize(VolumeSpec{SizeBytes: 10*1<<30 + 1})
	require.NoError(t, err)
	assert.EqualValues(t, 11, gb, "allocated in whole GiB, rounded up")
	assert.EqualValues(t, 10*1<<30+512, exact, "the device is a whole number of sectors")

	gb, exact, err = volumeSize(VolumeSpec{SizeGB: 4})
	require.NoError(t, err)
	assert.EqualValues(t, 4, gb)
	assert.Zero(t, exact)

	_, _, err = volumeSize(VolumeSpec{SizeGB: 1, SizeBytes: 2 << 30})
	assert.Error(t, err)
	_, _, err = volumeSize(VolumeSpec{})
	assert.Error(t, err)
}

const exactConf = `resource r {
    net {
        protocol C;
    }

    volume 0 {
        device    minor 1000;
        disk      /dev/vg0/r_data;
        meta-disk internal;
    }

    volume 1 {
        device    minor 1001;
        disk      /dev/vg0/r_vol1;
        meta-disk internal;
        disk {
            al-extents 1237;
        }
    }

    on n3 {
        node-id 2;
        volume 0 {
            disk none;
        }
    }
}
`

// The size goes into the resource-level volume block — a new disk section
// when there is none, the existing one otherwise — and is replaced, not
// repeated, on the next resize. A diskless node's override is not touched.
func TestSetVolumeSizeInConfig(t *testing.T) {
	out, err := setVolumeSizeInConfig(exactConf, 0, 1<<30)
	require.NoError(t, err)
	assert.Contains(t, out, "meta-disk internal;\n        disk {\n            size 2097152s;\n        }\n    }\n\n    volume 1")
	out, err = setVolumeSizeInConfig(out, 0, 2<<30)
	require.NoError(t, err)
	assert.Equal(t, 1, strings.Count(out, "size "), out)
	assert.Contains(t, out, "size 4194304s;")

	out, err = setVolumeSizeInConfig(exactConf, 1, 1<<30)
	require.NoError(t, err)
	assert.Contains(t, out, "disk {\n            size 2097152s;\n            al-extents 1237;")
	assert.Contains(t, out, "volume 0 {\n            disk none;", "the override is left alone")

	_, err = setVolumeSizeInConfig(exactConf, 7, 1<<30)
	assert.Error(t, err)
}

func TestGeneratedConfigCarriesTheExactSize(t *testing.T) {
	ctrl := newBasicTestController(&fakeDeploymentClient{})
	conf := ctrl.resources.generateDrbdConfig("r", 7000, []resolvedVolume{
		{id: 0, volumeName: "r_data", pool: "vg0", sizeGB: 2, exactBytes: 1536 << 20, minor: 1000},
	}, []string{"n1", "n2"}, nil, "C", "lvm", nil, nil)
	assert.Contains(t, conf, "size 3145728s;")
}

// A whole-GiB resize of an exact volume keeps it exact, at that many GiB;
// shrinking is refused.
func TestExactResizeBytes(t *testing.T) {
	ctrl := newBasicTestController(&fakeDeploymentClient{})
	ctrl.db = newTestDB(t)
	ctx := context.Background()
	require.NoError(t, ctrl.db.SaveVolume(ctx, &database.Volume{ResourceName: "r", VolumeName: "r_data", VolumeID: 0,
		Pool: "vg0", SizeGB: 2, SizeBytes: 1536 << 20}))

	b, err := ctrl.resources.exactResizeBytes(ctx, "r", 0, 3, 0)
	require.NoError(t, err)
	assert.EqualValues(t, 3<<30, b)
	_, err = ctrl.resources.exactResizeBytes(ctx, "r", 0, 2, 1<<30)
	assert.ErrorContains(t, err, "shrinking")
	b, err = ctrl.resources.exactResizeBytes(ctx, "other", 0, 3, 0)
	require.NoError(t, err)
	assert.Zero(t, b, "a whole-GiB volume stays whole-GiB")
}
