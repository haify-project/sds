package gateway

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	v1 "github.com/haify-project/sds/api/proto/v1"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

// Tests for editing an existing iSCSI gateway's target and LUNs. They assert on
// the config text that would be distributed, because the promoter config is the
// only durable record of a LUN — targetcli state is rebuilt from it on every
// promotion, so a LUN that is not in the file does not survive a failover.

func TestAddLUNUpdatesConfig(t *testing.T) {
	logger := zap.NewNop()
	mockDeployment := &MockDeploymentClient{}
	baseManager := New(nil, mockDeployment, logger, []string{"node1"})
	iscsiManager := NewISCSIManager(baseManager)

	req := &v1.CreateISCSIGatewayRequest{
		Resource:  "resource",
		Iqn:       "iqn.2024-01.com.example:sds.resource",
		ServiceIp: "192.168.1.200/24",
	}
	serviceIP, err := parseServiceIP(req.ServiceIp)
	require.NoError(t, err)
	config, err := iscsiManager.generateISCSIGatewayConfig(req, serviceIP, "/dev/drbd0", testVolumes(2))
	require.NoError(t, err)
	mockDeployment.SetConfig(gatewayConfigPath("sds-iscsi-resource"), config)

	ctx := context.Background()
	err = iscsiManager.AddLUN(ctx, "resource", 3, "/dev/drbd3")

	require.NoError(t, err)
	updated, ok := mockDeployment.GetConfig(gatewayConfigPath("sds-iscsi-resource"))
	require.True(t, ok)
	assert.Contains(t, updated, "lun=3")
	assert.Contains(t, updated, "path=/dev/drbd3")
	assertServiceIPLast(t, updated)
}

// assertServiceIPLast checks the chain order the iSCSI and NFS gateways depend
// on: the service IP is the last agent started, so a stop removes it first and
// clients never reach a target or export that is being taken apart.
func assertServiceIPLast(t *testing.T, config string) {
	t.Helper()
	last := ""
	for _, line := range strings.Split(config, "\n") {
		if strings.Contains(line, `"ocf:`) {
			last = line
		}
	}
	assert.Contains(t, last, "ocf:heartbeat:IPaddr2 ", "the service IP must be the last agent in the chain")
}

func TestISCSIConfigStartsServiceIPLast(t *testing.T) {
	iscsiManager := NewISCSIManager(New(nil, &MockDeploymentClient{}, zap.NewNop(), []string{"node1"}))
	req := &v1.CreateISCSIGatewayRequest{Resource: "r", Iqn: "iqn.2024-01.com.example:r", ServiceIp: "192.168.1.200/24"}
	serviceIP, err := parseServiceIP(req.ServiceIp)
	require.NoError(t, err)
	config, err := iscsiManager.generateISCSIGatewayConfig(req, serviceIP, "/dev/drbd0", testVolumes(3))
	require.NoError(t, err)
	assertServiceIPLast(t, config)
}

// A gateway written with the service IP ahead of the target is reordered when
// it is started from stopped.
func TestISCSIServiceIPLastScriptReordersLegacyConfig(t *testing.T) {
	legacy := `      start = [
        "ocf:heartbeat:Filesystem fs_cluster_private device=/dev/drbd15 directory=/var/lib/sds-gateway/r fstype=ext4 run_fsck=no",
        "ocf:heartbeat:IPaddr2 service_ip0 ip=10.0.0.9 cidr_netmask=24",
        "ocf:heartbeat:iSCSITarget target iqn=iqn.x:r portals=10.0.0.9:3260 implementation=lio-t",

        "ocf:heartbeat:iSCSILogicalUnit lu1 target_iqn=iqn.x:r lun=1 path=/dev/drbd14",

      ]
`
	dir := t.TempDir()
	conf := filepath.Join(dir, "sds-iscsi-r.toml.disabled")
	require.NoError(t, os.WriteFile(conf, []byte(legacy), 0644))
	script := strings.ReplaceAll(serviceIPLastScript("r"), "/etc/drbd-reactor.d", dir)
	for i := 0; i < 2; i++ { // idempotent
		out, err := exec.Command("/bin/sh", "-c", script).CombinedOutput()
		require.NoError(t, err, string(out))
	}
	got, err := os.ReadFile(conf)
	require.NoError(t, err)
	assertServiceIPLast(t, string(got))
	assert.Equal(t, 1, strings.Count(string(got), "IPaddr2"))
	assert.Equal(t, strings.Count(legacy, "\n"), strings.Count(string(got), "\n"))
}

func TestListLUNs(t *testing.T) {
	logger := zap.NewNop()
	mockDeployment := &MockDeploymentClient{}
	baseManager := New(nil, mockDeployment, logger, []string{"node1"})
	iscsiManager := NewISCSIManager(baseManager)

	req := &v1.CreateISCSIGatewayRequest{
		Resource:  "resource",
		Iqn:       "iqn.2024-01.com.example:sds.resource",
		ServiceIp: "192.168.1.200/24",
	}
	serviceIP, err := parseServiceIP(req.ServiceIp)
	require.NoError(t, err)
	config, err := iscsiManager.generateISCSIGatewayConfig(req, serviceIP, "/dev/drbd0", testVolumes(3))
	require.NoError(t, err)
	mockDeployment.SetConfig(gatewayConfigPath("sds-iscsi-resource"), config)

	luns, err := iscsiManager.ListLUNs(context.Background(), "resource")
	require.NoError(t, err)
	require.Len(t, luns, 2)
	assert.Equal(t, "1", luns[0]["lun"])
	assert.Equal(t, "/dev/drbd1", luns[0]["device"])
	assert.Equal(t, "2", luns[1]["lun"])
	assert.Equal(t, "/dev/drbd2", luns[1]["device"])
}

func TestRemoveLUNUpdatesConfig(t *testing.T) {
	logger := zap.NewNop()
	mockDeployment := &MockDeploymentClient{}
	baseManager := New(nil, mockDeployment, logger, []string{"node1"})
	iscsiManager := NewISCSIManager(baseManager)

	req := &v1.CreateISCSIGatewayRequest{
		Resource:  "resource",
		Iqn:       "iqn.2024-01.com.example:sds.resource",
		ServiceIp: "192.168.1.200/24",
	}
	serviceIP, err := parseServiceIP(req.ServiceIp)
	require.NoError(t, err)
	config, err := iscsiManager.generateISCSIGatewayConfig(req, serviceIP, "/dev/drbd0", testVolumes(3))
	require.NoError(t, err)
	mockDeployment.SetConfig(gatewayConfigPath("sds-iscsi-resource"), config)

	ctx := context.Background()
	err = iscsiManager.RemoveLUN(ctx, "resource", 2)

	require.NoError(t, err)
	updated, ok := mockDeployment.GetConfig(gatewayConfigPath("sds-iscsi-resource"))
	require.True(t, ok)
	assert.NotContains(t, updated, "lun=2")
}

func TestCreateTargetManagedByOCF(t *testing.T) {
	logger := zap.NewNop()
	baseManager := New(nil, nil, logger, nil)
	iscsiManager := NewISCSIManager(baseManager)

	ctx := context.Background()
	err := iscsiManager.CreateTarget(ctx, "resource")

	assert.Error(t, err)
	assert.Contains(t, err.Error(), "OCF resource agent")
}
