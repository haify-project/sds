package controller

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// settledVolumeStatus answers a fake's `drbdsetup status <res> --json` with a
// resource whose peers have all settled, for tests that add a volume.
func settledVolumeStatus(cmd string) (string, bool) {
	if !strings.Contains(cmd, "drbdsetup status") || !strings.Contains(cmd, "--json") {
		return "", false
	}
	return `[{"name":"r","role":"Secondary","devices":[],"connections":[]}]`, true
}

const settleStatus = `[{"name":"files","role":"Primary","devices":[{"volume":1,"disk-state":"UpToDate"}],
 "connections":[
  {"name":"sdt2","connection-state":"Connected","peer_devices":[{"volume":0,"replication-state":"Established","peer-disk-state":"UpToDate"},
                                                               {"volume":1,"replication-state":"Established","peer-disk-state":"%s"}]},
  {"name":"sdt3","connection-state":"Connected","peer_devices":[{"volume":1,"replication-state":"Established","peer-disk-state":"Diskless"}]},
  {"name":"sdt4","connection-state":"%s","peer_devices":[]}]}]`

func statusWith(sdt2Disk, sdt4Conn string) string {
	return strings.Replace(strings.Replace(settleStatus, "%s", sdt2Disk, 1), "%s", sdt4Conn, 1)
}

// A peer still negotiating its disk, or not connected, has not settled; a
// diskless peer has. Only the asked-for volume counts.
func TestNewVolumePeerStates(t *testing.T) {
	peers, err := parseVolumePeerStates(statusWith("Negotiating", "Connecting"), 1)
	require.NoError(t, err)
	assert.Equal(t, []string{"sdt2 (Established/Negotiating)", "sdt4 (Connecting/DUnknown)"}, unsettledPeers(peers))

	peers, err = parseVolumePeerStates(statusWith("Inconsistent", "Connected"), 1)
	require.NoError(t, err)
	assert.Equal(t, []string{"sdt4 (Connected/DUnknown)"}, unsettledPeers(peers),
		"a connected peer that reports no device for the volume has not attached it")

	peers, err = parseVolumePeerStates(strings.Replace(statusWith("Inconsistent", "Connected"), `,
  {"name":"sdt4","connection-state":"Connected","peer_devices":[]}`, "", 1), 1)
	require.NoError(t, err)
	assert.Empty(t, unsettledPeers(peers), "Inconsistent after the handshake is settled")
	assert.Equal(t, []string{"sdt2 (Established/Inconsistent)"}, peersNotUpToDate(peers),
		"but not UpToDate: the stall the bitmap clear can leave behind")

	_, err = parseVolumePeerStates("", 1)
	assert.Error(t, err)
}
