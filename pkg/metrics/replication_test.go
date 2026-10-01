package metrics

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// Steady state is the case that matters. drbdsetup omits the completion field
// entirely once a resync has finished, so a reader that treats the absence as
// zero paints a fully in-sync cluster as one whose resync never started — and
// that is the reading an operator acts on at three in the morning.
func TestSetReplicationExportsSteadyStateAsFullySynced(t *testing.T) {
	m := getTestMetrics(t)

	m.SetReplication(ReplicationSnapshot{
		Resources: []string{"data"},
		Replicas: []ReplicaState{{
			Resource: "data", Node: "node-a",
			Role: "Primary", DiskState: "UpToDate",
			SyncPercent: ptr(100.0), Quorum: ptr(true),
		}},
	})

	id := labels{"resource": "data", "node": "node-a"}
	assert.Equal(t, 1.0, requireGauge(t, m, "sds_drbd_resync_completed_ratio", id))
	assert.Equal(t, 1.0, requireGauge(t, m, "sds_drbd_quorum", id))
	assert.Equal(t, 1.0, requireGauge(t, m, "sds_drbd_role", labels{"resource": "data", "node": "node-a", "role": "Primary"}))
	assert.Equal(t, 1.0, requireGauge(t, m, "sds_drbd_disk_state", labels{"resource": "data", "node": "node-a", "state": "UpToDate"}))
	assert.Equal(t, 1.0, requireGauge(t, m, "sds_drbd_resource_up", labels{"resource": "data"}))
}

// A replica that did not report is absent, never zero. Zero is a real reading —
// "0% synced", "quorum lost" — and inventing it for an unreachable node turns a
// network problem into a data-loss alarm.
func TestSetReplicationOmitsAReplicaThatStoppedReporting(t *testing.T) {
	m := getTestMetrics(t)

	both := []ReplicaState{
		{Resource: "data", Node: "node-a", DiskState: "UpToDate", SyncPercent: ptr(100.0)},
		{Resource: "data", Node: "node-b", DiskState: "UpToDate", SyncPercent: ptr(100.0)},
	}
	m.SetReplication(ReplicationSnapshot{Resources: []string{"data"}, Replicas: both})
	m.SetReplication(ReplicationSnapshot{Resources: []string{"data"}, Replicas: both[:1]})

	_, ok := gaugeValue(t, m, "sds_drbd_resync_completed_ratio", labels{"resource": "data", "node": "node-b"})
	assert.False(t, ok, "an unreachable replica must vanish from /metrics, not read as 0%% synced")
	assert.Equal(t, 1.0, requireGauge(t, m, "sds_drbd_resync_completed_ratio", labels{"resource": "data", "node": "node-a"}))
	// The resource itself still answered through node-a.
	assert.Equal(t, 1.0, requireGauge(t, m, "sds_drbd_resource_up", labels{"resource": "data"}))
}

// resource_up is how "no node answered for this resource" is said out loud.
// Without it a resource whose every replica is unreachable is indistinguishable
// from one that was deleted.
func TestSetReplicationReportsResourceDownWhenNoReplicaAnswered(t *testing.T) {
	m := getTestMetrics(t)

	m.SetReplication(ReplicationSnapshot{Resources: []string{"data", "quiet"}, Replicas: []ReplicaState{
		{Resource: "data", Node: "node-a", DiskState: "UpToDate"},
	}})

	assert.Equal(t, 1.0, requireGauge(t, m, "sds_drbd_resource_up", labels{"resource": "data"}))
	assert.Equal(t, 0.0, requireGauge(t, m, "sds_drbd_resource_up", labels{"resource": "quiet"}))
	_, ok := gaugeValue(t, m, "sds_drbd_disk_state", labels{"resource": "quiet", "node": "node-a", "state": "UpToDate"})
	assert.False(t, ok, "a resource nobody answered for must export no replica series at all")
}

// Quorum has three states, not two: held, lost, and never reported. Only the
// node that was actually queried can speak for its own quorum, so a nil must
// stay off /metrics rather than become a confident zero.
func TestSetReplicationDistinguishesLostQuorumFromUnreportedQuorum(t *testing.T) {
	m := getTestMetrics(t)

	m.SetReplication(ReplicationSnapshot{Resources: []string{"data"}, Replicas: []ReplicaState{
		{Resource: "data", Node: "node-a", Quorum: ptr(false)},
		{Resource: "data", Node: "node-b"},
	}})

	assert.Equal(t, 0.0, requireGauge(t, m, "sds_drbd_quorum", labels{"resource": "data", "node": "node-a"}))
	_, ok := gaugeValue(t, m, "sds_drbd_quorum", labels{"resource": "data", "node": "node-b"})
	assert.False(t, ok, "an unreported quorum must not be exported as quorum lost")
}

// An empty state string is a source that did not say, not a state named "".
func TestSetReplicationSkipsEmptyStateLabels(t *testing.T) {
	m := getTestMetrics(t)

	m.SetReplication(ReplicationSnapshot{Resources: []string{"data"}, Replicas: []ReplicaState{
		{Resource: "data", Node: "node-a", Role: "", DiskState: "UpToDate", ReplicationState: ""},
	}})

	_, roleExported := gaugeValue(t, m, "sds_drbd_role", labels{"resource": "data", "node": "node-a", "role": ""})
	assert.False(t, roleExported, "an unknown role must not be exported as an empty label value")
	_, replExported := gaugeValue(t, m, "sds_drbd_replication_state", labels{"resource": "data", "node": "node-a", "state": ""})
	assert.False(t, replExported)
}

// A replica can be present and still not report its resync figure — the field
// is simply absent outside a resync. Exporting the nil as 0 would put a fully
// healthy replica on a dashboard at "0% synced", which is the same lie as
// zeroing an unreachable node, told about a node that answered.
func TestSetReplicationOmitsResyncWhenTheReplicaDidNotReportIt(t *testing.T) {
	m := getTestMetrics(t)

	m.SetReplication(ReplicationSnapshot{Resources: []string{"data"}, Replicas: []ReplicaState{
		{Resource: "data", Node: "node-a", DiskState: "UpToDate", SyncPercent: nil},
	}})

	_, ok := gaugeValue(t, m, "sds_drbd_resync_completed_ratio", labels{"resource": "data", "node": "node-a"})
	assert.False(t, ok, "an unreported resync figure must be absent, not exported as 0%%")
	// The replica itself is still very much there.
	assert.Equal(t, 1.0, requireGauge(t, m, "sds_drbd_disk_state", labels{"resource": "data", "node": "node-a", "state": "UpToDate"}))
}

// DRBD has been seen to report slightly over 100 on a finishing resync. A ratio
// above 1 on a panel labelled "percent complete" reads as a bug in the cluster
// rather than in the reporting.
func TestSetReplicationClampsResyncRatio(t *testing.T) {
	m := getTestMetrics(t)

	m.SetReplication(ReplicationSnapshot{Resources: []string{"data"}, Replicas: []ReplicaState{
		{Resource: "data", Node: "node-a", SyncPercent: ptr(105.0)},
		{Resource: "data", Node: "node-b", SyncPercent: ptr(-3.0)},
	}})

	assert.Equal(t, 1.0, requireGauge(t, m, "sds_drbd_resync_completed_ratio", labels{"resource": "data", "node": "node-a"}))
	assert.Equal(t, 0.0, requireGauge(t, m, "sds_drbd_resync_completed_ratio", labels{"resource": "data", "node": "node-b"}))
}
