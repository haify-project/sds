package controller

import (
	"context"
	"encoding/base64"
	"fmt"
	"os/exec"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/haify-project/haify/pkg/database"
	"github.com/haify-project/haify/pkg/deployment"
)

// nodeFiles is a fake of the files on each node that the placement code reads
// and writes, keyed by host then path.
type nodeFiles struct {
	files    map[string]map[string]string
	missing  map[string]string // host -> what checkHaChain reports missing there
	retired  []string
	reloaded []string
}

var dumpLoopRE = regexp.MustCompile(`for f in ([^;]+); do`)

func (n *nodeFiles) exec(hosts []string, cmd string) (*deployment.ExecResult, error) {
	script := cmd
	if i := strings.Index(cmd, "echo "); i == 0 {
		if b64, _, ok := strings.Cut(strings.TrimPrefix(cmd, "echo "), " | base64 -d"); ok {
			raw, err := base64.StdEncoding.DecodeString(b64)
			if err == nil {
				script = string(raw)
			}
		}
	}
	res := &deployment.ExecResult{Hosts: map[string]*deployment.HostResult{}}
	for _, h := range hosts {
		out := ""
		switch {
		case strings.Contains(script, "haify-gateway-config"):
			m := dumpLoopRE.FindStringSubmatch(script)
			for _, p := range strings.Fields(m[1]) {
				if c, ok := n.files[h][p]; ok {
					out += fmt.Sprintf("haify-gateway-config %s %s\n", p, base64.StdEncoding.EncodeToString([]byte(c)))
				}
			}
		case strings.Contains(script, "drbd-services@"):
			for p := range n.files[h] {
				if strings.Contains(p, "/drbd-reactor.d/haify-ha-") {
					delete(n.files[h], p)
					n.retired = append(n.retired, h)
				}
			}
		case strings.Contains(script, "LoadState"):
			out = n.missing[h]
		}
		res.Hosts[h] = &deployment.HostResult{Host: h, Success: true, Output: out}
	}
	return res, nil
}

const haConfigForTest = `[[promoter]]
[promoter.resources.data]
runner = "systemd"
start = [
"var-lib-app.mount",
"service-ip@10.0.0.50-24.service",
"postgresql.service",
]
`

func newPlacementFixture(t *testing.T, nodes string, wanDR string) (*Controller, *fakeDeploymentClient, *nodeFiles) {
	t.Helper()
	fs := &nodeFiles{files: map[string]map[string]string{}, missing: map[string]string{}}
	dep := &fakeDeploymentClient{}
	dep.execFunc = func(_ context.Context, hosts []string, cmd string, _ ...deployment.ExecOption) (*deployment.ExecResult, error) {
		return fs.exec(hosts, cmd)
	}
	dep.distributeConfigFunc = func(_ context.Context, hosts []string, content, path string, _ ...deployment.ConfigOption) (*deployment.ConfigResult, error) {
		for _, h := range hosts {
			if fs.files[h] == nil {
				fs.files[h] = map[string]string{}
			}
			fs.files[h][path] = content
		}
		return &deployment.ConfigResult{Success: true, Path: path}, nil
	}
	dep.reactorReloadFunc = func(_ context.Context, hosts []string) (*deployment.ExecResult, error) {
		fs.reloaded = append(fs.reloaded, hosts...)
		return successExecResult(hosts, ""), nil
	}
	ctrl := newBasicTestController(dep)
	ctrl.db = newTestDB(t)
	registerNodes(ctrl, map[string]string{
		"n1": "10.0.0.1", "n2": "10.0.0.2", "n3": "10.0.0.3", "n4": "10.0.0.4",
	})
	res := &database.Resource{Name: "data", Port: 7000, Nodes: nodes, Protocol: "C"}
	if wanDR != "" {
		res.WANMode, res.DRNode = true, wanDR
	}
	require.NoError(t, ctrl.db.SaveResource(context.Background(), res))
	return ctrl, dep, fs
}

func (n *nodeFiles) put(host, path, content string) {
	if n.files[host] == nil {
		n.files[host] = map[string]string{}
	}
	n.files[host][path] = content
}

const haPath = "/etc/drbd-reactor.d/haify-ha-data.toml"
const mountPath = "/etc/systemd/system/var-lib-app.mount"

// A replica added after `ha create` had no promoter, so it could never take
// over. It gets the config the others hold, and the mount unit that config
// starts; the nodes already holding it are not rewritten, because on the node
// running the chain a rewrite and reload is a restart of it.
func TestSyncPromotersPlacesTheHAConfigOnANewReplica(t *testing.T) {
	ctrl, dep, fs := newPlacementFixture(t, "n1,n2,n3", "")
	for _, h := range []string{"10.0.0.1", "10.0.0.2"} {
		fs.put(h, haPath, haConfigForTest)
		fs.put(h, mountPath, "[Mount]\nWhat=/dev/drbd/by-res/data/0\n")
	}

	require.NoError(t, ctrl.resources.SyncPromoters(context.Background(), "data"))

	assert.Equal(t, haConfigForTest, fs.files["10.0.0.3"][haPath])
	assert.Contains(t, fs.files["10.0.0.3"][mountPath], "What=/dev/drbd/by-res/data/0")
	for _, d := range dep.distributedConfigs {
		if strings.HasPrefix(d.remotePath, "/etc/drbd-reactor.d/") || strings.HasPrefix(d.remotePath, "/etc/systemd/system/var") {
			assert.Equal(t, []string{"10.0.0.3"}, d.hosts, "only the replica lacking it is written to (%s)", d.remotePath)
		}
	}
	assert.Equal(t, []string{"10.0.0.3"}, fs.reloaded)
}

