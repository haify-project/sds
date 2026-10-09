package controller

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/haify-project/haify/pkg/database"
	"github.com/haify-project/haify/pkg/deployment"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// newDualPrimaryController wires a controller with a test DB and the given
// resource record, plus nodes n1/n2/pve resolving to 10.0.0.{1,2,9}.
func newDualPrimaryController(t *testing.T, dep deploymentClient, res *database.Resource) *Controller {
	t.Helper()
	ctrl := newBasicTestController(dep)
	ctrl.db = newTestDB(t)
	registerNodes(ctrl, map[string]string{
		"n1":  "10.0.0.1",
		"n2":  "10.0.0.2",
		"pve": "10.0.0.9",
	})
	if res != nil {
		require.NoError(t, ctrl.db.SaveResource(context.Background(), res))
	}
	return ctrl
}

// execCmdsMatching returns every command issued that contains substr.
func execCmdsMatching(dep *fakeDeploymentClient, substr string) []execCall {
	var out []execCall
	for _, c := range dep.execCalls {
		if strings.Contains(c.cmd, substr) {
			out = append(out, c)
		}
	}
	return out
}

// TestSetDualPrimaryEnableCoversEveryParticipant is the core correctness claim:
// allow-two-primaries is a per-connection option, so it must reach replicas,
// quorum tiebreakers AND diskless clients. Missing the diskless client would be
// the silent failure that breaks Proxmox live migration onto a compute-only
// host, which is the whole point of the feature.
func TestSetDualPrimaryEnableCoversEveryParticipant(t *testing.T) {
	dep := &fakeDeploymentClient{}
	ctrl := newDualPrimaryController(t, dep, &database.Resource{
		Name:            "pve-100-0",
		Nodes:           "n1,n2",
		DisklessClients: "pve",
	})

	require.NoError(t, ctrl.resources.SetDualPrimary(context.Background(), "pve-100-0", true))

	calls := execCmdsMatching(dep, "--allow-two-primaries=yes")
	require.Len(t, calls, 1, "one batched exec expected")
	assert.ElementsMatch(t, []string{"10.0.0.1", "10.0.0.2", "10.0.0.9"}, calls[0].hosts,
		"replicas and the diskless client must all get the toggle")
	assert.Contains(t, calls[0].cmd, "pve-100-0")
}

// TestSetDualPrimaryRefusesWANResource: protocol A is asynchronous, so two
// Primaries corrupts data. The guard must fire BEFORE anything is executed.
func TestSetDualPrimaryRefusesWANResource(t *testing.T) {
	dep := &fakeDeploymentClient{}
	ctrl := newDualPrimaryController(t, dep, &database.Resource{
		Name:    "wanres",
		Nodes:   "n1,n2",
		WANMode: true,
	})

	err := ctrl.resources.SetDualPrimary(context.Background(), "wanres", true)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "asynchronous")
	assert.Empty(t, execCmdsMatching(dep, "allow-two-primaries"),
		"a refused WAN resource must not be touched at all")
}

// Disabling is the safety-restoring direction, so it stays available even on a
// WAN resource (where it is a no-op) rather than erroring out of the caller's
// cleanup path.
func TestSetDualPrimaryDisableAllowedOnWANResource(t *testing.T) {
	dep := &fakeDeploymentClient{}
	ctrl := newDualPrimaryController(t, dep, &database.Resource{
		Name:    "wanres",
		Nodes:   "n1,n2",
		WANMode: true,
	})

	require.NoError(t, ctrl.resources.SetDualPrimary(context.Background(), "wanres", false))
	assert.NotEmpty(t, execCmdsMatching(dep, "--allow-two-primaries=no"))
}

// TestSetDualPrimaryDisableIdempotentForUnknownResource: the plugin's cleanup
// path runs unconditionally, including after a failed create where the resource
// never made it into the DB. That must not turn into an error.
func TestSetDualPrimaryDisableIdempotentForUnknownResource(t *testing.T) {
	dep := &fakeDeploymentClient{}
	ctrl := newDualPrimaryController(t, dep, nil)

	assert.NoError(t, ctrl.resources.SetDualPrimary(context.Background(), "ghost", false))
}

func TestSetDualPrimaryEnableFailsForUnknownResource(t *testing.T) {
	dep := &fakeDeploymentClient{}
	ctrl := newDualPrimaryController(t, dep, nil)

	err := ctrl.resources.SetDualPrimary(context.Background(), "ghost", true)
	require.Error(t, err)
}

// A half-applied enable produces a migration that fails midway with a confusing
// DRBD error, so any node rejecting the command fails the whole call.
func TestSetDualPrimaryEnableFailsWhenAnyNodeRejects(t *testing.T) {
	dep := &fakeDeploymentClient{
		execFunc: func(_ context.Context, hosts []string, cmd string, _ ...deployment.ExecOption) (*deployment.ExecResult, error) {
			res := successExecResult(hosts, "")
			if strings.Contains(cmd, "--allow-two-primaries=yes") {
				res.Hosts["10.0.0.2"] = &deployment.HostResult{
					Host: "10.0.0.2", Success: false, Output: "Device is unconfigured",
				}
			}
			return res, nil
		},
	}
	ctrl := newDualPrimaryController(t, dep, &database.Resource{Name: "r1", Nodes: "n1,n2"})

	err := ctrl.resources.SetDualPrimary(context.Background(), "r1", true)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "10.0.0.2")
}

