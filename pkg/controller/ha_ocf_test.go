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

// A realistic (trimmed) OCF meta-data document, in the shape emitted by
// ocf:heartbeat:IPaddr2 meta-data.
const sampleIPaddr2MetaData = `<?xml version="1.0"?>
<!DOCTYPE resource-agent SYSTEM "ra-api-1.dtd">
<resource-agent name="IPaddr2" version="1.0">
<version>1.0</version>
<longdesc lang="en">This Linux-specific resource manages IP alias IP addresses.</longdesc>
<shortdesc lang="en">Manages virtual IPv4 and IPv6 addresses</shortdesc>
<parameters>
<parameter name="ip" unique="1" required="1">
<longdesc lang="en">The IPv4 (dotted quad notation) or IPv6 address.</longdesc>
<shortdesc lang="en">IPv4 or IPv6 address</shortdesc>
<content type="string" default=""/>
</parameter>
<parameter name="cidr_netmask" unique="0" required="0">
<longdesc lang="en">The netmask for the interface in CIDR format.</longdesc>
<shortdesc lang="en">CIDR netmask</shortdesc>
<content type="string" default="24"/>
</parameter>
</parameters>
<actions>
<action name="start" timeout="20s"/>
<action name="stop" timeout="20s"/>
</actions>
</resource-agent>`

func TestParseOCFMetaData(t *testing.T) {
	meta, err := parseOCFMetaData([]byte(sampleIPaddr2MetaData))
	require.NoError(t, err)

	assert.Equal(t, "IPaddr2", meta.Name)
	assert.Equal(t, "1.0", meta.Version)
	assert.Equal(t, "Manages virtual IPv4 and IPv6 addresses", meta.Shortdesc)
	assert.Contains(t, meta.Longdesc, "manages IP alias")
	require.Len(t, meta.Parameters, 2)

	ip := meta.Parameters[0]
	assert.Equal(t, "ip", ip.Name)
	assert.True(t, ip.Required, "ip must be required")
	assert.True(t, ip.Unique, "ip must be unique")
	assert.Equal(t, "string", ip.Type)
	assert.Equal(t, "", ip.Default)
	assert.Equal(t, "IPv4 or IPv6 address", ip.Shortdesc)

	mask := meta.Parameters[1]
	assert.Equal(t, "cidr_netmask", mask.Name)
	assert.False(t, mask.Required)
	assert.False(t, mask.Unique)
	assert.Equal(t, "24", mask.Default, "cidr_netmask default must be parsed")
}

func TestParseOCFMetaDataRejectsGarbage(t *testing.T) {
	_, err := parseOCFMetaData([]byte("not xml at all"))
	assert.Error(t, err)
}

func TestParseResourceAgentPaths(t *testing.T) {
	output := strings.Join([]string{
		"/usr/lib/ocf/resource.d/heartbeat/IPaddr2",
		"/usr/lib/ocf/resource.d/heartbeat/Filesystem",
		"/usr/lib/ocf/resource.d/heartbeat/IPaddr2", // duplicate
		"/usr/lib/ocf/resource.d/linbit/drbd",
		"/usr/lib/ocf/resource.d/heartbeat/.ocf-shellfuncs", // hidden helper
		"/usr/lib/ocf/resource.d/heartbeat/helper.sh",       // shell helper
		"/some/other/path",                                  // wrong prefix
		"",
	}, "\n")

	agents := parseResourceAgentPaths(output)
	require.Len(t, agents, 3)
	assert.Equal(t, ocfResourceAgent{Provider: "heartbeat", Name: "IPaddr2"}, agents[0])
	assert.Equal(t, ocfResourceAgent{Provider: "heartbeat", Name: "Filesystem"}, agents[1])
	assert.Equal(t, ocfResourceAgent{Provider: "linbit", Name: "drbd"}, agents[2])
}

func TestRenderOcfStartEntrySortsKeys(t *testing.T) {
	entry := renderOcfStartEntry(OcfAgentSpec{
		Provider: "heartbeat",
		Name:     "IPaddr2",
		Instance: "vip_pgha",
		Params:   map[string]string{"ip": "192.168.1.50", "cidr_netmask": "24"},
	})
	assert.Equal(t, "ocf:heartbeat:IPaddr2 vip_pgha cidr_netmask=24 ip=192.168.1.50", entry)

	// Missing provider/name yields an empty (skippable) entry.
	assert.Equal(t, "", renderOcfStartEntry(OcfAgentSpec{Name: "x"}))
}

func TestGeneratePromoterConfigComposesOcfAgents(t *testing.T) {
	rm := NewResourceManager(newBasicTestController(&fakeDeploymentClient{}))
	cfg := rm.generatePromoterConfig("pgha", []string{"postgresql.service"}, "/var/lib/pgha", "192.168.1.50/24", []OcfAgentSpec{
		{Provider: "heartbeat", Name: "IPaddr2", Instance: "vip_pgha", Params: map[string]string{"ip": "192.168.1.50", "cidr_netmask": "24"}},
	})
	assert.Contains(t, cfg, "[[promoter]]")
	assert.Contains(t, cfg, `"var-lib-pgha.mount"`)
	assert.Contains(t, cfg, `"postgresql.service"`)
	assert.Contains(t, cfg, `"ocf:heartbeat:IPaddr2 vip_pgha cidr_netmask=24 ip=192.168.1.50"`)
}

