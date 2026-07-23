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

func TestParseVolumeParamsProfileAllowsOmittedPool(t *testing.T) {
	p, err := ParseVolumeParams(map[string]string{"resourceProfile": "production"})
	require.NoError(t, err)
	assert.Equal(t, "production", p.ResourceProfile)
	assert.Empty(t, p.Pool)
}

func TestParseVolumeParamsExplicit(t *testing.T) {
	p, err := ParseVolumeParams(map[string]string{
		"pool": "tank", "replicas": "3", "storageType": "zfs",
		"resourceProfile": "production", "resourceLabels": "app=postgres, env=prod",
	})
	require.NoError(t, err)
	assert.Equal(t, 3, p.Replicas)
	assert.Equal(t, "zfs", p.StorageType)
	assert.Equal(t, "production", p.ResourceProfile)
	assert.Equal(t, map[string]string{"app": "postgres", "env": "prod"}, p.ResourceLabels)
}

func TestParseVolumeParamsErrors(t *testing.T) {
	_, err := ParseVolumeParams(map[string]string{})
	assert.Error(t, err, "missing pool")
	_, err = ParseVolumeParams(map[string]string{"pool": "vg0", "replicas": "x"})
	assert.Error(t, err, "bad replicas")
	_, err = ParseVolumeParams(map[string]string{"pool": "vg0", "storageType": "btrfs"})
	assert.Error(t, err, "bad storageType")
	_, err = ParseVolumeParams(map[string]string{"pool": "vg0", "resourceLabels": "missing-value"})
	assert.Error(t, err, "bad resourceLabels")
}
