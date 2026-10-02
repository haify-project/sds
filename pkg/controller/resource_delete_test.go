package controller

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/haify-project/sds/pkg/database"
	"github.com/haify-project/sds/pkg/deployment"
)

// A backing volume that cannot be removed aborts a non-forced delete. The
// error must point at something the operator can actually do — there is no
// force option on `sds resource delete` — and the records must survive so the
// rerun it suggests still knows which volume to remove.
func TestDeleteResourceBackingVolumeFailureMessage(t *testing.T) {
	dep := &fakeDeploymentClient{
		execFunc: func(ctx context.Context, hosts []string, cmd string, opts ...deployment.ExecOption) (*deployment.ExecResult, error) {
			if strings.Contains(cmd, "lvremove") {
				return failedExecResult(hosts, "Logical volume vg0/data_vol0 in use."), nil
			}
			return successExecResult(hosts, ""), nil
		},
	}
	ctrl := newBasicTestController(dep)
	registerNodes(ctrl, map[string]string{"orange1": "10.0.0.1", "orange2": "10.0.0.2"})
	db := newTestDB(t)
	ctrl.db = db
	ctx := context.Background()
	require.NoError(t, db.SaveResource(ctx, &database.Resource{
		Name: "data", Port: 7100, Nodes: "orange1,orange2", Protocol: "C", Replicas: 2,
	}))
	require.NoError(t, db.SaveVolume(ctx, &database.Volume{
		ResourceName: "data", VolumeName: "data_vol0", Pool: "vg0", Device: "/dev/vg0/data_vol0",
	}))

	err := ctrl.resources.DeleteResource(ctx, "data", false)
	require.Error(t, err)
	msg := err.Error()
	require.Contains(t, msg, "vg0/data_vol0")
	require.Contains(t, msg, "sds resource delete data")
	require.NotContains(t, msg, "force")

	_, gerr := db.GetResource(ctx, "data")
	require.NoError(t, gerr, "the resource record must survive for the rerun")
}
