package controller

import (
	"context"
	"strings"
	"testing"

	"github.com/liliang-cn/sds/pkg/deployment"
	"github.com/liliang-cn/sds/pkg/gateway"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

func newBasicTestController(dep deploymentClient) *Controller {
	ctrl := &Controller{
		logger:     zap.NewNop(),
		deployment: dep,
		hosts:      []string{},
		hostsMap:   make(map[string]string),
	}
	ctrl.nodes = NewNodeManager(ctrl)
	ctrl.storage = NewStorageManager(ctrl)
	ctrl.resources = NewResourceManager(ctrl)
	ctrl.snapshots = NewSnapshotManager(ctrl)
	ctrl.resources.SetDeployment(dep)
	ctrl.gateway = gateway.New(nil, nil, zap.NewNop(), nil)
	ctrl.schedules = NewScheduleManager(ctrl)
	ctrl.backups = NewBackupManager(ctrl)
	return ctrl
}

func TestNodeManagerRegisterAndUnregister(t *testing.T) {
	dep := &fakeDeploymentClient{
		execFunc: func(ctx context.Context, hosts []string, cmd string, opts ...deployment.ExecOption) (*deployment.ExecResult, error) {
			if cmd == "hostname" {
				return successExecResult(hosts, "node1.local\n"), nil
			}
			if strings.Contains(cmd, "os-release") {
				return successExecResult(hosts, "PRETTY_NAME=\"TestOS 1.2\"\n"), nil
			}
			return successExecResult(hosts, ""), nil
		},
	}
	ctrl := newBasicTestController(dep)

	node, err := ctrl.nodes.RegisterNode(context.Background(), "node1", "10.0.0.1")
	require.NoError(t, err)
	assert.Equal(t, "node1.local", node.Hostname)
	assert.Equal(t, "TestOS 1.2", node.Version)
	assert.Equal(t, "10.0.0.1", ctrl.ResolveHost("node1"))
	assert.Equal(t, "10.0.0.1", ctrl.ResolveHost("node1.local"))
	assert.Contains(t, ctrl.GetHosts(), "10.0.0.1")
	assert.Equal(t, []string{"10.0.0.1"}, ctrl.gateway.Hosts())

	listed, err := ctrl.nodes.ListNodes(context.Background())
	require.NoError(t, err)
	assert.Len(t, listed, 1)

	byName, err := ctrl.nodes.GetNode(context.Background(), "node1")
	require.NoError(t, err)
	assert.Equal(t, "10.0.0.1", byName.Address)
	byHostname, err := ctrl.nodes.GetNode(context.Background(), "node1.local")
	require.NoError(t, err)
	assert.Equal(t, "10.0.0.1", byHostname.Address)

	require.NoError(t, ctrl.nodes.UnregisterNode(context.Background(), "node1"))
	listed, err = ctrl.nodes.ListNodes(context.Background())
	require.NoError(t, err)
	assert.Empty(t, listed)
	assert.Equal(t, "node1", ctrl.ResolveHost("node1"))
	assert.Equal(t, "node1.local", ctrl.ResolveHost("node1.local"))
	assert.NotContains(t, ctrl.GetHosts(), "10.0.0.1")
	assert.Empty(t, ctrl.gateway.Hosts())
	_, err = ctrl.nodes.GetNode(context.Background(), "10.0.0.1")
	assert.ErrorContains(t, err, "node not found")
}

func TestParseNodeEnvironmentVersion(t *testing.T) {
	assert.Equal(t, "Debian GNU/Linux 12 (bookworm)", parseNodeEnvironmentVersion("PRETTY_NAME=\"Debian GNU/Linux 12 (bookworm)\"\nNAME=\"Debian GNU/Linux\"\nVERSION_ID=\"12\"\n"))
	assert.Equal(t, "6.12.0-custom", parseNodeEnvironmentVersion("6.12.0-custom"))
	assert.Equal(t, "unknown", parseNodeEnvironmentVersion(""))
}

func TestParseRoleFromStatusUsesLocalRole(t *testing.T) {
	output := "res1 role:Secondary\n  node1 role:Primary\n"
	assert.Equal(t, "Secondary", parseRoleFromStatus(output))
}

func TestParseRoleFromStatusSkipsVerboseCommandEcho(t *testing.T) {
	// "drbdadm status --verbose" echoes the underlying drbdsetup command on
	// the first line; the local resource line follows it.
	output := "drbdsetup status res1 --verbose\nres1 node-id:0 role:Primary suspended:no force-io-failures:no\n  volume:0 minor:0 disk:UpToDate backing_dev:/dev/vg0/res1_data quorum:yes\n      open:yes blocked:no\n  node2 node-id:1 connection:Connected role:Secondary tls:no congested:no\n    volume:0 replication:Established peer-disk:UpToDate resync-suspended:no\n"
	assert.Equal(t, "Primary", parseRoleFromStatus(output))
}

func TestParseNodeStatesFromVerboseStatusWithCommandEcho(t *testing.T) {
	output := "drbdsetup status res1 --verbose\n" +
		"res1 node-id:0 role:Secondary suspended:no force-io-failures:no\n" +
		"  volume:0 minor:0 disk:UpToDate backing_dev:/dev/vg0/res1_data quorum:yes\n" +
		"      open:no blocked:no\n" +
		"  node2 node-id:1 connection:Connected role:Secondary tls:no congested:no\n" +
		"      ap-in-flight:0 rs-in-flight:0\n" +
		"    volume:0 replication:Established peer-disk:UpToDate resync-suspended:no\n" +
		"  node3 node-id:2 connection:Connected role:Primary tls:no congested:no\n" +
		"      ap-in-flight:0 rs-in-flight:0\n" +
		"    volume:0 replication:Established peer-disk:UpToDate resync-suspended:no\n"
	states := parseNodeStatesFromStatus(output, []string{"node1", "node2", "node3"})
	require.Len(t, states, 3)
	assert.Equal(t, "Secondary", states["node1"].Role)
	assert.Equal(t, "UpToDate", states["node1"].DiskState)
	assert.Equal(t, "Secondary", states["node2"].Role)
	assert.Equal(t, "UpToDate", states["node2"].DiskState)
	assert.Equal(t, "Primary", states["node3"].Role)
	assert.Equal(t, "UpToDate", states["node3"].DiskState)
}

func TestParseNodeStatesFromVerboseStatus(t *testing.T) {
	output := "res1 role:Primary suspended:no\n  volume:0 minor:1 disk:UpToDate\n  node2 connection:Connected role:Secondary\n    volume:0 replication:Established peer-disk:UpToDate peer-client:no\n"
	states := parseNodeStatesFromStatus(output, []string{"node1", "node2"})
	require.Len(t, states, 2)
	assert.Equal(t, "Primary", states["node1"].Role)
	assert.Equal(t, "UpToDate", states["node1"].DiskState)
	assert.Equal(t, "Secondary", states["node2"].Role)
	assert.Equal(t, "UpToDate", states["node2"].DiskState)
}

func TestParseNodeStatesIgnoresDisklessTiebreakerPeer(t *testing.T) {
	// Real output from the diskful Primary of a 2-diskful + 1-tiebreaker
	// resource. The tiebreaker (orange3) is NOT in nodeAddresses (it lives in
	// DisklessNodes), so its "peer-disk:Diskless" line must not be attributed
	// to the preceding diskful node (orange2).
	output := "data role:Primary\n" +
		"  disk:UpToDate open:no\n" +
		"  orange2 role:Secondary\n" +
		"    peer-disk:UpToDate\n" +
		"  orange3 role:Secondary\n" +
		"    peer-disk:Diskless peer-client:yes\n"
	states := parseNodeStatesFromStatus(output, []string{"orange1", "orange2"})
	require.Len(t, states, 2)
	assert.Equal(t, "UpToDate", states["orange1"].DiskState)
	assert.Equal(t, "Secondary", states["orange2"].Role)
	// Must stay UpToDate — the tiebreaker's Diskless must not bleed onto orange2.
	assert.Equal(t, "UpToDate", states["orange2"].DiskState)
}

func TestParseVolumesFromVerboseStatus(t *testing.T) {
	output := "res1 role:Primary suspended:no\n  volume:0 minor:7 disk:UpToDate size:6291456\n  volume:1 minor:8 disk:UpToDate size:1048576\n  node2 connection:Connected role:Secondary\n    volume:0 replication:Established peer-disk:UpToDate\n"
	volumes := parseVolumesFromStatus(output)
	require.Len(t, volumes, 2)
	assert.Equal(t, 0, volumes[0].id)
	assert.Equal(t, "/dev/drbd7", volumes[0].device)
	assert.Equal(t, 1, volumes[1].id)
	assert.Equal(t, "/dev/drbd8", volumes[1].device)
}

func TestNodeHealthCheckUsesAddressForKnownNode(t *testing.T) {
	dep := &fakeDeploymentClient{
		execFunc: func(ctx context.Context, hosts []string, cmd string, opts ...deployment.ExecOption) (*deployment.ExecResult, error) {
			switch {
			case strings.Contains(cmd, "drbdadm --version"):
				require.Equal(t, []string{"10.0.0.1"}, hosts)
				return successExecResult(hosts, "DRBDADM_VERSION=9.2.12\n"), nil
			case strings.Contains(cmd, "drbd-reactor --version"):
				require.Equal(t, []string{"10.0.0.1"}, hosts)
				return successExecResult(hosts, "drbd-reactor 1.8.0\n"), nil
			case strings.Contains(cmd, "systemctl is-active drbd-reactor"):
				require.Equal(t, []string{"10.0.0.1"}, hosts)
				return successExecResult(hosts, "active\n"), nil
			case strings.Contains(cmd, "find /usr/lib/ocf/resource.d"):
				require.Equal(t, []string{"10.0.0.1"}, hosts)
				return successExecResult(hosts, "Filesystem\nIPaddr2\n"), nil
			default:
				return successExecResult(hosts, ""), nil
			}
		},
	}
	ctrl := newBasicTestController(dep)
	ctrl.nodes.nodes["10.0.0.1"] = &NodeInfo{Name: "node1", Address: "10.0.0.1", Hostname: "node1.local"}
	ctrl.hostsMap["node1"] = "10.0.0.1"
	ctrl.hostsMap["node1.local"] = "10.0.0.1"

	health, err := ctrl.nodes.HealthCheck(context.Background(), "node1")
	require.NoError(t, err)
	assert.True(t, health.DrbdInstalled)
	assert.Equal(t, "9.2.12", health.DrbdVersion)
	assert.True(t, health.DrbdReactorInstalled)
	assert.Equal(t, "1.8.0", health.DrbdReactorVersion)
	assert.True(t, health.DrbdReactorRunning)
	assert.Equal(t, []string{"Filesystem", "IPaddr2"}, health.AvailableAgents)
}

// A peer whose link is down prints its connection state and nothing else — no
// role, no peer-disk. Recording it is what lets everything downstream tell
// "this peer is not Primary" apart from "nobody asked this peer".
//
// The output below is verbatim from lima-sds-a on 2026-09-21, seconds after it
// booted back into the cluster. Parsed without the connection lines it yields
// exactly one node state, and a resource whose Primary was serving the whole
// time reads as a resource with no Primary at all.
func TestPeersThatAreNotConnectedAreStillRecorded(t *testing.T) {
	output := `openclaw role:Secondary
  disk:Outdated quorum:no open:no
  iZ2vca1rjuuxbqtpm9hy7zZ connection:Connecting
  sds-b connection:Connecting
  sds-e connection:Connecting`

	states := parseNodeStatesFromStatus(output, []string{"lima-sds-a", "sds-b", "sds-e", "iZ2vca1rjuuxbqtpm9hy7zZ"})

	if len(states) != 4 {
		t.Fatalf("got %d node states, want 4 — an unreachable peer that is absent from the map is a peer nobody checks: %+v", len(states), states)
	}
	if got := states["lima-sds-a"]; got == nil || got.Role != "Secondary" || got.DiskState != "Outdated" {
		t.Errorf("answering node = %+v, want the locally read Secondary/Outdated", got)
	}
	if got := states["lima-sds-a"]; got != nil && got.Connection != "" {
		t.Errorf("the answering node has no connection to describe, got %q", got.Connection)
	}
	for _, peer := range []string{"sds-b", "sds-e", "iZ2vca1rjuuxbqtpm9hy7zZ"} {
		st := states[peer]
		if st == nil {
			t.Fatalf("peer %s is missing from the parsed states", peer)
		}
		if st.Connection != "Connecting" {
			t.Errorf("peer %s connection = %q, want Connecting", peer, st.Connection)
		}
		if st.Role != "" {
			t.Errorf("peer %s role = %q; DRBD reported none, and inventing one is the bug", peer, st.Role)
		}
	}
}

// StandAlone is the state DRBD parks a peer in after refusing to resolve a
// split brain. It has to survive the parse for anything to alert on it.
func TestStandAlonePeerIsRecorded(t *testing.T) {
	output := `openclaw role:Primary
  disk:UpToDate open:yes
  lima-sds-a connection:StandAlone
  sds-b role:Secondary
    peer-disk:UpToDate`

	states := parseNodeStatesFromStatus(output, []string{"sds-e", "lima-sds-a", "sds-b"})

	if got := states["lima-sds-a"]; got == nil || got.Connection != "StandAlone" {
		t.Fatalf("lima-sds-a = %+v, want Connection StandAlone", got)
	}
	// The peer-disk line after it belongs to sds-b, not to the StandAlone peer.
	if got := states["lima-sds-a"]; got != nil && got.DiskState != "" {
		t.Errorf("StandAlone peer picked up a disk state %q from a following line", got.DiskState)
	}
	if got := states["sds-b"]; got == nil || got.Role != "Secondary" || got.DiskState != "UpToDate" {
		t.Errorf("sds-b = %+v, want Secondary/UpToDate", got)
	}
}

// The JSON path has to carry the connection state too, and this test exists
// because the text parser alone did not catch it.
//
// GetResource prefers `drbdsetup status --json` and replaces the text-parsed
// states with it wholesale. So a Connection recorded only by the text parser
// is silently discarded on every cluster new enough to have --json — which is
// every cluster this runs on. The first cut of this fix did exactly that: unit
// tests green, and a live replica disconnected for ninety seconds still raised
// nothing.
//
// The shape below is verbatim drbdsetup output: a peer whose link is down has
// a connection-state and nothing else — no peer-role, no peer_devices.
func TestJSONParseCarriesPeerConnectionState(t *testing.T) {
	output := `[{
	  "name": "openclaw",
	  "role": "Secondary",
	  "devices": [{"volume": 0, "disk-state": "Inconsistent", "quorum": true}],
	  "connections": [
	    {"name": "iZ2vca1rjuuxbqtpm9hy7zZ", "connection-state": "Connected", "peer-role": "Secondary",
	     "peer_devices": [{"volume": 0, "replication-state": "SyncTarget", "peer-disk-state": "UpToDate", "done": 43.13}]},
	    {"name": "sds-b", "connection-state": "Connecting", "peer-role": "Unknown", "peer_devices": []},
	    {"name": "sds-e", "connection-state": "Connected", "peer-role": "Primary",
	     "peer_devices": [{"volume": 0, "replication-state": "PausedSyncT", "peer-disk-state": "UpToDate"}]}
	  ]
	}]`

	states, err := parseNodeStatesFromJSON(output, "lima-sds-a")
	if err != nil {
		t.Fatalf("parseNodeStatesFromJSON: %v", err)
	}

	if got := states["sds-b"]; got == nil || got.Connection != "Connecting" {
		t.Fatalf("sds-b = %+v, want Connection Connecting — without it a disconnected replica is invisible", got)
	}
	if got := states["sds-e"]; got == nil || got.Connection != "Connected" || got.Role != "Primary" {
		t.Errorf("sds-e = %+v, want a Connected Primary", got)
	}
	// The answering node has no connection to itself to describe.
	if got := states["lima-sds-a"]; got == nil || got.Connection != "" {
		t.Errorf("answering node = %+v, want an empty Connection", got)
	}
}
