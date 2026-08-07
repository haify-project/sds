package controller

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/liliang-cn/sds/pkg/deployment"
)

// The 30-second default cost a whole afternoon on real hardware, so it is
// pinned here.
//
// deployment.Exec defaults to a 30-second timeout — right for the `lvs` and
// `drbdsetup` queries it was written for, catastrophic for streaming a volume.
// A 1 GiB image takes roughly 40 seconds on a gigabit LAN, so Exec returned
// while dd and rclone were still running: the upload read as finished, the
// verification that followed found no object yet and failed the backup, the
// cleanup could not delete an object that did not exist yet, and the pipeline
// carried on and eventually left a complete but orphaned image on the target.
func TestDataMoveOutlivesTheDefaultExecTimeout(t *testing.T) {
	var got time.Duration
	dep := &fakeDeploymentClient{
		execFunc: func(_ context.Context, hosts []string, _ string, opts ...deployment.ExecOption) (*deployment.ExecResult, error) {
			got = deployment.ExecTimeoutOf(opts...)
			return successExecResult(hosts, ""), nil
		},
	}
	bm := &BackupManager{controller: newBasicTestController(dep)}

	_, err := bm.execDataMove(context.Background(), "10.0.0.1", "true")
	require.NoError(t, err)
	assert.Equal(t, dataMoveTimeout, got,
		"a transfer with no caller deadline must not inherit the 30s query default")
	assert.Greater(t, got, 30*time.Second)
}

// When the caller does set a deadline — the CLI's --timeout becomes one — the
// transfer gets exactly that, not more and not the default.
func TestDataMoveHonoursTheCallerDeadline(t *testing.T) {
	var got time.Duration
	dep := &fakeDeploymentClient{
		execFunc: func(_ context.Context, hosts []string, _ string, opts ...deployment.ExecOption) (*deployment.ExecResult, error) {
			got = deployment.ExecTimeoutOf(opts...)
			return successExecResult(hosts, ""), nil
		},
	}
	bm := &BackupManager{controller: newBasicTestController(dep)}

	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Minute)
	defer cancel()
	_, err := bm.execDataMove(ctx, "10.0.0.1", "true")
	require.NoError(t, err)

	assert.Greater(t, got, 85*time.Minute, "the caller's remaining time must reach Exec")
	assert.LessOrEqual(t, got, 90*time.Minute)
}

// deployment.Exec reports AllSuccess() == true for an empty host set, so a
// command that ran nowhere is indistinguishable from one that succeeded. For a
// step whose entire purpose is moving bytes that is the wrong way to be wrong.
func TestDataMoveRejectsAResultThatNamesNoHost(t *testing.T) {
	dep := &fakeDeploymentClient{
		execFunc: func(context.Context, []string, string, ...deployment.ExecOption) (*deployment.ExecResult, error) {
			empty := &deployment.ExecResult{Hosts: map[string]*deployment.HostResult{}}
			require.True(t, empty.AllSuccess(), "precondition: an empty result still reads as success")
			return empty, nil
		},
	}
	bm := &BackupManager{controller: newBasicTestController(dep)}

	_, err := bm.execDataMove(context.Background(), "10.0.0.1", "true")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "may not have run at all")
}
