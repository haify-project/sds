package controller

import (
	"context"
	"encoding/base64"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/haify-project/haify/pkg/database"
	"github.com/haify-project/haify/pkg/deployment"
)

func TestProjectIDIsStable(t *testing.T) {
	a := projectID("/srv/haify/data/team-a")
	assert.Equal(t, a, projectID("/srv/haify/data/team-a"))
	assert.NotEqual(t, a, projectID("/srv/haify/data/team-b"))
	assert.GreaterOrEqual(t, a, uint32(100000))
}

// The quota is set where the export is mounted; the other nodes skip, and an
// older filesystem without project quotas is explained, not half-done.
func TestSetNFSExportQuota(t *testing.T) {
	for _, tc := range []struct {
		out  string
		want string
	}{
		{"DONE", ""},
		{"NOPRJQUOTA", "tune2fs -O quota,project"},
		{"NOSETQUOTA", "quota package"},
		{"SKIP", "not mounted on any"},
	} {
		var script string
		dep := &fakeDeploymentClient{}
		dep.execFunc = func(_ context.Context, hosts []string, cmd string, _ ...deployment.ExecOption) (*deployment.ExecResult, error) {
			enc := strings.Fields(cmd)[1]
			b, _ := base64.StdEncoding.DecodeString(enc)
			script = string(b)
			res := successExecResult(hosts, "SKIP")
			res.Hosts[hosts[0]].Output = tc.out
			return res, nil
		}
		ctrl := newBasicTestController(dep)
		ctrl.db = newTestDB(t)
		require.NoError(t, ctrl.db.SaveResource(context.Background(), &database.Resource{Name: "data", Port: 7000, Nodes: "10.0.0.1,10.0.0.2"}))
		err := ctrl.resources.SetNFSExportQuota(context.Background(), "data", "team-a", 10<<30)
		if tc.want == "" {
			require.NoError(t, err)
			assert.Contains(t, script, "setquota -P ")
			assert.Contains(t, script, " 0 10485760 0 0 ")
		} else {
			assert.ErrorContains(t, err, tc.want)
		}
	}
}