// TestGeneratePromoterConfigOrdered verifies the ordered renderer emits start[]
// in the exact given order with systemd units and OCF agents as peers — the
// thing the legacy bucketed generator cannot express. Mirrors a correct NFS
// stack: portblock -> Filesystem -> IPaddr2 -> nfsserver -> exportfs -> portunblock.
func TestGeneratePromoterConfigOrdered(t *testing.T) {
	rm := NewResourceManager(newBasicTestController(&fakeDeploymentClient{}))
	ocf := func(name, inst string, p map[string]string) HaStartItem {
		return HaStartItem{Ocf: &OcfAgentSpec{Provider: "heartbeat", Name: name, Instance: inst, Params: p}}
	}
	cfg := rm.generatePromoterConfigOrdered("nfs1", []HaStartItem{
		ocf("portblock", "pb_pre", map[string]string{"action": "block", "portno": "2049", "protocol": "tcp"}),
		ocf("Filesystem", "fs_1", map[string]string{"directory": "/srv/nfs", "fstype": "ext4"}),
		ocf("IPaddr2", "vip", map[string]string{"ip": "192.168.1.50", "cidr_netmask": "24"}),
		{SystemdUnit: "nfs-server.service"},
		ocf("exportfs", "exp_1", map[string]string{"directory": "/srv/nfs"}),
		ocf("portblock", "pb_post", map[string]string{"action": "unblock", "portno": "2049", "protocol": "tcp"}),
	})

	// The start[] items must appear in the exact order supplied — a systemd unit
	// sitting BETWEEN OCF agents, which the bucketed generator could never do.
	fsIdx := strings.Index(cfg, "fs_1")
	vipIdx := strings.Index(cfg, "vip cidr_netmask")
	svcIdx := strings.Index(cfg, "nfs-server.service")
	expIdx := strings.Index(cfg, "exp_1")
	assert.Greater(t, vipIdx, fsIdx, "IPaddr2 must come after Filesystem")
	assert.Greater(t, svcIdx, vipIdx, "the systemd service must come after the VIP")
	assert.Greater(t, expIdx, svcIdx, "exportfs must come after the service")
	assert.Contains(t, cfg, `"nfs-server.service"`)
	assert.Contains(t, cfg, `"ocf:heartbeat:portblock pb_pre action=block portno=2049 protocol=tcp"`)
}

func makeHaTestController(t *testing.T, dep *fakeDeploymentClient) *Controller {
	t.Helper()
	ctrl := newBasicTestController(dep)
	db := newTestDB(t)
	ctrl.db = db

	require.NoError(t, ctrl.db.SaveResource(context.Background(), &database.Resource{
		Name:     "res1",
		Port:     7001,
		Nodes:    "node1,node2",
		Protocol: "C",
		Replicas: 2,
	}))
	ctrl.nodes.nodes["10.0.0.1"] = &NodeInfo{Name: "node1", Address: "10.0.0.1"}
	ctrl.nodes.nodes["10.0.0.2"] = &NodeInfo{Name: "node2", Address: "10.0.0.2"}
	ctrl.hostsMap["node1"] = "10.0.0.1"
	ctrl.hostsMap["node2"] = "10.0.0.2"
	return ctrl
}

func TestMakeHaComposesOcfAgentsIntoStartList(t *testing.T) {
	dep := &fakeDeploymentClient{
		execFunc: func(ctx context.Context, hosts []string, cmd string, opts ...deployment.ExecOption) (*deployment.ExecResult, error) {
			if strings.Contains(cmd, "drbdadm status") {
				return successExecResult(hosts, "res1 role:Primary\n"), nil
			}
			return successExecResult(hosts, ""), nil
		},
	}
	ctrl := makeHaTestController(t, dep)

	_, err := ctrl.resources.MakeHa(context.Background(), "res1", nil, "", "", "", []OcfAgentSpec{
		{Provider: "heartbeat", Name: "IPaddr2", Instance: "vip_res1", Params: map[string]string{"ip": "192.168.1.50", "cidr_netmask": "24"}},
	}, nil)
	require.NoError(t, err)

	var promoter string
	for _, dc := range dep.distributedConfigs {
		if dc.remotePath == "/etc/drbd-reactor.d/sds-ha-res1.toml" {
			promoter = dc.content
		}
	}
	require.NotEmpty(t, promoter, "promoter config must be distributed")
	assert.Contains(t, promoter, `"ocf:heartbeat:IPaddr2 vip_res1 cidr_netmask=24 ip=192.168.1.50"`)
}

func TestSyncHaTomlValidation(t *testing.T) {
	ctrl := makeHaTestController(t, &fakeDeploymentClient{})

	_, err := ctrl.resources.SyncHaToml(context.Background(), "res1", "   ")
	assert.ErrorContains(t, err, "empty")

	_, err = ctrl.resources.SyncHaToml(context.Background(), "res1", "runner = \"systemd\"\n")
	assert.ErrorContains(t, err, "[[promoter]]")
}

