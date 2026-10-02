package metrics

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestHealthSeries(t *testing.T) {
	m := getTestMetrics(t)

	m.SetReplication(ReplicationSnapshot{
		Resources: []string{"data"},
		Replicas: []ReplicaState{
			{Resource: "data", Node: "a", DiskState: "UpToDate"},
			{Resource: "data", Node: "b", DiskState: "UpToDate", OutOfSyncBytes: 4096, TLS: ptr(true)},
		},
	})
	assert.Equal(t, 4096.0, requireGauge(t, m, "sds_drbd_out_of_sync_bytes", labels{"resource": "data", "node": "b"}))
	assert.Equal(t, 1.0, requireGauge(t, m, "sds_drbd_connection_tls", labels{"resource": "data", "node": "b"}))
	_, ok := gaugeValue(t, m, "sds_drbd_connection_tls", labels{"resource": "data", "node": "a"})
	assert.False(t, ok, "the node that answered has no connection to itself")

	m.SetFiringAlerts([]string{"resource.degraded/warning", "resource.degraded/warning", "pool.data_full/critical"})
	assert.Equal(t, 2.0, requireGauge(t, m, "sds_controller_alerts_firing", labels{"type": "resource.degraded", "severity": "warning"}))
	m.SetFiringAlerts(nil)
	_, ok = gaugeValue(t, m, "sds_controller_alerts_firing", labels{"type": "resource.degraded", "severity": "warning"})
	assert.False(t, ok, "a cleared alert's series goes away")

	m.SetBackups([]BackupState{{Resource: "db", Target: "s3", Kind: "incremental", StartedUnix: 1000, ShippedBytes: 26 << 20}})
	assert.Equal(t, 1000.0, requireGauge(t, m, "sds_controller_backup_last_success_timestamp_seconds", labels{"resource": "db", "target": "s3"}))
	assert.Equal(t, float64(26<<20), requireGauge(t, m, "sds_controller_backup_last_shipped_bytes", labels{"resource": "db", "target": "s3", "kind": "incremental"}))

	m.SetNodeReachability([]NodeReach{{Node: "a", Reachable: true}, {Node: "b"}})
	assert.Equal(t, 0.0, requireGauge(t, m, "sds_controller_node_reachable", labels{"node": "b"}))

	m.SetThinPoolUsage([]ThinUsage{{Pool: "p", Node: "a", DataPercent: 91.5, MetaPercent: 12}})
	assert.Equal(t, 91.5, requireGauge(t, m, "sds_controller_pool_thin_used_percent", labels{"pool": "p", "node": "a", "kind": "data"}))

	m.SetFaultDomainRisks(map[string]string{"db": "host=dell"})
	assert.Equal(t, 1.0, requireGauge(t, m, "sds_controller_resource_fault_domain_risk", labels{"resource": "db", "domain": "host=dell"}))
}
