package controller

import (
	"context"
	"encoding/base64"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/haify-project/sds/pkg/database"
	"github.com/haify-project/sds/pkg/deployment"
)

// decodeWrapped returns the script inside an "echo <b64> | base64 -d | ..."
// command, or the command itself.
func decodeWrapped(cmd string) string {
	f := strings.Fields(cmd)
	if len(f) > 2 && f[0] == "echo" && strings.Contains(cmd, "base64 -d") {
		if b, err := base64.StdEncoding.DecodeString(f[1]); err == nil {
			return string(b)
		}
	}
	return cmd
}

const r3Config = `resource r3 {
    volume 0 {
        device    minor 2;
        disk      /dev/sds_tp/r3_data;
        meta-disk internal;
    }
    on sdt1 { address 10.0.0.1:7102; node-id 0; }
    on sdt3 { address 10.0.0.3:7102; node-id 1; }
}
`

func restoreHarness(t *testing.T, status map[string]string) (*Controller, *[]string) {
	var calls []string
	dep := &fakeDeploymentClient{}
	dep.execFunc = func(_ context.Context, hosts []string, cmd string, _ ...deployment.ExecOption) (*deployment.ExecResult, error) {
		script := decodeWrapped(cmd)
		calls = append(calls, strings.Join(hosts, ",")+" "+script)
		switch {
		case strings.HasPrefix(cmd, "grep -lE"):
			return successExecResult(hosts, "/etc/drbd.d/r3.res"), nil
		case strings.HasPrefix(cmd, "cat /etc/drbd.d/r3.res"):
			return successExecResult(hosts, r3Config), nil
		case strings.Contains(cmd, "drbdsetup status r3"):
			r := successExecResult(hosts, "")
			for h, hr := range r.Hosts {
				hr.Output = status[h]
			}
			return r, nil
		}
		return successExecResult(hosts, ""), nil
	}
	ctrl := newBasicTestController(dep)
	ctrl.db = newTestDB(t)
	require.NoError(t, ctrl.db.SaveResource(context.Background(), &database.Resource{
		Name: "r3", Port: 7102, Nodes: "sdt1,sdt3", Protocol: "C", Replicas: 2,
	}))
	for n, a := range map[string]string{"sdt1": "10.0.0.1", "sdt3": "10.0.0.3"} {
		ctrl.nodes.nodes[a] = &NodeInfo{Name: n, Address: a}
		ctrl.hostsMap[n] = a
	}
	return ctrl, &calls
}

// A restore under a Primary is a filesystem changing beneath its own cache,
// and on one replica only. It is refused before anything is touched.
func TestRestoreRefusesAResourceInUse(t *testing.T) {
	ctrl, calls := restoreHarness(t, map[string]string{"10.0.0.3": "r3 role:Primary\n  disk:UpToDate"})
	err := ctrl.snapshots.RestoreSnapshot(context.Background(), "sds_tp/r3_data", "s1", "sdt3")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "Primary on sdt3")
	for _, c := range *calls {
		assert.NotContains(t, c, "lvconvert", "nothing may be merged while the resource is in use")
		assert.NotContains(t, c, "drbdadm down")
	}
}

// The merge happens with the resource down, and the other replica is reset so
// it syncs from the restored one instead of overwriting it.
func TestRestoreKeepsTheReplicasInStep(t *testing.T) {
	ctrl, calls := restoreHarness(t, map[string]string{
		"10.0.0.1": "r3 role:Secondary", "10.0.0.3": "r3 role:Secondary",
	})
	require.NoError(t, ctrl.snapshots.RestoreSnapshot(context.Background(), "sds_tp/r3_data", "s1", "sdt3"))

	at := func(sub string) int {
		for i, c := range *calls {
			if strings.Contains(c, sub) {
				return i
			}
		}
		t.Fatalf("never ran %q: %v", sub, *calls)
		return -1
	}
	down, merge := at("drbdadm down r3"), at("lvconvert --merge /dev/sds_tp/s1")
	up, reset := at("10.0.0.3 sudo drbdadm up r3"), at("create-md --force r3/0")
	assert.True(t, down < merge && merge < up && up < reset, "order: down, merge, up, reset peers")
	assert.True(t, strings.HasPrefix((*calls)[reset], "10.0.0.1 "), "only the other replica is reset: %s", (*calls)[reset])
}

