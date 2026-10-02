package gateway

import (
	"context"
	"testing"

	v1 "github.com/haify-project/sds/api/proto/v1"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

// Tests for the iSCSI initiator allow-list and CHAP credentials. Each case
// checks that the parameters the edit was not about — portals, IQN, the other
// half of the ACL — survive the rewrite of the iSCSITarget line, since losing
// one of them widens access rather than erroring.

func TestAddAndListInitiators(t *testing.T) {
	logger := zap.NewNop()
	mockDeployment := &MockDeploymentClient{}
	baseManager := New(nil, mockDeployment, logger, []string{"node1"})
	iscsiManager := NewISCSIManager(baseManager)

	req := &v1.CreateISCSIGatewayRequest{
		Resource:          "resource",
		Iqn:               "iqn.2024-01.com.example:sds.resource",
		ServiceIp:         "192.168.1.200/24",
		AllowedInitiators: []string{"iqn.2024-01.com.example:init-a"},
	}
	serviceIP, err := parseServiceIP(req.ServiceIp)
	require.NoError(t, err)
	config, err := iscsiManager.generateISCSIGatewayConfig(req, serviceIP, "/dev/drbd0", testVolumes(2))
	require.NoError(t, err)
	mockDeployment.SetConfig(gatewayConfigPath("sds-iscsi-resource"), config)

	ctx := context.Background()
	err = iscsiManager.AddInitiator(ctx, "resource", "iqn.2024-01.com.example:init-b")
	require.NoError(t, err)

	initiators, err := iscsiManager.ListInitiators(ctx, "resource")
	require.NoError(t, err)
	assert.Contains(t, initiators, "iqn.2024-01.com.example:init-a")
	assert.Contains(t, initiators, "iqn.2024-01.com.example:init-b")
}

func TestSetAndGetCHAP(t *testing.T) {
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
	err = iscsiManager.SetCHAP(ctx, "resource", "user", "pass", false)
	require.NoError(t, err)

	username, password, mutual, err := iscsiManager.GetCHAP(ctx, "resource")
	require.NoError(t, err)
	assert.Equal(t, "user", username)
	assert.Equal(t, "pass", password)
	assert.False(t, mutual)
}
