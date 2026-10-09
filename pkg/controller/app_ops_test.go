package controller

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/haify-project/haify/pkg/database"
)

// recordApp stores the app newAppTestController's resource runs.
func recordApp(t *testing.T, c *Controller) *database.App {
	t.Helper()
	app := &database.App{Name: "orders", Engine: "postgres", Resource: "res1", ServiceIP: "10.0.0.50/24", Port: 5432,
		Device: "/dev/drbd/by-res/res1/0", Server: "/usr/lib/postgresql/16/bin/postgres",
		Client: "/usr/lib/postgresql/16/bin/psql", Admin: "/usr/lib/postgresql/16/bin/pg_isready",
		Init: "/usr/lib/postgresql/16/bin/initdb", Version: "postgres (PostgreSQL) 16.4", UID: 113, GID: 120}
	require.NoError(t, c.db.SaveApp(context.Background(), app))
	return app
}

func TestAppStatus(t *testing.T) {
	tests := []struct {
		name    string
		primary string
		out     string
		state   string
		healthy bool
	}{
		{"running", "node2", "active=active\nhealth=ok\n", AppRunning, true},
		{"not answering", "node2", "active=active\nhealth=fail\n", AppDegraded, false},
		{"unit failed", "node1", "active=failed\nhealth=fail\n", AppDegraded, false},
		{"primary nowhere", "", "", AppStopped, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			nodes := &appNodes{primary: tc.primary, statusOut: tc.out}
			ctrl, _ := newAppTestController(t, nodes, "node1,node2")
			recordApp(t, ctrl)
			st, err := ctrl.appManager().Status(context.Background(), "orders")
			require.NoError(t, err)
			assert.Equal(t, tc.state, st.State)
			assert.Equal(t, tc.primary, st.Primary)
			assert.Equal(t, tc.healthy, st.Healthy)
			assert.Equal(t, []string{"node1", "node2"}, st.Nodes)
			if tc.primary == "" {
				assert.Zero(t, nodes.ran("status"), "nothing to ask when nothing runs")
			}
		})
	}

	ctrl, _ := newAppTestController(t, &appNodes{}, "node1,node2")
	_, err := ctrl.appManager().Status(context.Background(), "missing")
	require.Error(t, err)
	assert.Contains(t, err.Error(), `app "missing" not found`)
}

func TestAppFailover(t *testing.T) {
	nodes := &appNodes{primary: "node1", evictOut: "Node 'node2' took over\n"}
	ctrl, dep := newAppTestController(t, nodes, "node1,node2")
	recordApp(t, ctrl)
	from, to, err := ctrl.appManager().Failover(context.Background(), "orders")
	require.NoError(t, err)
	assert.Equal(t, "node1", from)
	assert.Equal(t, "node2", to)
	var evictHosts []string
	for _, c := range dep.execCalls {
		if strings.Contains(c.cmd, "| base64 -d |") && appScriptKind(decodeB64Script(t, c.cmd)) == "evict" {
			evictHosts = c.hosts
		}
	}
	assert.Equal(t, []string{"node1"}, evictHosts, "evicted on the node running it")

	idle := &appNodes{}
	ctrl, _ = newAppTestController(t, idle, "node1,node2")
	recordApp(t, ctrl)
	_, _, err = ctrl.appManager().Failover(context.Background(), "orders")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not running anywhere")
}

func TestAppDelete(t *testing.T) {
	nodes := &appNodes{}
	ctrl, dep := newAppTestController(t, nodes, "node1,node2")
	recordApp(t, ctrl)
	msg, err := ctrl.appManager().Delete(context.Background(), "orders", false)
	require.NoError(t, err)
	assert.Contains(t, msg, "resource res1 and its data are kept")
	require.Equal(t, 1, nodes.ran("remove"))
	remove := nodes.scripts[0]
	// The config is disabled, and drbd-reactor told, before the chain stops.
	disable := strings.Index(remove, `mv -f "$f" "$f.disabled"`)
	stop := strings.Index(remove, `systemctl stop 'drbd-services@res1.target'`)
	require.True(t, disable >= 0 && stop >= 0, remove)
	assert.Less(t, disable, stop)
	rec, err := ctrl.db.GetApp(context.Background(), "orders")
	require.NoError(t, err)
	assert.Nil(t, rec)
	res, err := ctrl.db.GetResource(context.Background(), "res1")
	require.NoError(t, err)
	assert.NotNil(t, res, "the data stays")
	assert.Empty(t, dep.drbdDownCalls)
}

func TestAppDeleteKeepsTheAppWhenAReplicaDoesNotAnswer(t *testing.T) {
	nodes := &appNodes{removeFail: map[string]bool{"node2": true}}
	ctrl, _ := newAppTestController(t, nodes, "node1,node2")
	recordApp(t, ctrl)
	_, err := ctrl.appManager().Delete(context.Background(), "orders", true)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "node2: unreachable")
	rec, err := ctrl.db.GetApp(context.Background(), "orders")
	require.NoError(t, err)
	assert.NotNil(t, rec, "a replica that may still hold the promoter keeps the app on record")
	res, err := ctrl.db.GetResource(context.Background(), "res1")
	require.NoError(t, err)
	assert.NotNil(t, res, "--delete-data does not run when the app could not be removed")
}

