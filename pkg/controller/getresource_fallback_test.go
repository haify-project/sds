package controller

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/liliang-cn/sds/pkg/database"
	"github.com/liliang-cn/sds/pkg/deployment"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

// GetResource asked hosts[0] and nothing else, so a resource became entirely
// unobservable the moment its *first* node went down — role Unknown, no node
// states, every peer greyed out in the UI — while three healthy peers sat there
// able to answer. Whether the cluster is legible then depends on which node
// happens to be listed first, which is not a property anyone chose.

const fallbackStatus = `openclaw role:Secondary
  disk:UpToDate open:no
  sds-b role:Primary
    peer-disk:UpToDate
  sds-e role:Secondary
    peer-disk:UpToDate
`

func fallbackTestController(t *testing.T, dep deploymentClient) *Controller {
	t.Helper()
	ctrl := newBasicTestController(dep)

	db, err := database.Open(&database.Config{Path: filepath.Join(t.TempDir(), "sds.db")}, zap.NewNop())
	require.NoError(t, err)
	t.Cleanup(func() { db.Close() })
	ctrl.db = db

	require.NoError(t, db.SaveResource(context.Background(), &database.Resource{
		Name: "openclaw", Port: 7000, Nodes: "node-down,node-b,node-e", Protocol: "C", Replicas: 3,
	}))
	registerNodes(ctrl, map[string]string{
		"node-down": "10.0.0.9", "node-b": "10.0.0.2", "node-e": "10.0.0.5",
	})
	return ctrl
}

func TestResourceStaysReadableWhenTheFirstNodeIsDown(t *testing.T) {
	var asked []string
	dep := &fakeDeploymentClient{
		drbdStatusFunc: func(_ context.Context, hosts []string, _ string) (*deployment.ExecResult, error) {
			asked = append(asked, hosts...)
			if hosts[0] == "10.0.0.9" {
				// What an unreachable node looks like: the SSH call itself fails.
				return nil, assert.AnError
			}
			return successExecResult(hosts, fallbackStatus), nil
		},
	}
	ctrl := fallbackTestController(t, dep)

	res, err := ctrl.resources.GetResource(context.Background(), "openclaw")
	require.NoError(t, err)
	require.NotNil(t, res)

	assert.Equal(t, []string{"10.0.0.9", "10.0.0.2"}, asked,
		"the dead node is tried first, then the next one — and no further once answered")
	assert.NotEqual(t, "Unknown", res.Role, "a healthy peer answered, so the role is known")
	assert.NotEmpty(t, res.NodeStates, "peer states came back and must not be dropped")
}

// A node that answers with a failed command is no more useful than one that
// cannot be reached; both must fall through to the next.
func TestAFailedStatusCommandAlsoFallsThrough(t *testing.T) {
	dep := &fakeDeploymentClient{
		drbdStatusFunc: func(_ context.Context, hosts []string, _ string) (*deployment.ExecResult, error) {
			if hosts[0] == "10.0.0.9" {
				return failedResult(hosts, "drbdadm: no resources defined"), nil
			}
			return successExecResult(hosts, fallbackStatus), nil
		},
	}
	ctrl := fallbackTestController(t, dep)

	res, err := ctrl.resources.GetResource(context.Background(), "openclaw")
	require.NoError(t, err)
	assert.NotEqual(t, "Unknown", res.Role)
	assert.NotEmpty(t, res.NodeStates)
}

// With every node down there is nothing to report, and that has to stay a
// legible answer rather than an error: the UI still needs to draw the resource.
func TestEveryNodeDownStillReturnsTheResource(t *testing.T) {
	dep := &fakeDeploymentClient{
		drbdStatusFunc: func(_ context.Context, hosts []string, _ string) (*deployment.ExecResult, error) {
			return nil, assert.AnError
		},
	}
	ctrl := fallbackTestController(t, dep)

	res, err := ctrl.resources.GetResource(context.Background(), "openclaw")
	require.NoError(t, err)
	require.NotNil(t, res)
	assert.Equal(t, "Unknown", res.Role)
}

// Falling through to another host moved which node the answer describes, and
// both parsers were still told it was the first configured node. The result is
// worse than the outage it was meant to survive: a node that is down gets
// rendered UpToDate, because the reachable node's own state was filed under the
// unreachable node's name.
func TestTheAnswerIsAttributedToTheNodeThatAnswered(t *testing.T) {
	dep := &fakeDeploymentClient{
		drbdStatusFunc: func(_ context.Context, hosts []string, _ string) (*deployment.ExecResult, error) {
			if hosts[0] == "10.0.0.9" {
				return nil, assert.AnError
			}
			return successExecResult(hosts, fallbackStatus), nil
		},
		drbdStatusJSONFunc: func(_ context.Context, hosts []string, _ string) (*deployment.ExecResult, error) {
			if hosts[0] == "10.0.0.9" {
				t.Error("the JSON pass must follow the host that answered, not hosts[0]")
			}
			return successExecResult(hosts, ""), nil
		},
	}
	ctrl := fallbackTestController(t, dep)

	res, err := ctrl.resources.GetResource(context.Background(), "openclaw")
	require.NoError(t, err)

	if st, ok := res.NodeStates["node-down"]; ok && st.DiskState == "UpToDate" {
		t.Errorf("the unreachable node is reported healthy: %+v", st)
	}
	assert.Contains(t, res.NodeStates, "node-b",
		"node-b answered, so its own state belongs to node-b")
}
