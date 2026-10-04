package controller

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/haify-project/sds/pkg/deployment"
)

const lostHost = "192.168.123.228" // node-d, sds-d, node-id 3 in threeReplicaConfig

// What a survivor sees when sds-d is gone: the connection to it down, its own
// copy UpToDate, quorum held with sds-b.
const survivorSeesDGone = `sds-meta role:Secondary
  disk:UpToDate open:no
  sds-b role:Primary
    peer-disk:UpToDate
  sds-d connection:Connecting
`

type lostFixture struct {
	ctrl        *Controller
	ran         []string // "host: cmd"
	distributed [][]string
	status      string
	answers     bool
}

func newLostFixture(t *testing.T, nodes string) *lostFixture {
	t.Helper()
	f := &lostFixture{status: survivorSeesDGone}
	dep := &fakeDeploymentClient{}
	dep.execFunc = func(_ context.Context, hosts []string, cmd string, _ ...deployment.ExecOption) (*deployment.ExecResult, error) {
		if hosts[0] == lostHost && !f.answers {
			f.ran = append(f.ran, hosts[0]+": "+cmd)
			return nil, errors.New("ssh: connect to host 192.168.123.228 port 22: No route to host")
		}
		if strings.HasPrefix(cmd, "cat /etc/drbd.d/") {
			return successExecResult(hosts, threeReplicaConfig), nil
		}
		for _, h := range hosts {
			f.ran = append(f.ran, h+": "+cmd)
		}
		return successExecResult(hosts, ""), nil
	}
	dep.drbdStatusFunc = func(_ context.Context, hosts []string, _ string) (*deployment.ExecResult, error) {
		return successExecResult(hosts, f.status), nil
	}
	dep.distributeConfigFunc = func(_ context.Context, hosts []string, _, path string, _ ...deployment.ConfigOption) (*deployment.ConfigResult, error) {
		f.distributed = append(f.distributed, append([]string{}, hosts...))
		return &deployment.ConfigResult{Success: true, Path: path}, nil
	}
	f.ctrl = removeTestController(t, dep, nodes, "")
	// As registration records them: the DRBD name is the node's hostname.
	for addr, host := range map[string]string{"192.168.123.227": "sds-b", "192.168.123.212": "sds-e", lostHost: "sds-d"} {
		f.ctrl.nodes.nodes[addr].Hostname = host
		f.ctrl.hostsMap[host] = addr
	}
	return f
}

func (f *lostFixture) on(host string) []string {
	var out []string
	for _, r := range f.ran {
		if strings.HasPrefix(r, host+": ") {
			out = append(out, strings.TrimPrefix(r, host+": "))
		}
	}
	return out
}

// A dead node could not be removed at all: every step ran something on it.
// Now nothing does, its slot is freed on the survivors, and its storage is
// left for whoever brings it back.
func TestRemoveLostReplicaRunsNothingOnTheLostNode(t *testing.T) {
	f := newLostFixture(t, "node-b,node-e,node-d")

	require.NoError(t, f.ctrl.resources.RemoveReplicaOptions(context.Background(), "sds-meta", "node-d", true))

	assert.Equal(t, []string{"true"}, f.on(lostHost), "the lost node is only probed, never acted on")
	for _, hosts := range f.distributed {
		assert.NotContains(t, hosts, lostHost)
	}
	for _, h := range []string{"192.168.123.227", "192.168.123.212"} {
		assert.Contains(t, f.on(h), "sudo drbdadm adjust sds-meta")
		assert.Contains(t, f.on(h), "sudo drbdsetup forget-peer sds-meta 3", "the dead peer's bitmap slot is freed")
	}
	for _, r := range f.ran {
		assert.NotContains(t, r, "lvremove", "no storage is touched anywhere")
	}
	res, err := f.ctrl.db.GetResource(context.Background(), "sds-meta")
	require.NoError(t, err)
	assert.Equal(t, "node-b,node-e", res.Nodes)
}

// Its copy is already gone, so removing it from two leaves one: allowed.
func TestRemoveLostReplicaMayLeaveOneCopy(t *testing.T) {
	f := newLostFixture(t, "node-b,node-d")
	require.NoError(t, f.ctrl.resources.RemoveReplicaOptions(context.Background(), "sds-meta", "node-d", true))

	f = newLostFixture(t, "node-b,node-d")
	f.answers = true
	err := f.ctrl.resources.RemoveReplicaOptions(context.Background(), "sds-meta", "node-d", false)
	require.Error(t, err, "without --lost the floor stays at two")
}

func TestRemoveLostReplicaRefusesANodeThatAnswers(t *testing.T) {
	f := newLostFixture(t, "node-b,node-e,node-d")
	f.answers = true
	err := f.ctrl.resources.RemoveReplicaOptions(context.Background(), "sds-meta", "node-d", true)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "answers over SSH")
	assert.Empty(t, f.distributed)
}

