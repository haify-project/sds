package controller

import (
	"context"
	"os"
	"os/exec"
	"strings"
	"testing"

	"github.com/haify-project/haify/pkg/database"
	"github.com/haify-project/haify/pkg/deployment"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

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
	require.Len(t, change.Moves, 1)
	assert.Equal(t, "10.0.0.1", change.Moves[0].OldAddress)
	assert.Equal(t, "10.0.0.9", change.Moves[0].ReplicationAddress, "replication followed the management address")

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
	assert.Contains(t, err.Error(), "would belong to both")

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
	assert.Equal(t, "172.16.0.1", change.Moves[0].ReplicationAddress)
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

// The Lima state after a swap was renumbered one node at a time: sdt1 and
// sdt2 both on .205. Reconciling from the registry fixes it without knowing
// what anything used to be, and leaves a WAN leg's loopback alone.
func TestReconcileDrbdAddressesFromTheRegistry(t *testing.T) {
	cfg := `resource r3 {
    volume 0 {
        device    minor 2;
    }

    on sdt1 {
        address   192.168.123.205:7102;
        node-id   0;
    }

    on sdt3 {
        address   ipv4 192.168.123.225:7102;
        node-id   1;
    }

    on sdt2 {
        address   192.168.123.205:7102;
        node-id   2;
        volume 0 {
            disk      none;
        }
    }

    on dr1 {
        address   127.0.0.1:7150;
    }

    on stranger {
        address   10.9.9.9:7102;
    }

    connection-mesh {
        hosts sdt1 sdt3 sdt2;
    }
}`
	got := reconcileDrbdAddresses(cfg, map[string]string{
		"sdt1": "192.168.123.205", "sdt2": "192.168.123.206", "sdt3": "192.168.123.217", "dr1": "10.0.0.50",
	})
	assert.Contains(t, got, "on sdt1 {\n        address   192.168.123.205:7102;")
	assert.Contains(t, got, "on sdt2 {\n        address   192.168.123.206:7102;")
	assert.Contains(t, got, "on sdt3 {\n        address   ipv4 192.168.123.217:7102;")
	assert.Contains(t, got, "address   127.0.0.1:7150;", "a WAN leg's loopback stays")
	assert.Contains(t, got, "address   10.9.9.9:7102;", "an unregistered host stays")
	assert.Equal(t, strings.Count(cfg, "\n"), strings.Count(got, "\n"))
}

// Two nodes trading addresses, as a DHCP server does when it hands out new
// leases, in one call: nothing is lost halfway and the configs end right.
func TestSetNodeAddressesSwapsTwoNodes(t *testing.T) {
	ctx := context.Background()
	// While sdt2 still answers on 10.0.0.2, sdt1 cannot be moved onto it.
	ctrl, _ := renumberFixture(t, map[string]string{"10.0.0.9": "sdt2"})
	_, err := ctrl.nodes.SetNodeAddresses(ctx, []AddressMove{
		{Node: "sdt1", Address: "10.0.0.2"}, {Node: "sdt2", Address: "10.0.0.9"},
	})
	require.Error(t, err, "10.0.0.2 answers as sdt2, not sdt1")
	assert.Contains(t, err.Error(), "another machine")

	// Order the moves so the naive sequential approach would clobber sdt2.
	ctrl, dep := renumberFixture(t, map[string]string{"10.0.0.9": "sdt2"})
	require.NoError(t, ctrl.db.SaveResource(ctx, &database.Resource{Name: "r1", Nodes: "sdt1,sdt2", Port: 7000}))
	// Make 10.0.0.2 answer as sdt1 now: the node that had it moved away.
	ans := map[string]string{"10.0.0.2": "sdt1", "10.0.0.9": "sdt2"}
	inner := dep.execFunc
	dep.execFunc = func(c context.Context, hosts []string, cmd string, o ...deployment.ExecOption) (*deployment.ExecResult, error) {
		if cmd == "hostname" {
			return successExecResult(hosts, ans[hosts[0]]), nil
		}
		return inner(c, hosts, cmd, o...)
	}
	change, err := ctrl.nodes.SetNodeAddresses(ctx, []AddressMove{
		{Node: "sdt1", Address: "10.0.0.2"}, {Node: "sdt2", Address: "10.0.0.9"},
	})
	require.NoError(t, err)
	assert.Len(t, change.Moves, 2)
	assert.Equal(t, "10.0.0.2", ctrl.ResolveHost("sdt1"))
	assert.Equal(t, "10.0.0.9", ctrl.ResolveHost("sdt2"))
	nodes, err := ctrl.nodes.ListNodes(ctx)
	require.NoError(t, err)
	got := map[string]string{}
	for _, n := range nodes {
		got[n.Name] = n.Address
	}
	assert.Equal(t, map[string]string{"sdt1": "10.0.0.2", "sdt2": "10.0.0.9", "sdt3": "10.0.0.3"}, got)

	ctrl.resources.RenumberInResources(ctx, change)
	assert.Equal(t, []string{"r1"}, change.Resources)
	for _, d := range dep.distributedConfigs {
		if d.remotePath == "/etc/drbd.d/r1.res" {
			assert.Contains(t, d.content, "on sdt1 { address 10.0.0.2:7000; }")
			assert.Contains(t, d.content, "on sdt2 { address 10.0.0.9:7000; }")
		}
	}
}

// Two nodes trading addresses: every stale entry must be gone before either
// node's key is written, or the second removal deletes the first node's fresh
// entry.
func TestKnownHostsScriptForgetsBeforeAdding(t *testing.T) {
	script := knownHostsScript([]string{"10.0.0.1", "10.0.0.2", "10.0.0.2", "10.0.0.1", ""},
		"10.0.0.2 ssh-ed25519 AAAAa\n10.0.0.1 ssh-ed25519 AAAAb\n")
	lastForget := strings.LastIndex(script, "ssh-keygen -R")
	add := strings.Index(script, "HAIFY_KNOWN_HOSTS")
	if lastForget < 0 || add < 0 || lastForget > add {
		t.Fatalf("every ssh-keygen -R must come before the keys are added:\n%s", script)
	}
	if n := strings.Count(script, "ssh-keygen -R"); n != 2 {
		t.Fatalf("each address is forgotten once, got %d:\n%s", n, script)
	}
	// The here-document must end on its own delimiter line, or the shell
	// appends the rest of the script to known_hosts.
	if !strings.Contains(script, "<<'HAIFY_KNOWN_HOSTS'\n") || !strings.Contains(script, "\nHAIFY_KNOWN_HOSTS\n") {
		t.Fatalf("the here-document's delimiters do not match:\n%s", script)
	}
}