// The DR node's copy is asynchronous and may be behind; failing over to it is
// a manual decision. A promoter there made it automatic.
func TestSyncPromotersKeepsTheDRNodeFree(t *testing.T) {
	ctrl, dep, fs := newPlacementFixture(t, "n1,n2,n3", "n3")
	for _, h := range []string{"10.0.0.1", "10.0.0.2", "10.0.0.3"} {
		fs.put(h, haPath, haConfigForTest)
	}

	require.NoError(t, ctrl.resources.SyncPromoters(context.Background(), "data"))

	_, onDR := fs.files["10.0.0.3"][haPath]
	assert.False(t, onDR, "the DR node's promoter is retired")
	assert.Contains(t, fs.retired, "10.0.0.3")
	assert.NotContains(t, fs.retired, "10.0.0.1")
	assert.NotContains(t, fs.retired, "10.0.0.2")
	assert.Empty(t, dep.distributedConfigs, "replicas that hold it are left alone")

	hosts, err := ctrl.resources.failoverHosts(context.Background(), "data")
	require.NoError(t, err)
	sort.Strings(hosts)
	assert.Equal(t, []string{"10.0.0.1", "10.0.0.2"}, hosts)
}

// A removed replica's promoter outlived its copy of the data.
func TestSyncPromotersRetiresAFormerReplica(t *testing.T) {
	ctrl, _, fs := newPlacementFixture(t, "n1,n2", "")
	for _, h := range []string{"10.0.0.1", "10.0.0.2", "10.0.0.4"} {
		fs.put(h, haPath, haConfigForTest)
	}

	require.NoError(t, ctrl.resources.SyncPromoters(context.Background(), "data", "n4"))

	_, left := fs.files["10.0.0.4"][haPath]
	assert.False(t, left)
	assert.Equal(t, []string{"10.0.0.4"}, fs.retired)
}

// A stopped HA config stays stopped on the new replica.
func TestSyncPromotersKeepsAStoppedConfigStopped(t *testing.T) {
	ctrl, _, fs := newPlacementFixture(t, "n1,n2", "")
	fs.put("10.0.0.1", haPath+".disabled", haConfigForTest)
	fs.files["10.0.0.1"][mountPath] = "[Mount]\n"

	require.NoError(t, ctrl.resources.SyncPromoters(context.Background(), "data"))

	assert.Equal(t, haConfigForTest, fs.files["10.0.0.2"][haPath+".disabled"])
	_, live := fs.files["10.0.0.2"][haPath]
	assert.False(t, live, "writing the live .toml would start the service as a side effect")
	assert.Empty(t, fs.reloaded)
}

// A replica whose chain cannot start would look like a standby and never be
// one, so it is refused before anything is provisioned.
func TestPromoterPrereqsRefuseANodeMissingAService(t *testing.T) {
	ctrl, _, fs := newPlacementFixture(t, "n1,n2", "")
	fs.put("10.0.0.1", haPath, haConfigForTest)
	dep := ctrl.deployment.(*fakeDeploymentClient)
	inner := dep.execFunc
	dep.execFunc = func(ctx context.Context, hosts []string, cmd string, opts ...deployment.ExecOption) (*deployment.ExecResult, error) {
		if strings.Contains(cmd, "cat "+haPath) {
			return successExecResult(hosts, haConfigForTest), nil
		}
		return inner(ctx, hosts, cmd, opts...)
	}
	fs.missing["10.0.0.3"] = "missing unit postgresql.service\n"

	err := ctrl.resources.checkPromoterPrereqs(context.Background(), "data", []string{"10.0.0.3"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "postgresql.service")

	assert.NoError(t, ctrl.resources.checkPromoterPrereqs(context.Background(), "data", []string{"10.0.0.2"}))
}

// The node-side scripts are shell; a quoting slip breaks every sync.
func TestPromoterPlacementScriptsParse(t *testing.T) {
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("no sh")
	}
	var captured []string
	dep := &fakeDeploymentClient{execFunc: func(_ context.Context, hosts []string, cmd string, _ ...deployment.ExecOption) (*deployment.ExecResult, error) {
		captured = append(captured, cmd)
		return successExecResult(hosts, ""), nil
	}}
	ctrl := newBasicTestController(dep)
	ctrl.resources.retireHaPromoter(context.Background(), "my-data", []string{"10.0.0.1"})
	require.Len(t, captured, 1)
	b64, _, _ := strings.Cut(strings.TrimPrefix(captured[0], "echo "), " | base64 -d")
	script, err := base64.StdEncoding.DecodeString(b64)
	require.NoError(t, err)
	out, err := exec.Command("sh", "-n", "-c", string(script)).CombinedOutput()
	require.NoError(t, err, string(out))
	assert.Contains(t, string(script), `drbd-services@my\x2ddata.target`)
}
