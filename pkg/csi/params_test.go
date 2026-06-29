package csi

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseVolumeParamsDefaults(t *testing.T) {
	p, err := ParseVolumeParams(map[string]string{"pool": "vg0"})
	require.NoError(t, err)
	assert.Equal(t, "vg0", p.Pool)
	assert.Equal(t, 2, p.Replicas)
	assert.Equal(t, "lvm", p.StorageType)
}

func TestParseVolumeParamsExplicit(t *testing.T) {
	p, err := ParseVolumeParams(map[string]string{"pool": "tank", "replicas": "3", "storageType": "zfs"})
	require.NoError(t, err)
	assert.Equal(t, 3, p.Replicas)
	assert.Equal(t, "zfs", p.StorageType)
}

func TestParseVolumeParamsErrors(t *testing.T) {
	_, err := ParseVolumeParams(map[string]string{})
	assert.Error(t, err, "missing pool")
	_, err = ParseVolumeParams(map[string]string{"pool": "vg0", "replicas": "x"})
	assert.Error(t, err, "bad replicas")
	_, err = ParseVolumeParams(map[string]string{"pool": "vg0", "storageType": "btrfs"})
	assert.Error(t, err, "bad storageType")
}
