package controller

import (
	"context"
	"github.com/haify-project/sds/pkg/database"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"strings"
	"testing"
)

// The decision follows what was built on the nodes, not the flag: a thin LV
// made from --storage-type lvm on a thin pool still skips the sync, and an
// encrypted one never does.
func TestInitialSyncSkipFollowsTheBuiltVolumes(t *testing.T) {
	thinOn := map[string]bool{}
	dep := &fakeDeploymentClient{
		lvIsThinFunc: func(_ context.Context, host, _, _ string) (bool, error) { return thinOn[host], nil },
	}
	rm := newBasicTestController(dep).resources
	ctx := context.Background()
	nodes := []string{"10.0.0.1", "10.0.0.2"}
	plain := []resolvedVolume{{pool: "vg0", volumeName: "r_data"}}
	encrypted := []resolvedVolume{{pool: "vg0", volumeName: "r_data", encrypted: true}}

	if rm.backedByZeroReadingStorage(ctx, "lvm", nodes, plain) {
		t.Error("thick LVs were treated as reading zeros")
	}
	thinOn["10.0.0.1"], thinOn["10.0.0.2"] = true, true
	if !rm.backedByZeroReadingStorage(ctx, "lvm", nodes, plain) {
		t.Error("thin LVs created under --storage-type lvm still ran the full sync")
	}
	if rm.backedByZeroReadingStorage(ctx, "lvm-thin", nodes, encrypted) {
		t.Error("an encrypted volume skipped the sync; its replicas read different noise")
	}
	thinOn["10.0.0.2"] = false
	if rm.backedByZeroReadingStorage(ctx, "lvm-thin", nodes, plain) {
		t.Error("one thick replica must force the full sync")
	}
	if !rm.backedByZeroReadingStorage(ctx, "zfs", nodes, plain) {
		t.Error("zvols read unwritten blocks as zeros")
	}
}

// Online verify is how a skipped initial sync, or anything else, is proven
// safe: it has to be configured before anyone needs it.
func TestGeneratedConfigCanRunOnlineVerify(t *testing.T) {
	rm := newBasicTestController(&fakeDeploymentClient{}).resources
	vols := []resolvedVolume{{volumeName: "r_data", pool: "vg0", sizeGB: 1}}
	cfg := rm.generateDrbdConfig("r", 7000, vols, []string{"n1", "n2"}, nil, "C", "lvm-thin", nil, nil)
	if !strings.Contains(cfg, "verify-alg crc32c;") {
		t.Fatalf("no default verify-alg:\n%s", cfg)
	}
	cfg = rm.generateDrbdConfig("r", 7000, vols, []string{"n1", "n2"}, nil, "C", "lvm-thin",
		map[string]string{"net/verify-alg": "sha256"}, nil)
	if !strings.Contains(cfg, "verify-alg sha256;") || strings.Contains(cfg, "crc32c") {
		t.Fatalf("an explicit verify-alg must replace the default:\n%s", cfg)
	}
}

// A resource with a replica on an unreachable node is refused up front, not
// after volumes were built on the reachable ones.
func TestResourceCreationRefusesAnOfflineNode(t *testing.T) {
	ctrl := newBasicTestController(&fakeDeploymentClient{})
	ctrl.nodes.nodes["10.0.0.1"] = &NodeInfo{Name: "n1", Address: "10.0.0.1", State: NodeStateOnline}
	ctrl.nodes.nodes["10.0.0.3"] = &NodeInfo{Name: "n3", Address: "10.0.0.3", State: NodeStateOffline}
	ctrl.hostsMap["n1"], ctrl.hostsMap["n3"] = "10.0.0.1", "10.0.0.3"

	if err := ctrl.resources.assertNodesOnline([]string{"n1"}); err != nil {
		t.Fatalf("an online node was refused: %v", err)
	}
	err := ctrl.resources.assertNodesOnline([]string{"n1", "n3"})
	if err == nil || !strings.Contains(err.Error(), "n3") || strings.Contains(err.Error(), "n1,") {
		t.Fatalf("err = %v, want n3 named as offline", err)
	}
}

// Zero runs are skipped only when every replica reads unwritten blocks as
// zeros and the device was discarded first; otherwise the full write stays.
func TestSparseWritesOnlyOntoDiscardedThinReplicas(t *testing.T) {
	assert.Equal(t, "CONV=fsync; ", sparseWriteSetup(false, "/dev/drbd/by-res/r/0"))
	on := sparseWriteSetup(true, "/dev/drbd/by-res/r/0")
	assert.Contains(t, on, "blkdiscard -f /dev/drbd/by-res/r/0")
	assert.Contains(t, on, "then CONV=sparse,fsync; else CONV=fsync; fi")

	thin := map[string]bool{"10.0.0.1": true, "10.0.0.2": true}
	dep := &fakeDeploymentClient{lvIsThinFunc: func(_ context.Context, h, _, _ string) (bool, error) { return thin[h], nil }}
	ctrl := newBasicTestController(dep)
	ctrl.db = newTestDB(t)
	ctx := context.Background()
	require.NoError(t, ctrl.db.SaveResource(ctx, &database.Resource{Name: "r", Nodes: "n1,n2"}))
	require.NoError(t, ctrl.db.SaveVolume(ctx, &database.Volume{ResourceName: "r", VolumeName: "r_data", Pool: "sds_tp", VolumeID: 0}))
	ctrl.hostsMap["n1"], ctrl.hostsMap["n2"] = "10.0.0.1", "10.0.0.2"
	assert.True(t, ctrl.resources.zeroReadingReplicas(ctx, "r"))
	thin["10.0.0.2"] = false
	assert.False(t, ctrl.resources.zeroReadingReplicas(ctx, "r"), "one thick replica keeps the full write")
	thin["10.0.0.2"] = true
	require.NoError(t, ctrl.db.SaveResource(ctx, &database.Resource{Name: "r", Nodes: "n1,n2", Encrypted: true}))
	assert.False(t, ctrl.resources.zeroReadingReplicas(ctx, "r"), "an encrypted replica reads noise, not zeros")
}
