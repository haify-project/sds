package controller

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/liliang-cn/sds/pkg/database"
	"github.com/liliang-cn/sds/pkg/deployment"
	"go.uber.org/zap"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// SDS could grow a resource but never shrink it: add-replica existed, its
// inverse did not. Every removal was therefore hand-editing DRBD config on
// every node — on, in this cluster's case, the volume holding the controller's
// own database while the controller ran on it.
//
// The config rewrite is the part that has to be exact. A leftover `on` block
// leaves a member that never connects and still counts toward quorum; a
// leftover mesh entry names a host that no longer exists and `drbdadm adjust`
// rejects the whole file.

const threeReplicaConfig = `resource sds-meta {

    options {
        quorum majority;
    }

    volume 0 {
        device    minor 3;
        disk      /dev/sds_sdspool/sds-meta_data;
        meta-disk internal;
    }

    on sds-b {
        address   192.168.123.227:7999;
        node-id   0;
    }

    on sds-e {
        address   192.168.123.212:7999;
        node-id   1;
    }

    on sds-d {
        address   192.168.123.228:7999;
        node-id   3;
    }

    connection-mesh {
        hosts sds-b sds-e sds-d;
    }
}
`

func TestRemoveReplicaDropsTheNodeAndItsMeshEntry(t *testing.T) {
	ctrl := newBasicTestController(&fakeDeploymentClient{})
	registerNodes(ctrl, map[string]string{
		"node-b": "192.168.123.227", "node-e": "192.168.123.212", "node-d": "192.168.123.228",
	})

	out, err := ctrl.resources.removeReplicaFromConfig(threeReplicaConfig, "sds-meta", "node-d")
	require.NoError(t, err)

	assert.NotContains(t, out, "sds-d",
		"a leftover on-block is a member that never connects and still counts for quorum")
	assert.Contains(t, out, "on sds-b", "the surviving replicas must stay")
	assert.Contains(t, out, "on sds-e")

	mesh := meshLine(t, out)
	assert.NotContains(t, mesh, "sds-d", "adjust rejects a mesh naming a host with no on-block")
	assert.Contains(t, mesh, "sds-b")
	assert.Contains(t, mesh, "sds-e")
}

// The node-ids of the survivors must not move. DRBD stores the peer's node-id
// in metadata; renumbering a surviving replica invalidates its bitmap slot and
// forces a full resync of a resource that never changed.
func TestRemoveReplicaLeavesSurvivingNodeIDsAlone(t *testing.T) {
	ctrl := newBasicTestController(&fakeDeploymentClient{})
	registerNodes(ctrl, map[string]string{
		"node-b": "192.168.123.227", "node-e": "192.168.123.212", "node-d": "192.168.123.228",
	})

	out, err := ctrl.resources.removeReplicaFromConfig(threeReplicaConfig, "sds-meta", "node-d")
	require.NoError(t, err)

	for _, want := range []string{"node-id   0", "node-id   1"} {
		assert.Contains(t, out, want, "surviving node-ids must be untouched")
	}
}

func TestRemoveReplicaRefusesANodeThatIsNotInTheConfig(t *testing.T) {
	ctrl := newBasicTestController(&fakeDeploymentClient{})
	registerNodes(ctrl, map[string]string{"node-x": "10.0.0.9"})

	_, err := ctrl.resources.removeReplicaFromConfig(threeReplicaConfig, "sds-meta", "node-x")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not in the config")
}

// meshLine returns the hosts line of the connection-mesh, or fails.
func meshLine(t *testing.T, config string) string {
	t.Helper()
	for _, line := range strings.Split(config, "\n") {
		if strings.Contains(line, "hosts ") {
			return line
		}
	}
	t.Fatalf("no connection-mesh in:\n%s", config)
	return ""
}

// The guards matter more than the operation. Removing a replica is permanent —
// unlike a conversion, where the single-copy window closes when the resync
// finishes — so the checks are stricter, not looser.

func removeTestController(t *testing.T, dep deploymentClient, nodes, diskless string) *Controller {
	t.Helper()
	ctrl := newBasicTestController(dep)
	db, err := database.Open(&database.Config{Path: filepath.Join(t.TempDir(), "sds.db")}, zap.NewNop())
	require.NoError(t, err)
	t.Cleanup(func() { db.Close() })
	ctrl.db = db

	require.NoError(t, db.SaveResource(context.Background(), &database.Resource{
		Name: "sds-meta", Port: 7999, Nodes: nodes, DisklessNodes: diskless,
		Protocol: "C", Replicas: 3,
	}))
	require.NoError(t, db.SaveVolume(context.Background(), &database.Volume{
		ResourceName: "sds-meta", VolumeName: "sds-meta_data", VolumeID: 0,
		Pool: "sds_sdspool", SizeGB: 1, Device: "/dev/sds_sdspool/sds-meta_data",
	}))
	registerNodes(ctrl, map[string]string{
		"node-b": "192.168.123.227", "node-e": "192.168.123.212", "node-d": "192.168.123.228",
	})
	return ctrl
}

