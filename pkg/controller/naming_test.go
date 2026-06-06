package controller

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestNormalizeManagedName(t *testing.T) {
	assert.Equal(t, "sds_data-pool", normalizeManagedName("data-pool"))
	assert.Equal(t, "sds_data-pool", normalizeManagedName("sds_data-pool"))
	assert.Equal(t, "", normalizeManagedName(""))
}

func TestNormalizeLVMPoolType(t *testing.T) {
	tests := []struct {
		input    string
		expected string
		wantErr  bool
	}{
		{input: "", expected: "vg"},
		{input: "vg", expected: "vg"},
		{input: "lvm", expected: "vg"},
		{input: "lvm-thin", expected: "thin_pool"},
		{input: "thin-pool", expected: "thin_pool"},
		{input: "thin_pool", expected: "thin_pool"},
		{input: "zfs", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			actual, err := normalizeLVMPoolType(tt.input)
			if tt.wantErr {
				assert.Error(t, err)
				return
			}
			assert.NoError(t, err)
			assert.Equal(t, tt.expected, actual)
		})
	}
}

func TestNormalizeManagedZFSPath(t *testing.T) {
	assert.Equal(t, "sds_tank/app", normalizeManagedZFSPath("tank/app"))
	assert.Equal(t, "sds_tank/app@snap1", normalizeManagedZFSPath("tank/app@snap1"))
	assert.Equal(t, "sds_tank/app@snap1", normalizeManagedZFSPath("sds_tank/app@snap1"))
}

func TestParseByteCount(t *testing.T) {
	value, err := parseByteCount("1073741824B")
	assert.NoError(t, err)
	assert.Equal(t, uint64(1073741824), value)

	value, err = parseByteCount("1073741824.00B")
	assert.NoError(t, err)
	assert.Equal(t, uint64(1073741824), value)
}

func TestParseLVMPoolLine(t *testing.T) {
	name, total, free, ok := parseLVMPoolLine("sds_data-pool|10737418240B|5368709120B")
	assert.True(t, ok)
	assert.Equal(t, "sds_data-pool", name)
	assert.Equal(t, uint64(10737418240), total)
	assert.Equal(t, uint64(5368709120), free)

	_, _, _, ok = parseLVMPoolLine("broken")
	assert.False(t, ok)
}

func TestParseZFSSnapshotLine(t *testing.T) {
	name, volume, createdAt, ok := parseZFSSnapshotLine("sds_tank/app@snap1 0B 0B 2026-03-13-23:30")
	assert.True(t, ok)
	assert.Equal(t, "snap1", name)
	assert.Equal(t, "sds_tank/app", volume)
	assert.Equal(t, "2026-03-13-23:30", createdAt)

	_, _, _, ok = parseZFSSnapshotLine("sds_tank/app@snap1 0B")
	assert.False(t, ok)
}