// An encrypted resource names its LUKS container in the DRBD config, not the
// LV, so the restore used to miss that it was replicated at all — and with the
// container holding the LV open, LVM only scheduled the merge for the next
// activation. The container is closed around the merge and reopened after.
func TestRestoreOfAnEncryptedVolume(t *testing.T) {
	var calls []string
	cfg := strings.ReplaceAll(r3Config, "/dev/sds_tp/r3_data", "/dev/mapper/sds_sds_tp_r3_data")
	dep := &fakeDeploymentClient{}
	dep.execFunc = func(_ context.Context, hosts []string, cmd string, _ ...deployment.ExecOption) (*deployment.ExecResult, error) {
		calls = append(calls, strings.Join(hosts, ",")+" "+decodeWrapped(cmd))
		switch {
		case strings.HasPrefix(cmd, "grep -lE") && strings.Contains(cmd, "mapper"):
			return successExecResult(hosts, "/etc/drbd.d/r3.res"), nil
		case strings.HasPrefix(cmd, "grep -lE"):
			return successExecResult(hosts, ""), nil
		case strings.HasPrefix(cmd, "cat /etc/drbd.d/r3.res"):
			return successExecResult(hosts, cfg), nil
		case strings.Contains(cmd, "drbdsetup status r3"):
			return successExecResult(hosts, "r3 role:Secondary"), nil
		}
		return successExecResult(hosts, ""), nil
	}
	ctrl := newBasicTestController(dep)
	ctrl.db = newTestDB(t)
	require.NoError(t, ctrl.db.SaveResource(context.Background(), &database.Resource{
		Name: "r3", Port: 7102, Nodes: "sdt1,sdt3", Protocol: "C", Replicas: 2, Encrypted: true,
	}))
	for n, a := range map[string]string{"sdt1": "10.0.0.1", "sdt3": "10.0.0.3"} {
		ctrl.nodes.nodes[a] = &NodeInfo{Name: n, Address: a}
		ctrl.hostsMap[n] = a
	}

	require.NoError(t, ctrl.snapshots.RestoreSnapshot(context.Background(), "sds_tp/r3_data", "s1", "sdt3"))
	at := func(sub string) int {
		for i, c := range calls {
			if strings.Contains(c, sub) {
				return i
			}
		}
		t.Fatalf("never ran %q: %v", sub, calls)
		return -1
	}
	down, closeC, merge := at("drbdadm down r3"), at("cryptsetup close sds_sds_tp_r3_data"), at("lvconvert --merge")
	open, up := at("cryptsetup open"), at("10.0.0.3 sudo drbdadm up r3")
	assert.True(t, down < closeC && closeC < merge && merge < open && open < up,
		"order: down, close container, merge, reopen, up: %v", calls)
}

// Thin snapshots are created with activation skipped; anything that reads one
// has to activate it first, and nothing else may be touched.
func TestSnapshotsAreActivatedBeforeTheyAreRead(t *testing.T) {
	assert.Equal(t, "[ -e /dev/sds_tp/r3_data_bk_1 ] || sudo lvchange -ay -K sds_tp/r3_data_bk_1;",
		activateSnapshotCmd("/dev/sds_tp/r3_data_bk_1"))
	for _, d := range []string{"/dev/zvol/tank/v@s", "/dev/mapper/sds_x", "/dev/drbd3", "/dev/vdb", "relative"} {
		assert.Empty(t, activateSnapshotCmd(d), d)
	}
}

func TestRestoreDecompressesOnlyCompressedImages(t *testing.T) {
	assert.Equal(t, " | gzip -dc", decompressFor("data/b1/volume-0.img.gz"))
	assert.Equal(t, "", decompressFor("data/b1/volume-0.img"), "a backup from before compression is raw")
}