func TestSyncHaTomlDistributesAndReloads(t *testing.T) {
	var reloaded [][]string
	dep := &fakeDeploymentClient{
		reactorReloadFunc: func(ctx context.Context, hosts []string) (*deployment.ExecResult, error) {
			reloaded = append(reloaded, append([]string(nil), hosts...))
			return successExecResult(hosts, ""), nil
		},
	}
	ctrl := makeHaTestController(t, dep)

	content := "[[promoter]]\n[promoter.resources.res1]\nrunner = \"systemd\"\nstart = []\n"
	msg, err := ctrl.resources.SyncHaToml(context.Background(), "res1", content)
	require.NoError(t, err)
	assert.Contains(t, msg, "2 nodes")

	require.Len(t, dep.distributedConfigs, 1)
	assert.Equal(t, "/etc/drbd-reactor.d/sds-ha-res1.toml", dep.distributedConfigs[0].remotePath)
	assert.Equal(t, content, dep.distributedConfigs[0].content)
	assert.Equal(t, []string{"10.0.0.1", "10.0.0.2"}, dep.distributedConfigs[0].hosts)
	require.Len(t, reloaded, 1)
	assert.Equal(t, []string{"10.0.0.1", "10.0.0.2"}, reloaded[0])
}

func TestSyncHaTomlReportsReloadFailure(t *testing.T) {
	dep := &fakeDeploymentClient{
		reactorReloadFunc: func(ctx context.Context, hosts []string) (*deployment.ExecResult, error) {
			res := successExecResult(hosts, "")
			res.Hosts["10.0.0.2"] = &deployment.HostResult{Host: "10.0.0.2", Success: false, Output: "boom"}
			return res, nil
		},
	}
	ctrl := makeHaTestController(t, dep)

	_, err := ctrl.resources.SyncHaToml(context.Background(), "res1", "[[promoter]]\nstart = []\n")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "10.0.0.2")
}

func TestGetHaTomlMissing(t *testing.T) {
	dep := &fakeDeploymentClient{
		execFunc: func(ctx context.Context, hosts []string, cmd string, opts ...deployment.ExecOption) (*deployment.ExecResult, error) {
			return successExecResult(hosts, "__SDS_HA_TOML_MISSING__\n"), nil
		},
	}
	ctrl := makeHaTestController(t, dep)

	_, _, err := ctrl.resources.GetHaToml(context.Background(), "res1")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no HA config")
}

func TestGetHaTomlReadsContent(t *testing.T) {
	content := "[[promoter]]\n[promoter.resources.res1]\n"
	dep := &fakeDeploymentClient{
		execFunc: func(ctx context.Context, hosts []string, cmd string, opts ...deployment.ExecOption) (*deployment.ExecResult, error) {
			return successExecResult(hosts, content), nil
		},
	}
	ctrl := makeHaTestController(t, dep)

	path, got, err := ctrl.resources.GetHaToml(context.Background(), "res1")
	require.NoError(t, err)
	assert.Equal(t, "/etc/drbd-reactor.d/sds-ha-res1.toml", path)
	assert.Equal(t, content, got)
}

func TestListResourceAgents(t *testing.T) {
	dep := &fakeDeploymentClient{
		execFunc: func(ctx context.Context, hosts []string, cmd string, opts ...deployment.ExecOption) (*deployment.ExecResult, error) {
			if strings.Contains(cmd, "resource.d") {
				return successExecResult(hosts, "/usr/lib/ocf/resource.d/heartbeat/IPaddr2\n/usr/lib/ocf/resource.d/heartbeat/Filesystem\n"), nil
			}
			return successExecResult(hosts, ""), nil
		},
	}
	ctrl := makeHaTestController(t, dep)

	agents, err := ctrl.resources.ListResourceAgents(context.Background())
	require.NoError(t, err)
	require.Len(t, agents, 2)
	assert.Equal(t, "heartbeat", agents[0].Provider)
}

func TestGetResourceAgentMetadataViaExec(t *testing.T) {
	dep := &fakeDeploymentClient{
		execFunc: func(ctx context.Context, hosts []string, cmd string, opts ...deployment.ExecOption) (*deployment.ExecResult, error) {
			if strings.Contains(cmd, "meta-data") {
				return successExecResult(hosts, sampleIPaddr2MetaData), nil
			}
			return successExecResult(hosts, ""), nil
		},
	}
	ctrl := makeHaTestController(t, dep)

	meta, err := ctrl.resources.GetResourceAgentMetadata(context.Background(), "heartbeat", "IPaddr2")
	require.NoError(t, err)
	assert.Equal(t, "heartbeat", meta.Provider)
	assert.Equal(t, "IPaddr2", meta.Name)
	require.Len(t, meta.Parameters, 2)

	// Injection-guarded names are rejected before any exec.
	_, err = ctrl.resources.GetResourceAgentMetadata(context.Background(), "heartbeat", "IPaddr2; rm -rf /")
	assert.ErrorContains(t, err, "invalid")
}
