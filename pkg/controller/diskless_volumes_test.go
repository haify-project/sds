package controller

import (
	"context"
	"strings"
	"testing"

	"github.com/haify-project/sds/pkg/database"
	"github.com/haify-project/sds/pkg/deployment"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// xplatBeforeStateVolume is the NFS gateway resource from the orange test
// cluster, verbatim, as it stood before its second (state) volume was added:
// two diskful nodes and orange3 as a diskless tiebreaker.
const xplatBeforeStateVolume = `resource xplat {

    options {
        auto-promote no;
        quorum majority;
    }

    net {
        protocol C;
    }

    volume 0 {
        device    minor 24;
        disk      /dev/sds_vg0/xplat_data;
        meta-disk internal;
    }

    on orange1 {
        address   192.168.123.214:7420;
        node-id   0;
    }

    on orange2 {
        address   192.168.123.215:7420;
        node-id   1;
    }

    on orange3 {
        address   192.168.123.216:7420;
        node-id   2;
        volume 0 {
            device    minor 24;
            disk      none;
        }
    }

    connection-mesh {
        hosts orange1 orange2 orange3;
    }
}
`

// onStanza returns the text of one `on <host>` stanza.
func onStanza(t *testing.T, cfg, host string) string {
	t.Helper()
	i := strings.Index(cfg, "on "+host+" {")
	require.GreaterOrEqual(t, i, 0, "no on-stanza for %s", host)
	depth := 0
	for j := i; j < len(cfg); j++ {
		switch cfg[j] {
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return cfg[i : j+1]
			}
		}
	}
	t.Fatalf("unterminated on-stanza for %s", host)
	return ""
}

// The tiebreaker's stanza gains the new volume as `disk none`, the diskful
// stanzas are left alone, and doing it twice changes nothing.
func TestAddingAVolumeGivesTheTiebreakerADisklessOverride(t *testing.T) {
	out := addDisklessVolumeOverrides(xplatBeforeStateVolume, 1, 25)

	tb := onStanza(t, out, "orange3")
	assert.Contains(t, tb, "volume 1 {")
	assert.Contains(t, tb, "device    minor 25;")
	assert.Equal(t, 2, strings.Count(tb, "disk      none;"), "both volumes diskless on the tiebreaker")

	for _, h := range []string{"orange1", "orange2"} {
		assert.NotContains(t, onStanza(t, out, h), "volume", "%s is diskful; its stanza must not be touched", h)
	}
	assert.Equal(t, out, addDisklessVolumeOverrides(out, 1, 25), "applying it again must not add a second override")
}

func TestRemovingAVolumeDropsOnlyItsDisklessOverride(t *testing.T) {
	withBoth := addDisklessVolumeOverrides(xplatBeforeStateVolume, 1, 25)
	out := removeDisklessVolumeOverrides(withBoth, 1)

	tb := onStanza(t, out, "orange3")
	assert.NotContains(t, tb, "volume 1")
	assert.Contains(t, tb, "volume 0 {", "the other volume's override must survive")
	assert.Equal(t, xplatBeforeStateVolume, out, "removing what was added restores the original exactly")
}

func TestAResourceWithoutDisklessNodesIsUnchanged(t *testing.T) {
	cfg := strings.Replace(xplatBeforeStateVolume,
		"        volume 0 {\n            device    minor 24;\n            disk      none;\n        }\n", "", 1)
	assert.Equal(t, cfg, addDisklessVolumeOverrides(cfg, 1, 25))
}

// The bug as it happened: the state volume of a gateway reached only the two
// diskful nodes, the tiebreaker's config never learned of it, DRBD refused the
// tiebreaker's connection, and the gateway ran without a vote to spare.
func TestAddVolumeReachesTheTiebreaker(t *testing.T) {
	dep := &fakeDeploymentClient{
		execFunc: func(ctx context.Context, hosts []string, cmd string, opts ...deployment.ExecOption) (*deployment.ExecResult, error) {
			if strings.HasPrefix(cmd, "cat /etc/drbd.d/xplat.res") {
				return successExecResult(hosts, xplatBeforeStateVolume), nil
			}
			return successExecResult(hosts, ""), nil
		},
	}
	ctrl := newBasicTestController(dep)
	ctrl.db = newTestDB(t)
	require.NoError(t, ctrl.db.SaveResource(context.Background(), &database.Resource{
		Name: "xplat", Port: 7420, Nodes: "orange1,orange2", DisklessNodes: "orange3", Protocol: "C", Replicas: 2,
	}))
	for i, n := range []string{"orange1", "orange2", "orange3"} {
		addr := "192.168.123." + []string{"214", "215", "216"}[i]
		ctrl.nodes.nodes[addr] = &NodeInfo{Name: n, Address: addr}
		ctrl.hostsMap[n] = addr
	}

	require.NoError(t, ctrl.resources.AddVolume(context.Background(), "xplat", "xplat_state1", "vg0", 1))

	require.NotEmpty(t, dep.distributedConfigs)
	fwd := dep.distributedConfigs[0]
	assert.Contains(t, fwd.hosts, "192.168.123.216", "the tiebreaker must receive the new config")
	assert.Contains(t, onStanza(t, fwd.content, "orange3"), "volume 1 {", "and it must describe the new volume as diskless")

	var adjusted, createdMDOnTiebreaker bool
	for _, c := range dep.execCalls {
		for _, h := range c.hosts {
			if h != "192.168.123.216" {
				continue
			}
			if strings.Contains(c.cmd, "drbdadm adjust") {
				adjusted = true
			}
			if strings.Contains(c.cmd, "create-md") {
				createdMDOnTiebreaker = true
			}
		}
	}
	assert.True(t, adjusted, "the tiebreaker must be adjusted to bring the new volume up")
	assert.False(t, createdMDOnTiebreaker, "a diskless node has no disk to put metadata on")
	for _, c := range dep.lvCreateCalls {
		assert.NotEqual(t, "192.168.123.216", strings.Join(c.hosts, ","), "no LV on the tiebreaker")
	}
}

