package controller

import (
	"context"
	"os"
	"os/exec"
	"strings"
	"testing"

	"github.com/liliang-cn/sds/pkg/database"
	"github.com/liliang-cn/sds/pkg/deployment"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRenumberDrbdAddressTouchesOnlyThatAddress(t *testing.T) {
	cfg := `resource r {
  on sdt1 { address 10.0.0.1:7000; }
  on sdt2 { address ipv4 10.0.0.1:7001; }
  on sdt3 { address 10.0.0.10:7000; }
  # 10.0.0.1 was the old router
}`
	got := renumberDrbdAddress(cfg, "10.0.0.1", "10.0.0.9")
	assert.Contains(t, got, "address 10.0.0.9:7000;")
	assert.Contains(t, got, "address ipv4 10.0.0.9:7001;")
	assert.Contains(t, got, "address 10.0.0.10:7000;", "10.0.0.10 is another node")
	assert.Contains(t, got, "# 10.0.0.1 was the old router")
}

// renumberFixture registers sdt1..sdt3 on 10.0.0.1..3 against a fake that
// answers hostname as whatever the address map says, and hands back a config
// for any `cat` of a .res file.
func renumberFixture(t *testing.T, answersAs map[string]string) (*Controller, *fakeDeploymentClient) {
	t.Helper()
	dep := &fakeDeploymentClient{}
	dep.execFunc = func(_ context.Context, hosts []string, cmd string, _ ...deployment.ExecOption) (*deployment.ExecResult, error) {
		res := &deployment.ExecResult{Hosts: map[string]*deployment.HostResult{}}
		for _, h := range hosts {
			out := ""
			switch {
			case cmd == "hostname":
				out = answersAs[h]
			case strings.HasPrefix(cmd, "cat /etc/drbd.d/"):
				out = "resource r1 {\n  on sdt1 { address 10.0.0.1:7000; }\n  on sdt2 { address 10.0.0.2:7000; }\n}\n"
			}
			res.Hosts[h] = &deployment.HostResult{Host: h, Success: true, Output: out}
		}
		return res, nil
	}
	ctrl := newBasicTestController(dep)
	ctrl.db = newTestDB(t)
	ctx := context.Background()
	for i, n := range []string{"sdt1", "sdt2", "sdt3"} {
		addr := "10.0.0." + string(rune('1'+i))
		answersAs[addr] = n
		_, err := ctrl.nodes.RegisterNode(ctx, n, addr)
		require.NoError(t, err)
	}
	_, err := ctrl.nodes.SetNodeLabels(ctx, "sdt1", map[string]string{"rack": "a"}, false)
	require.NoError(t, err)
	return ctrl, dep
}

func TestSetNodeAddressMovesTheNodeEverywhere(t *testing.T) {
	ctrl, dep := renumberFixture(t, map[string]string{"10.0.0.9": "sdt1"})
	ctx := context.Background()
	require.NoError(t, ctrl.db.SaveResource(ctx, &database.Resource{Name: "r1", Nodes: "sdt1,sdt2", Port: 7000}))

	change, err := ctrl.nodes.SetNodeAddress(ctx, "sdt1", "10.0.0.9", "")
	require.NoError(t, err)
	assert.Equal(t, "10.0.0.1", change.OldAddress)
	assert.Equal(t, "10.0.0.9", change.ReplicationAddress, "replication followed the management address")

	assert.Equal(t, "10.0.0.9", ctrl.ResolveHost("sdt1"))
	nodes, err := ctrl.nodes.ListNodes(ctx)
	require.NoError(t, err)
	var sdt1 []string
	for _, n := range nodes {
		if n.Name == "sdt1" {
			sdt1 = append(sdt1, n.Address)
			assert.Equal(t, "a", n.Labels["rack"], "labels kept")
		}
	}
	assert.Equal(t, []string{"10.0.0.9"}, sdt1, "one entry, at the new address")
	old, _ := ctrl.db.GetNode(ctx, "10.0.0.1")
	assert.Nil(t, old, "the old record is gone")
	rec, err := ctrl.db.GetNode(ctx, "10.0.0.9")
	require.NoError(t, err)
	assert.Equal(t, "sdt1", rec.Name)

	ctrl.resources.RenumberInResources(ctx, change)
	assert.Equal(t, []string{"r1"}, change.Resources)
	assert.Empty(t, change.Failed)
	var written string
	for _, d := range dep.distributedConfigs {
		if d.remotePath == "/etc/drbd.d/r1.res" {
			written = d.content
			assert.Contains(t, d.hosts, "10.0.0.9", "the renumbered node gets its new config")
		}
	}
	assert.Contains(t, written, "address 10.0.0.9:7000;")
	assert.NotContains(t, written, "10.0.0.1:")
}

func TestSetNodeAddressRefusesAnotherMachineOrATakenAddress(t *testing.T) {
	ctrl, _ := renumberFixture(t, map[string]string{"10.0.0.9": "somebody-else"})
	ctx := context.Background()
	_, err := ctrl.nodes.SetNodeAddress(ctx, "sdt1", "10.0.0.9", "")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "another machine")
	assert.Equal(t, "10.0.0.1", ctrl.ResolveHost("sdt1"), "nothing moved")

	_, err = ctrl.nodes.SetNodeAddress(ctx, "sdt1", "10.0.0.2", "")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "belongs to node sdt2")

	_, err = ctrl.nodes.SetNodeAddress(ctx, "sdt1", "not-an-ip", "")
	assert.Error(t, err)
}

