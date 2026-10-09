package controller

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/haify-project/haify/pkg/database"
	"github.com/haify-project/haify/pkg/deployment"
)

const grownRes = `resource db {
    volume 0 {
        device    minor 0;
        disk      /dev/vg0/db_data;
        meta-disk internal;
        disk {
            rs-discard-granularity 65536;
            size 4194304s;
        }
    }
}
`

// A volume grown after the snapshot comes up at the snapshot's size, without
// the grown size in its config, and is then grown back to the size it had.
func TestRollbackGrownVolume(t *testing.T) {
	var cmds, configs []string
	dep := &fakeDeploymentClient{}
	dep.execFunc = func(_ context.Context, hosts []string, cmd string, _ ...deployment.ExecOption) (*deployment.ExecResult, error) {
		cmds = append(cmds, cmd)
		switch {
		case strings.Contains(cmd, "-o lv_size vg0/db_data_snap_"):
			return successExecResult(hosts, "1077936128\n"), nil
		case strings.Contains(cmd, "-o lv_size vg0/db_data"):
			return successExecResult(hosts, "2155872256\n"), nil
		case strings.HasPrefix(cmd, "cat /etc/drbd.d/db.res"):
			return successExecResult(hosts, grownRes), nil
		}
		return successExecResult(hosts, ""), nil
	}
	dep.distributeConfigFunc = func(_ context.Context, hosts []string, content, _ string, _ ...deployment.ConfigOption) (*deployment.ConfigResult, error) {
		configs = append(configs, content)
		cmds = append(cmds, "distribute")
		return &deployment.ConfigResult{}, nil
	}
	dep.lvIsThinFunc = func(context.Context, string, string, string) (bool, error) { return true, nil }
	ctrl := newBasicTestController(dep)
	ctrl.db = newTestDB(t)
	ctx := context.Background()
	require.NoError(t, ctrl.db.SaveResource(ctx, &database.Resource{Name: "db", Port: 7000, Nodes: "10.0.0.1,10.0.0.2"}))
	require.NoError(t, ctrl.db.SaveVolume(ctx, &database.Volume{ResourceName: "db", VolumeName: "db_data", Pool: "vg0",
		SizeGB: 2, SizeBytes: 2 << 30, Device: "/dev/vg0/db_data"}))

	require.NoError(t, ctrl.resources.RollbackResourceSnapshot(ctx, "db", "before"))
	joined := strings.Join(cmds, "\n")
	merge := strings.Index(joined, "lvconvert --merge")
	up := strings.Index(joined, "drbdadm up db")
	wait := strings.Index(joined, "wait-connect-resource --wfc-timeout=60 db")
	grow := strings.Index(joined, "lvresize -L")
	resize := strings.Index(joined, "drbdadm resize --size=4194304s db/0")
	require.True(t, merge >= 0 && up > merge && wait > up && grow > wait && resize > grow, joined)
	require.GreaterOrEqual(t, len(configs), 2)
	assert.NotContains(t, configs[0], "size 4194304s", "it comes up without the grown size")
	assert.Contains(t, configs[0], "rs-discard-granularity 65536;", "and keeps the rest")
	assert.Contains(t, configs[len(configs)-1], "size 4194304s;", "which the grow writes back")
	assert.Less(t, strings.Index(joined, "distribute"), up, "the config is changed before the resource comes up")
}

func TestClearVolumeSizeInConfig(t *testing.T) {
	out, err := clearVolumeSizeInConfig(grownRes, 0)
	require.NoError(t, err)
	assert.Equal(t, strings.Replace(grownRes, "            size 4194304s;\n", "", 1), out)
}
