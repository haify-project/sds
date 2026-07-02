package controller

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// midSyncStatusJSON is a realistic `drbdsetup status data --json` capture with
// orange1 Primary/UpToDate, a SyncTarget peer (orange2) resyncing at 42.3%, and
// an Established diskless tiebreaker (orange3) whose "done" field is absent
// (steady state) and therefore treated as 100%.
const midSyncStatusJSON = `[
  {
    "name": "data",
    "node-id": 0,
    "role": "Primary",
    "suspended": false,
    "write-ordering": "flush",
    "devices": [
      {
        "volume": 0,
        "minor": 0,
        "disk-state": "UpToDate",
        "client": false,
        "quorum": true,
        "size": 1048576
      }
    ],
    "connections": [
      {
        "peer-node-id": 1,
        "name": "orange2",
        "connection-state": "Connected",
        "congested": false,
        "peer-role": "Secondary",
        "peer_devices": [
          {
            "volume": 0,
            "replication-state": "SyncTarget",
            "peer-disk-state": "Inconsistent",
            "peer-client": false,
            "resync-suspended": "no",
            "received": 100,
            "sent": 0,
            "out-of-sync": 200,
            "pending": 0,
            "unacked": 0,
            "done": 42.3
          }
        ]
      },
      {
        "peer-node-id": 2,
        "name": "orange3",
        "connection-state": "Connected",
        "congested": false,
        "peer-role": "Secondary",
        "peer_devices": [
          {
            "volume": 0,
            "replication-state": "Established",
            "peer-disk-state": "Diskless",
            "peer-client": true,
            "resync-suspended": "no",
            "received": 0,
            "sent": 0,
            "out-of-sync": 0
          }
        ]
      }
    ]
  }
]`

// steadyStateStatusJSON has no resync in progress: orange2 Established/UpToDate
// and orange3 an Established diskless tiebreaker.
const steadyStateStatusJSON = `[
  {
    "name": "data",
    "node-id": 0,
    "role": "Primary",
    "devices": [ { "volume": 0, "minor": 0, "disk-state": "UpToDate" } ],
    "connections": [
      {
        "name": "orange2",
        "peer-role": "Secondary",
        "peer_devices": [
          { "volume": 0, "replication-state": "Established", "peer-disk-state": "UpToDate" }
        ]
      },
      {
        "name": "orange3",
        "peer-role": "Secondary",
        "peer_devices": [
          { "volume": 0, "replication-state": "Established", "peer-disk-state": "Diskless", "peer-client": true }
        ]
      }
    ]
  }
]`

func TestParseNodeStatesFromJSONMidSync(t *testing.T) {
	states, err := parseNodeStatesFromJSON(midSyncStatusJSON, "orange1")
	require.NoError(t, err)
	require.Len(t, states, 3)

	// Local node: role + disk from the top-level resource, no replication and
	// treated as fully in sync.
	local := states["orange1"]
	require.NotNil(t, local)
	assert.Equal(t, "Primary", local.Role)
	assert.Equal(t, "UpToDate", local.DiskState)
	assert.Empty(t, local.Replication)
	assert.Equal(t, 100.0, local.SyncPercent)

	// SyncTarget peer mid-resync.
	orange2 := states["orange2"]
	require.NotNil(t, orange2)
	assert.Equal(t, "Secondary", orange2.Role)
	assert.Equal(t, "SyncTarget", orange2.Replication)
	assert.Equal(t, "Inconsistent", orange2.DiskState)
	assert.InDelta(t, 42.3, orange2.SyncPercent, 0.0001)

	// Established diskless tiebreaker: "done" absent -> fully in sync.
	orange3 := states["orange3"]
	require.NotNil(t, orange3)
	assert.Equal(t, "Established", orange3.Replication)
	assert.Equal(t, "Diskless", orange3.DiskState)
	assert.Equal(t, 100.0, orange3.SyncPercent)
}

func TestParseNodeStatesFromJSONSteadyState(t *testing.T) {
	states, err := parseNodeStatesFromJSON(steadyStateStatusJSON, "orange1")
	require.NoError(t, err)
	require.Len(t, states, 3)

	assert.Equal(t, "Primary", states["orange1"].Role)
	assert.Equal(t, "UpToDate", states["orange1"].DiskState)

	orange2 := states["orange2"]
	require.NotNil(t, orange2)
	assert.Equal(t, "Established", orange2.Replication)
	assert.Equal(t, "UpToDate", orange2.DiskState)
	assert.Equal(t, 100.0, orange2.SyncPercent)

	orange3 := states["orange3"]
	require.NotNil(t, orange3)
	assert.Equal(t, "Diskless", orange3.DiskState)
	assert.Equal(t, "Established", orange3.Replication)
}

// percent-in-sync is accepted as an alias for the resync percentage so the
// parser is robust across drbd versions that spell the field differently.
func TestParseNodeStatesFromJSONPercentInSyncAlias(t *testing.T) {
	const j = `[
  {
    "name": "data",
    "role": "Secondary",
    "devices": [ { "volume": 0, "disk-state": "Inconsistent" } ],
    "connections": [
      {
        "name": "orange1",
        "peer-role": "Primary",
        "peer_devices": [
          { "volume": 0, "replication-state": "SyncTarget", "peer-disk-state": "UpToDate", "percent-in-sync": 3.55 }
        ]
      }
    ]
  }
]`
	states, err := parseNodeStatesFromJSON(j, "orange2")
	require.NoError(t, err)
	require.Len(t, states, 2)
	assert.InDelta(t, 3.55, states["orange1"].SyncPercent, 0.0001)
	assert.Equal(t, "SyncTarget", states["orange1"].Replication)
}

func TestParseNodeStatesFromJSONFallback(t *testing.T) {
	// Invalid / empty JSON must return an error so callers fall back to the
	// text parser.
	for _, in := range []string{"", "   ", "not json", "{}", "[]"} {
		_, err := parseNodeStatesFromJSON(in, "orange1")
		assert.Error(t, err, "input %q should be a parse/empty error", in)
	}
}