// Disable tolerates the command failing (a down node cannot be dual-primary
// anyway) but proves the outcome via drbdsetup show. Empty show output = the
// resource is unconfigured or already single-primary; both are "not stranded".
func TestSetDualPrimaryDisableToleratesCommandFailureWhenStateIsClean(t *testing.T) {
	dep := &fakeDeploymentClient{
		execFunc: func(_ context.Context, hosts []string, cmd string, _ ...deployment.ExecOption) (*deployment.ExecResult, error) {
			if strings.Contains(cmd, "--allow-two-primaries=no") {
				return nil, fmt.Errorf("ssh: connection refused")
			}
			return successExecResult(hosts, ""), nil
		},
	}
	ctrl := newDualPrimaryController(t, dep, &database.Resource{Name: "r1", Nodes: "n1,n2"})

	assert.NoError(t, ctrl.resources.SetDualPrimary(context.Background(), "r1", false),
		"a failed command with a verified-clean state must not block cleanup")
	assert.NotEmpty(t, execCmdsMatching(dep, "drbdsetup show"), "state must be verified, not assumed")
}

// The one case disable MUST report: a reachable node still advertising
// allow-two-primaries. Silently returning success here would leave a resource
// exposed to a genuine dual-Primary mount.
func TestSetDualPrimaryDisableReportsStrandedNode(t *testing.T) {
	dep := &fakeDeploymentClient{
		execFunc: func(_ context.Context, hosts []string, cmd string, _ ...deployment.ExecOption) (*deployment.ExecResult, error) {
			res := successExecResult(hosts, "")
			if strings.Contains(cmd, "drbdsetup show") {
				res.Hosts["10.0.0.2"] = &deployment.HostResult{
					Host:    "10.0.0.2",
					Success: true,
					Output:  "resource r1 {\n  net {\n    allow-two-primaries yes;\n  }\n}\n",
				}
			}
			return res, nil
		},
	}
	ctrl := newDualPrimaryController(t, dep, &database.Resource{Name: "r1", Nodes: "n1,n2"})

	err := ctrl.resources.SetDualPrimary(context.Background(), "r1", false)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "10.0.0.2")
	assert.Contains(t, err.Error(), "still in dual-primary")
}

// An unreachable node cannot be proven clean, but it is not holding the volume
// open either and re-reads its .res (no dual-primary) on the way back, so it
// must not fail the cleanup path.
func TestSetDualPrimaryDisableIgnoresUnverifiableNode(t *testing.T) {
	dep := &fakeDeploymentClient{
		execFunc: func(_ context.Context, hosts []string, cmd string, _ ...deployment.ExecOption) (*deployment.ExecResult, error) {
			res := successExecResult(hosts, "")
			if strings.Contains(cmd, "drbdsetup show") {
				res.Hosts["10.0.0.2"] = &deployment.HostResult{
					Host: "10.0.0.2", Success: false, Output: "no route to host",
				}
			}
			return res, nil
		},
	}
	ctrl := newDualPrimaryController(t, dep, &database.Resource{Name: "r1", Nodes: "n1,n2"})

	assert.NoError(t, ctrl.resources.SetDualPrimary(context.Background(), "r1", false))
}

func TestSetDualPrimaryRejectsEmptyResource(t *testing.T) {
	ctrl := newDualPrimaryController(t, &fakeDeploymentClient{}, nil)
	require.Error(t, ctrl.resources.SetDualPrimary(context.Background(), "  ", true))
}

func TestHasAllowTwoPrimaries(t *testing.T) {
	tests := []struct {
		name   string
		output string
		want   bool
	}{
		{"empty (unconfigured resource)", "", false},
		{"enabled", "net {\n    allow-two-primaries yes;\n}", true},
		{"explicitly disabled", "net {\n    allow-two-primaries no;\n}", false},
		{"absent (default)", "net {\n    protocol C;\n}", false},
		// "yes" must be read from the allow-two-primaries line only — another
		// option whose value happens to contain "yes" must not trip the check.
		{"unrelated yes value", "net {\n    fencing yes;\n}", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, hasAllowTwoPrimaries(tt.output))
		})
	}
}

// A live migration opens the window on its source and target only: another
// host still attached from a guest it once ran, and down, used to fail every
// migration of that guest.
func TestSetDualPrimaryOnOpensOnlyTheNamedNodes(t *testing.T) {
	dep := &fakeDeploymentClient{}
	ctrl := newDualPrimaryController(t, dep, &database.Resource{
		Name: "pve-100-0", Nodes: "n1,n2", DisklessClients: "pve",
	})

	require.NoError(t, ctrl.resources.SetDualPrimaryOn(context.Background(), "pve-100-0", true, []string{"n1", "pve"}))
	calls := execCmdsMatching(dep, "--allow-two-primaries=yes")
	require.Len(t, calls, 1)
	assert.ElementsMatch(t, []string{"10.0.0.1", "10.0.0.9"}, calls[0].hosts)

	err := ctrl.resources.SetDualPrimaryOn(context.Background(), "pve-100-0", true, []string{"n1", "elsewhere"})
	require.Error(t, err, "a node outside the resource cannot open its window")
	assert.Contains(t, err.Error(), "does not take part")

	// Closing always covers everyone, whatever the caller names.
	require.NoError(t, ctrl.resources.SetDualPrimaryOn(context.Background(), "pve-100-0", false, []string{"n1"}))
	calls = execCmdsMatching(dep, "--allow-two-primaries=no")
	require.Len(t, calls, 1)
	assert.ElementsMatch(t, []string{"10.0.0.1", "10.0.0.2", "10.0.0.9"}, calls[0].hosts)
}
