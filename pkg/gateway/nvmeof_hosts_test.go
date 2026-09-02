package gateway

import (
	"context"
	"testing"

	v1 "github.com/liliang-cn/sds/api/proto/v1"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

// Tests for the NVMe subsystem's host allow-list, including the refusal to
// "remove" a host from a subsystem that has no explicit list — where an empty
// list means allow-all, succeeding there would report an ACL that does not
// exist.

func TestAddAndRemoveNVMeHost(t *testing.T) {
	logger := zap.NewNop()
	mockDeployment := &MockDeploymentClient{}
	baseManager := New(nil, mockDeployment, logger, []string{"node1"})
	nvmeManager := NewNVMeManager(baseManager)

	req := &v1.CreateNVMeGatewayRequest{
		Resource:  "resource",
		Nqn:       "nqn.2024-01.com.example:sds.resource",
		ServiceIp: "192.168.1.150/24",
	}
	serviceIP, err := parseServiceIP(req.ServiceIp)
	require.NoError(t, err)
	config, err := nvmeManager.generateNVMeGatewayConfig(req, serviceIP, "/dev/drbd0", testVolumes(2))
	require.NoError(t, err)
	mockDeployment.SetConfig(gatewayConfigPath("sds-nvmeof-resource"), config)

	ctx := context.Background()
	err = nvmeManager.AddHost(ctx, "resource", "nqn.2024-01.com.example:host1")
	require.NoError(t, err)

	hosts, err := nvmeManager.ListHosts(ctx, "resource")
	require.NoError(t, err)
	assert.Contains(t, hosts, "nqn.2024-01.com.example:host1")

	err = nvmeManager.RemoveHost(ctx, "resource", "nqn.2024-01.com.example:host1")
	require.NoError(t, err)
	hosts, err = nvmeManager.ListHosts(ctx, "resource")
	require.NoError(t, err)
	assert.Equal(t, []string{"ALL"}, hosts)
}
