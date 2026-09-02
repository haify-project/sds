package gateway

import (
	"context"
	"testing"

	v1 "github.com/liliang-cn/sds/api/proto/v1"
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
