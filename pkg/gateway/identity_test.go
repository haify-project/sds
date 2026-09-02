package gateway

import (
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Tests for the identifiers a promoter config embeds. What they check is
// stability and uniqueness, not format: a serial or FSID that changes between
// two calls with the same inputs is what breaks a client across a failover.

func TestGenerateUUID(t *testing.T) {
	uuid1 := generateUUID()
	uuid2 := generateUUID()

	// UUIDs should be unique
	assert.NotEqual(t, uuid1, uuid2)

	// UUID should have correct format: xxxxxxxx-xxxx-xxxx-xxxx-xxxxxxxxxxxx
	assert.Len(t, uuid1, 36)
	assert.Equal(t, 4, strings.Count(uuid1, "-"))

	// Verify version 4 UUID
	parts := strings.Split(uuid1, "-")
	require.Len(t, parts, 5)
	// Version nibble should be 4 (position 12 in the UUID string, which is parts[2][0])
	assert.Equal(t, "4", string(parts[2][0]))
	// Variant nibble should be 8, 9, a, or b (position 16 in UUID, parts[3][0])
	variant := string(parts[3][0])
	assert.Contains(t, "89ab", variant)
}

func TestGenerateFSID(t *testing.T) {
	resourceUUID := "12345678-1234-1234-1234-123456789abc"
	volumeUUID := "87654321-4321-4321-4321-cba987654321"

	fsid := generateFSID(resourceUUID, volumeUUID)

	// FSID should have UUID format
	assert.Len(t, fsid, 36)
	assert.Equal(t, 4, strings.Count(fsid, "-"))

	// Same inputs should produce same FSID
	fsid2 := generateFSID(resourceUUID, volumeUUID)
	assert.Equal(t, fsid, fsid2)

	// Different inputs should produce different FSID
	fsid3 := generateFSID("different", volumeUUID)
	assert.NotEqual(t, fsid, fsid3)
}

func TestGenerateSerialFromIQN(t *testing.T) {
	tests := []struct {
		iqn          string
		volumeNumber int
	}{
		{"iqn.2024-01.com.example:sds.data", 0},
		{"iqn.2024-01.com.example:sds.data", 1},
		{"iqn.2024-01.com.example:storage", 0},
	}

	results := make(map[string]bool)
	for _, tt := range tests {
		serial := generateSerialFromIQN(tt.iqn, tt.volumeNumber)
		// Serial should be 16 hex characters (8 bytes)
		assert.Len(t, serial, 16)
		// Should only contain hex characters
		for _, c := range serial {
			assert.Contains(t, "0123456789abcdef", string(c))
		}
		// Each combination should produce unique serial
		key := fmt.Sprintf("%s-%d", tt.iqn, tt.volumeNumber)
		assert.False(t, results[key], "serial should be unique for each key")
		results[key] = true
	}
}
