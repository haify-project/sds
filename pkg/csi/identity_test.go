package csi

import (
	"context"
	"testing"

	"github.com/container-storage-interface/spec/lib/go/csi"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestIdentityGetPluginInfo(t *testing.T) {
	resp, err := NewIdentityServer().GetPluginInfo(context.Background(), &csi.GetPluginInfoRequest{})
	require.NoError(t, err)
	assert.Equal(t, DriverName, resp.Name)
	assert.Equal(t, DriverVersion, resp.VendorVersion)
}

func TestIdentityProbeReady(t *testing.T) {
	resp, err := NewIdentityServer().Probe(context.Background(), &csi.ProbeRequest{})
	require.NoError(t, err)
	assert.True(t, resp.GetReady().GetValue())
}

func TestIdentityGetPluginCapabilities(t *testing.T) {
	resp, err := NewIdentityServer().GetPluginCapabilities(context.Background(), &csi.GetPluginCapabilitiesRequest{})
	require.NoError(t, err)
	var types []csi.PluginCapability_Service_Type
	for _, c := range resp.GetCapabilities() {
		types = append(types, c.GetService().GetType())
	}
	assert.Contains(t, types, csi.PluginCapability_Service_CONTROLLER_SERVICE)
	assert.Contains(t, types, csi.PluginCapability_Service_VOLUME_ACCESSIBILITY_CONSTRAINTS)
}
