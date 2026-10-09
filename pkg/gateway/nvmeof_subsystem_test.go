package gateway

import (
	"context"
	"testing"

	v1 "github.com/haify-project/haify/api/proto/v1"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

// Tests for the namespaces and transport port an existing NVMe-oF subsystem
// exports. Namespace IDs are checked for allocation above the current maximum:
// reusing a removed ID would rebind a client's open namespace to different
// backing storage under the same identifier.

func TestAddNamespaceUpdatesConfig(t *testing.T) {
	logger := zap.NewNop()
	mockDeployment := &MockDeploymentClient{}
	baseManager := New(nil, mockDeployment, logger, []string{"node1"})
	nvmeManager := NewNVMeManager(baseManager)

	req := &v1.CreateNVMeGatewayRequest{
		Resource:  "resource",
		Nqn:       "nqn.2024-01.com.example:haify.resource",
		ServiceIp: "192.168.1.150/24",
	}
	serviceIP, err := parseServiceIP(req.ServiceIp)
	require.NoError(t, err)
	config, err := nvmeManager.generateNVMeGatewayConfig(req, serviceIP, "/dev/drbd0", testVolumes(2))
	require.NoError(t, err)
	mockDeployment.SetConfig(gatewayConfigPath("haify-nvmeof-resource"), config)

	ctx := context.Background()
	err = nvmeManager.AddNamespace(ctx, "resource", "/dev/drbd3")

	require.NoError(t, err)
	updated, ok := mockDeployment.GetConfig(gatewayConfigPath("haify-nvmeof-resource"))
	require.True(t, ok)
	assert.Contains(t, updated, "namespace_id=2")
	assert.Contains(t, updated, "backing_path=/dev/drbd3")
}

func TestRemoveNamespaceUpdatesConfig(t *testing.T) {
	logger := zap.NewNop()
	mockDeployment := &MockDeploymentClient{}
	baseManager := New(nil, mockDeployment, logger, []string{"node1"})
	nvmeManager := NewNVMeManager(baseManager)

	req := &v1.CreateNVMeGatewayRequest{
		Resource:  "resource",
		Nqn:       "nqn.2024-01.com.example:haify.resource",
		ServiceIp: "192.168.1.150/24",
	}
	serviceIP, err := parseServiceIP(req.ServiceIp)
	require.NoError(t, err)
	config, err := nvmeManager.generateNVMeGatewayConfig(req, serviceIP, "/dev/drbd0", testVolumes(3))
	require.NoError(t, err)
	mockDeployment.SetConfig(gatewayConfigPath("haify-nvmeof-resource"), config)

	ctx := context.Background()
	err = nvmeManager.RemoveNamespace(ctx, "resource", 2)

	require.NoError(t, err)
	updated, ok := mockDeployment.GetConfig(gatewayConfigPath("haify-nvmeof-resource"))
	require.True(t, ok)
	assert.NotContains(t, updated, "namespace_id=2")
}

func TestCreateSubsystemManagedByOCF(t *testing.T) {
	logger := zap.NewNop()
	baseManager := New(nil, nil, logger, nil)
	nvmeManager := NewNVMeManager(baseManager)

	ctx := context.Background()
	err := nvmeManager.CreateSubsystem(ctx, "resource", "nqn.2024-01.com.example:haify.test")

	assert.Error(t, err)
	assert.Contains(t, err.Error(), "OCF resource agent")
}

func TestDeleteAndListNVMePort(t *testing.T) {
	logger := zap.NewNop()
	mockDeployment := &MockDeploymentClient{}
	baseManager := New(nil, mockDeployment, logger, []string{"node1"})
	nvmeManager := NewNVMeManager(baseManager)

	req := &v1.CreateNVMeGatewayRequest{
		Resource:  "resource",
		Nqn:       "nqn.2024-01.com.example:haify.resource",
		ServiceIp: "192.168.1.150/24",
	}
	serviceIP, err := parseServiceIP(req.ServiceIp)
	require.NoError(t, err)
	config, err := nvmeManager.generateNVMeGatewayConfig(req, serviceIP, "/dev/drbd0", testVolumes(2))
	require.NoError(t, err)
	mockDeployment.SetConfig(gatewayConfigPath("haify-nvmeof-resource"), config)

	ctx := context.Background()
	ports, err := nvmeManager.ListPorts(ctx, "resource")
	require.NoError(t, err)
	require.Len(t, ports, 1)
	assert.Equal(t, "192.168.1.150", ports[0]["addr"])
	assert.Equal(t, "4420", ports[0]["port"])

	err = nvmeManager.DeletePort(ctx, "resource", "192.168.1.150", 4420)
	require.NoError(t, err)

	updated, ok := mockDeployment.GetConfig(gatewayConfigPath("haify-nvmeof-resource"))
	require.True(t, ok)
	assert.NotContains(t, updated, "ocf:heartbeat:nvmet-port")
}