// A node whose DRBD runs on its own network keeps it when only the management
// address moves, and the resource configs are left alone.
func TestSetNodeAddressKeepsASeparateReplicationNetwork(t *testing.T) {
	ctrl, dep := renumberFixture(t, map[string]string{"10.0.0.9": "sdt1"})
	ctx := context.Background()
	ctrl.nodes.mu.Lock()
	ctrl.nodes.nodes["10.0.0.1"].ReplicationAddress = "172.16.0.1"
	ctrl.nodes.mu.Unlock()
	require.NoError(t, ctrl.db.SaveResource(ctx, &database.Resource{Name: "r1", Nodes: "sdt1,sdt2", Port: 7000}))

	change, err := ctrl.nodes.SetNodeAddress(ctx, "sdt1", "10.0.0.9", "")
	require.NoError(t, err)
	assert.Equal(t, "172.16.0.1", change.ReplicationAddress)
	before := len(dep.distributedConfigs)
	ctrl.resources.RenumberInResources(ctx, change)
	assert.Empty(t, change.Resources)
	assert.Len(t, dep.distributedConfigs, before, "no config rewritten")
}

// Every stale entry for the host moves — including the ones from addresses
// before the last — while loopback and other hosts' lines stay, and the
// rewrite leaves no duplicates.
func TestHostsFileScriptRewritesEveryEntryForTheHost(t *testing.T) {
	dir := t.TempDir()
	f := dir + "/hosts"
	require.NoError(t, os.WriteFile(f, []byte(`127.0.0.1 localhost
127.0.1.1 sdt3
192.168.123.233 sdt3
192.168.123.233	sdt3
192.168.123.225	sdt3
192.168.123.235	sdt1
::1 ip6-localhost
`), 0644))
	script := strings.ReplaceAll(hostsFileScript("sdt3", "192.168.123.237"), "/etc/hosts", f)
	out, err := exec.Command("/bin/sh", "-c", script).CombinedOutput()
	require.NoError(t, err, string(out))
	got, err := os.ReadFile(f)
	require.NoError(t, err)
	assert.Equal(t, `127.0.0.1 localhost
127.0.1.1 sdt3
192.168.123.237 sdt3
192.168.123.235	sdt1
::1 ip6-localhost
`, string(got))
}