// A resource left broken by the old AddVolume: two volumes, the tiebreaker's
// stanza overriding only the first. Repair gives it the second, sends the file
// to the tiebreaker, and adjusts the tiebreaker before the diskful nodes —
// which the kernel requires, since a diskful node may only drop the bitmap for
// a peer's volume once that peer has connected as diskless.
func TestRepairReconcilesTheTiebreakerAndAdjustsItFirst(t *testing.T) {
	broken := strings.Replace(xplatBeforeStateVolume, "    on orange1 {",
		"    volume 1 {\n        device    minor 25;\n        disk      /dev/sds_vg0/xplat_state1;\n        meta-disk internal;\n    }\n\n    on orange1 {", 1)
	dep := &fakeDeploymentClient{
		execFunc: func(ctx context.Context, hosts []string, cmd string, opts ...deployment.ExecOption) (*deployment.ExecResult, error) {
			if strings.HasPrefix(cmd, "cat /etc/drbd.d/xplat.res") {
				return successExecResult(hosts, broken), nil
			}
			return successExecResult(hosts, ""), nil
		},
	}
	ctrl := newBasicTestController(dep)
	ctrl.db = newTestDB(t)
	require.NoError(t, ctrl.db.SaveResource(context.Background(), &database.Resource{
		Name: "xplat", Port: 7420, Nodes: "orange1,orange2", DisklessNodes: "orange3", Protocol: "C", Replicas: 2,
	}))
	for i, n := range []string{"orange1", "orange2", "orange3"} {
		addr := "192.168.123." + []string{"214", "215", "216"}[i]
		ctrl.nodes.nodes[addr] = &NodeInfo{Name: n, Address: addr}
		ctrl.hostsMap[n] = addr
	}

	require.NoError(t, ctrl.resources.RepairResourceConfig(context.Background(), "xplat"))

	require.Len(t, dep.distributedConfigs, 1)
	d := dep.distributedConfigs[0]
	assert.ElementsMatch(t, []string{"192.168.123.214", "192.168.123.215", "192.168.123.216"}, d.hosts)
	tb := onStanza(t, d.content, "orange3")
	assert.Contains(t, tb, "volume 1 {")
	assert.Equal(t, 2, strings.Count(tb, "disk      none;"))

	var order []string
	for _, c := range dep.execCalls {
		if strings.Contains(c.cmd, "drbdadm adjust") {
			order = append(order, strings.Join(c.hosts, ","))
		}
	}
	require.Len(t, order, 2)
	assert.Equal(t, "192.168.123.216", order[0], "the tiebreaker is adjusted first")
	assert.Equal(t, "192.168.123.214,192.168.123.215", order[1])
}