// Cut off from the controller is not gone: it may still be replicating.
func TestRemoveLostReplicaRefusesANodeStillConnectedOverDRBD(t *testing.T) {
	f := newLostFixture(t, "node-b,node-e,node-d")
	f.status = strings.Replace(survivorSeesDGone, "sds-d connection:Connecting", "sds-d role:Secondary\n    peer-disk:UpToDate", 1)
	err := f.ctrl.resources.RemoveReplicaOptions(context.Background(), "sds-meta", "node-d", true)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "still connected")
	assert.Empty(t, f.distributed)
}

// Without quorum the survivors may be the minority; dropping the absent vote
// is exactly what would let them carry on as if they were the cluster.
func TestRemoveLostReplicaRefusesSurvivorsWithoutQuorum(t *testing.T) {
	f := newLostFixture(t, "node-b,node-e,node-d")
	f.status = strings.Replace(survivorSeesDGone, "disk:UpToDate open:no", "disk:UpToDate open:no quorum:no", 1)
	err := f.ctrl.resources.RemoveReplicaOptions(context.Background(), "sds-meta", "node-d", true)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no quorum")
	assert.Contains(t, err.Error(), "set-tiebreaker")
	assert.Empty(t, f.distributed)
}

// A normal removal now frees the slot too, and reaches the tiebreaker.
func TestRemoveReplicaForgetsThePeerAndUpdatesTheTiebreaker(t *testing.T) {
	f := newLostFixture(t, "node-b,node-e,node-d")
	f.answers = true
	registerNodes(f.ctrl, map[string]string{"node-t": "192.168.123.230"})
	res, err := f.ctrl.db.GetResource(context.Background(), "sds-meta")
	require.NoError(t, err)
	res.DisklessNodes = "node-t"
	require.NoError(t, f.ctrl.db.SaveResource(context.Background(), res))

	require.NoError(t, f.ctrl.resources.RemoveReplicaOptions(context.Background(), "sds-meta", "node-d", false))

	assert.Contains(t, f.on("192.168.123.227"), "sudo drbdsetup forget-peer sds-meta 3")
	assert.Contains(t, f.on("192.168.123.230"), "sudo drbdadm adjust sds-meta", "the tiebreaker drops the member too")
	assert.NotContains(t, f.on("192.168.123.230"), "sudo drbdsetup forget-peer sds-meta 3", "a diskless node has no bitmap")
	found := false
	for _, hosts := range f.distributed {
		for _, h := range hosts {
			found = found || h == "192.168.123.230"
		}
	}
	assert.True(t, found, "the tiebreaker gets the new config")
}

// A dead member no longer blocks moving the tiebreaker — the way to give the
// survivors back their quorum.
func TestSetTiebreakerSkipsAMemberThatIsGone(t *testing.T) {
	f := newLostFixture(t, "node-b,node-e,node-d")
	registerNodes(f.ctrl, map[string]string{"node-t": "192.168.123.230"})

	require.NoError(t, f.ctrl.resources.SetTiebreaker(context.Background(), "sds-meta", "node-t"))

	assert.Equal(t, []string{"true"}, f.on(lostHost))
	assert.Contains(t, f.on("192.168.123.230"), "sudo drbdadm adjust sds-meta")

	// Still connected over DRBD: not gone, so refused.
	f = newLostFixture(t, "node-b,node-e,node-d")
	registerNodes(f.ctrl, map[string]string{"node-t": "192.168.123.230"})
	f.status = strings.Replace(survivorSeesDGone, "sds-d connection:Connecting", "sds-d role:Secondary", 1)
	err := f.ctrl.resources.SetTiebreaker(context.Background(), "sds-meta", "node-t")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "still connected")
}

func TestParseSurvivorViewReadsBothStatusFormats(t *testing.T) {
	verbose := `drbdsetup status sds-meta --verbose
sds-meta node-id:0 role:Secondary suspended:no
  volume:0 minor:3 disk:UpToDate quorum:yes blocked:no
  sds-e node-id:1 connection:Connected role:Primary congested:no
    volume:0 replication:Established peer-disk:UpToDate
  sds-d node-id:3 connection:Connecting
`
	v := parseSurvivorView(verbose)
	assert.True(t, v.quorum)
	assert.True(t, v.upToDate)
	assert.True(t, v.seesConnected["sds-e"])
	assert.False(t, v.seesConnected["sds-d"])

	v = parseSurvivorView(strings.Replace(verbose, "quorum:yes", "quorum:no", 1))
	assert.False(t, v.quorum)
	v = parseSurvivorView(strings.Replace(verbose, "disk:UpToDate quorum", "disk:Outdated quorum", 1))
	assert.False(t, v.upToDate, "a peer's UpToDate disk is not the survivor's own")
}

func TestNodeIDOf(t *testing.T) {
	id, ok := nodeIDOf(threeReplicaConfig, "sds-d")
	assert.True(t, ok)
	assert.Equal(t, "3", id)
	_, ok = nodeIDOf(threeReplicaConfig, "sds-x")
	assert.False(t, ok)
}