func TestAppSnapshotFreezesAroundTheResourceSnapshot(t *testing.T) {
	nodes := &appNodes{primary: "node1"}
	ctrl, dep := newAppTestController(t, nodes, "node1,node2")
	recordApp(t, ctrl)
	frozen, err := ctrl.appManager().Snapshot(context.Background(), "orders", "nightly")
	require.NoError(t, err)
	assert.True(t, frozen)

	freeze, suspend, thaw := -1, -1, -1
	for i, c := range dep.execCalls {
		switch {
		case strings.Contains(c.cmd, "| base64 -d |"):
			switch appScriptKind(decodeB64Script(t, c.cmd)) {
			case "freeze":
				freeze = i
				assert.Equal(t, []string{"node1"}, c.hosts, "frozen on the Primary")
			case "thaw":
				thaw = i
				assert.Equal(t, []string{"node1"}, c.hosts)
			}
		case strings.Contains(c.cmd, "drbdadm suspend-io res1") && suspend < 0:
			suspend = i
		}
	}
	require.True(t, freeze >= 0 && suspend >= 0 && thaw >= 0, "freeze %d suspend %d thaw %d", freeze, suspend, thaw)
	assert.Less(t, freeze, suspend, "the database is frozen before I/O is suspended")
	assert.Less(t, suspend, thaw, "and thawed after the snapshot")

	_, err = ctrl.appManager().Snapshot(context.Background(), "orders", "bad name!")
	require.Error(t, err)
}

func TestAppSnapshotOfAStoppedAppDoesNotFreeze(t *testing.T) {
	nodes := &appNodes{}
	ctrl, _ := newAppTestController(t, nodes, "node1,node2")
	recordApp(t, ctrl)
	frozen, err := ctrl.appManager().Snapshot(context.Background(), "orders", "nightly")
	require.NoError(t, err)
	assert.False(t, frozen)
	assert.Zero(t, nodes.ran("freeze"))
}

// A replica added after the app was created gets the unit and the promoter —
// once it is shown to run the same engine as the others, with the same ids.
func TestAppPlacementOnANewReplica(t *testing.T) {
	nodes := &appNodes{presence: map[string]string{"node3": "promoter=no\nunit=no\n"}}
	ctrl, dep := newAppTestController(t, nodes, "node1,node2,node3")
	recordApp(t, ctrl)
	require.NoError(t, ctrl.resources.SyncPromoters(context.Background(), "res1"))
	var placed []string
	for _, d := range dep.distributedConfigs {
		if d.remotePath == "/etc/drbd-reactor.d/haify-app-orders.toml" {
			placed = d.hosts
		}
	}
	assert.Equal(t, []string{"node3"}, placed, "only the replica that lacks it")

	other := &appNodes{presence: map[string]string{"node3": "promoter=no\nunit=no\n"},
		probe: map[string]string{"node3": strings.Replace(appProbeOK, "gid=120", "gid=121", 1)}}
	ctrl, dep = newAppTestController(t, other, "node1,node2,node3")
	recordApp(t, ctrl)
	err := ctrl.resources.SyncPromoters(context.Background(), "res1")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "gid=121")
	for _, d := range dep.distributedConfigs {
		assert.NotEqual(t, "/etc/drbd-reactor.d/haify-app-orders.toml", d.remotePath)
	}
}

// `haify ha evict` and a node drain evict through whichever promoter manages
// the resource; an app's is named after the app, so it is found by content.
func TestEvictScriptFindsAnAppPromoter(t *testing.T) {
	root := t.TempDir()
	confDir := filepath.Join(root, "etc", "drbd-reactor.d")
	bin := filepath.Join(root, "bin")
	require.NoError(t, os.MkdirAll(confDir, 0755))
	require.NoError(t, os.MkdirAll(bin, 0755))
	require.NoError(t, os.WriteFile(filepath.Join(bin, "drbd-reactorctl"),
		[]byte("#!/bin/sh\necho \"$@\"\necho \"Node 'n2' took over\"\n"), 0755))
	require.NoError(t, os.WriteFile(filepath.Join(confDir, "haify-app-orders.toml"),
		[]byte("[[promoter]]\n[promoter.resources.res1]\nrunner = \"systemd\"\n"), 0644))
	require.NoError(t, os.WriteFile(filepath.Join(confDir, "haify-app-other.toml"),
		[]byte("[[promoter]]\n[promoter.resources.res10]\n"), 0644))

	script := strings.ReplaceAll(evictScript("res1"), "/etc/drbd-reactor.d", confDir)
	cmd := exec.Command("/bin/sh", "-c", script)
	cmd.Env = append(os.Environ(), "PATH="+bin+":"+os.Getenv("PATH"))
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, string(out))
	assert.Equal(t, "evict haify-app-orders\nNode 'n2' took over", strings.TrimSpace(string(out)))
}