// On a thin pool the added volume is a thin LV, exactly as resource creation
// makes it. It used to be a thick lvcreate that failed on every node — the thin
// pool had taken the volume group — and the add carried on to create-md
// against a device that did not exist.
func TestAddVolumeOnAThinPoolMakesAThinVolume(t *testing.T) {
	var thin []string
	dep := &fakeDeploymentClient{
		execFunc: func(ctx context.Context, hosts []string, cmd string, opts ...deployment.ExecOption) (*deployment.ExecResult, error) {
			if strings.HasPrefix(cmd, "cat /etc/drbd.d/xplat.res") {
				return successExecResult(hosts, xplatBeforeStateVolume), nil
			}
			return successExecResult(hosts, ""), nil
		},
		lvThinPoolInFunc: func(_ context.Context, _, vg string) (string, error) { return vg + "_thin", nil },
		lvCreateThinVolumeFunc: func(_ context.Context, hosts []string, _, _, lv, _ string) (*deployment.ExecResult, error) {
			thin = append(thin, lv)
			return successExecResult(hosts, ""), nil
		},
		lvCreateFunc: func(_ context.Context, hosts []string, _, _, _ string) (*deployment.ExecResult, error) {
			t.Error("a thick lvcreate must not be used on a thin pool")
			return successExecResult(hosts, ""), nil
		},
	}
	ctrl := newBasicTestController(dep)
	ctrl.db = newTestDB(t)
	require.NoError(t, ctrl.db.SaveResource(context.Background(), &database.Resource{
		Name: "xplat", Port: 7420, Nodes: "orange1,orange2", DisklessNodes: "orange3", Protocol: "C", Replicas: 2,
	}))
	for i, n := range []string{"orange1", "orange2", "orange3"} {
		addr := "192.168.123." + []string{"214", "215", "216"}[i]
		ctrl.nodes.nodes[addr] = &NodeInfo{Name: n, Address: addr}
		ctrl.hostsMap[n] = addr
	}

	require.NoError(t, ctrl.resources.AddVolume(context.Background(), "xplat", "xplat_state1", "vg0", 1))
	assert.Equal(t, []string{"xplat_state1", "xplat_state1"}, thin, "one thin LV on each diskful node")
}

// A backing volume that cannot be created stops the add there, before any
// metadata is written or the config is extended on the peers.
func TestAddVolumeStopsWhenTheBackingVolumeFails(t *testing.T) {
	dep := &fakeDeploymentClient{
		execFunc: func(ctx context.Context, hosts []string, cmd string, opts ...deployment.ExecOption) (*deployment.ExecResult, error) {
			if strings.HasPrefix(cmd, "cat /etc/drbd.d/xplat.res") {
				return successExecResult(hosts, xplatBeforeStateVolume), nil
			}
			if strings.Contains(cmd, "create-md") {
				t.Error("create-md ran after the backing volume failed")
			}
			return successExecResult(hosts, ""), nil
		},
		lvCreateFunc: func(_ context.Context, hosts []string, _, _, _ string) (*deployment.ExecResult, error) {
			r := successExecResult(hosts, "")
			for _, h := range r.Hosts {
				h.Success, h.Output = false, "Volume group \"vg0\" has insufficient free space"
			}
			return r, nil
		},
	}
	ctrl := newBasicTestController(dep)
	ctrl.db = newTestDB(t)
	require.NoError(t, ctrl.db.SaveResource(context.Background(), &database.Resource{
		Name: "xplat", Port: 7420, Nodes: "orange1,orange2", Protocol: "C", Replicas: 2,
	}))
	for i, n := range []string{"orange1", "orange2"} {
		addr := "192.168.123." + []string{"214", "215"}[i]
		ctrl.nodes.nodes[addr] = &NodeInfo{Name: n, Address: addr}
		ctrl.hostsMap[n] = addr
	}

	err := ctrl.resources.AddVolume(context.Background(), "xplat", "xplat_state1", "vg0", 1)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "insufficient free space")
}

// The tiebreaker's `volume 1 { disk none; }` override can come before the
// resource's own volume 1 in the file. Taking the first "volume 1" found the
// override, and resize ran `lvresize ... none`.
func TestConfigVolumesAreTheTopLevelOnesNotPerHostOverrides(t *testing.T) {
	cfg := `resource r5 {
    volume 0 {
        device    minor 4;
        disk      /dev/sds_tp/r5_data;
        meta-disk internal;
    }
    on sdt1 {
        address   192.168.123.232:7104;
        node-id   0;
    }
    on sdt3 {
        address   192.168.123.233:7104;
        node-id   2;
        volume 0 {
            device    minor 4;
            disk      none;
        }
        volume 1 {
            device    minor 5;
            disk      none;
        }
    }
    volume 1 {
        device    minor 5;
        disk      /dev/sds_tp/r5_extra;
        meta-disk internal;
    }
}
`
	vols := parseResourceConfigVolumes(cfg)
	require.Len(t, vols, 2)
	assert.Equal(t, "/dev/sds_tp/r5_data", vols[0].DiskPath)
	assert.Equal(t, 1, vols[1].VolumeID)
	assert.Equal(t, "/dev/sds_tp/r5_extra", vols[1].DiskPath, "not the tiebreaker's disk none")

	// A resource written per host, with no top-level volumes, is still read.
	perHost := `resource old {
    on a {
        volume 0 {
            device minor 1;
            disk /dev/vg/old;
        }
    }
}
`
	vols = parseResourceConfigVolumes(perHost)
	require.Len(t, vols, 1)
	assert.Equal(t, "/dev/vg/old", vols[0].DiskPath)
}