func statusFake(status string) *fakeDeploymentClient {
	return &fakeDeploymentClient{
		execFunc: statusExec(status),
		drbdStatusFunc: func(_ context.Context, hosts []string, _ string) (*deployment.ExecResult, error) {
			return successExecResult(hosts, status), nil
		},
	}
}

func statusExec(status string) func(context.Context, []string, string, ...deployment.ExecOption) (*deployment.ExecResult, error) {
	return func(_ context.Context, hosts []string, cmd string, _ ...deployment.ExecOption) (*deployment.ExecResult, error) {
		if strings.HasPrefix(cmd, "cat /etc/drbd.d/") {
			return successExecResult(hosts, threeReplicaConfig), nil
		}
		if strings.Contains(cmd, "drbdadm status") {
			return successExecResult(hosts, status), nil
		}
		return successExecResult(hosts, ""), nil
	}
}

const healthyStatus = `sds-meta role:Secondary
  disk:UpToDate open:no
  sds-b role:Primary
    peer-disk:UpToDate
  sds-e role:Secondary
    peer-disk:UpToDate
`

// Taking the copy a resource is currently served from is not a removal, it is
// an outage.
func TestRemoveReplicaRefusesThePrimary(t *testing.T) {
	primaryHere := `sds-meta role:Primary
  disk:UpToDate open:yes
  sds-b role:Secondary
    peer-disk:UpToDate
  sds-e role:Secondary
    peer-disk:UpToDate
`
	ctrl := removeTestController(t,
		statusFake(primaryHere),
		"node-b,node-e,node-d", "")

	err := ctrl.resources.RemoveReplica(context.Background(), "sds-meta", "node-d")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "Primary")
}

// Two diskful copies is the floor: going to one leaves a resource with no
// redundancy at all, permanently, which is not something to do by accident.
func TestRemoveReplicaRefusesWhenOnlyOneDiskfulWouldRemain(t *testing.T) {
	ctrl := removeTestController(t,
		statusFake(healthyStatus),
		"node-b,node-d", "")

	err := ctrl.resources.RemoveReplica(context.Background(), "sds-meta", "node-d")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "one diskful")
}

func TestRemoveReplicaRefusesANodeThatHoldsNoReplica(t *testing.T) {
	ctrl := removeTestController(t,
		statusFake(healthyStatus),
		"node-b,node-e", "")

	err := ctrl.resources.RemoveReplica(context.Background(), "sds-meta", "node-d")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no replica")
}

// The tiebreaker is diskless: it has metadata and no data, and removing it is
// `ha set-tiebreaker`, not this.
func TestRemoveReplicaRefusesTheTiebreaker(t *testing.T) {
	ctrl := removeTestController(t,
		statusFake(healthyStatus),
		"node-b,node-e,node-d", "node-d")

	err := ctrl.resources.RemoveReplica(context.Background(), "sds-meta", "node-d")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "tiebreaker")
}

func TestRemoveReplicaTearsDownAndForgetsTheNode(t *testing.T) {
	var ran []string
	dep := &fakeDeploymentClient{
		execFunc: func(_ context.Context, hosts []string, cmd string, _ ...deployment.ExecOption) (*deployment.ExecResult, error) {
			if strings.HasPrefix(cmd, "cat /etc/drbd.d/") {
				return successExecResult(hosts, threeReplicaConfig), nil
			}
			ran = append(ran, hosts[0]+": "+cmd)
			return successExecResult(hosts, ""), nil
		},
		drbdStatusFunc: func(_ context.Context, hosts []string, _ string) (*deployment.ExecResult, error) {
			return successExecResult(hosts, healthyStatus), nil
		},
	}
	ctrl := removeTestController(t, dep, "node-b,node-e,node-d", "")

	require.NoError(t, ctrl.resources.RemoveReplica(context.Background(), "sds-meta", "node-d"))

	joined := strings.Join(ran, "\n")
	assert.Contains(t, joined, "192.168.123.228: sudo drbdadm down sds-meta",
		"the leaving node must stop serving the resource")
	assert.Contains(t, joined, "192.168.123.227: sudo drbdadm adjust sds-meta",
		"survivors must be told the peer is gone")

	// Recorded last, so a failure above leaves the row describing reality.
	res, err := ctrl.db.GetResource(context.Background(), "sds-meta")
	require.NoError(t, err)
	assert.Equal(t, "node-b,node-e", res.Nodes, "the leaving node must be forgotten")
}

// A status that says nothing is not a Secondary. Reading it as one would
// remove a replica whose role was never established.
func TestRemoveReplicaRefusesWhenTheRoleCannotBeRead(t *testing.T) {
	ctrl := removeTestController(t, statusFake(""), "node-b,node-e,node-d", "")

	err := ctrl.resources.RemoveReplica(context.Background(), "sds-meta", "node-d")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "blind")
}
