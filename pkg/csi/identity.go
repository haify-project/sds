package csi

import (
	"context"

	"github.com/container-storage-interface/spec/lib/go/csi"
	"google.golang.org/protobuf/types/known/wrapperspb"
)

// identityServer implements the CSI Identity service.
type identityServer struct {
	csi.UnimplementedIdentityServer
}

// NewIdentityServer returns the Identity service implementation.
func NewIdentityServer() csi.IdentityServer { return &identityServer{} }

func (s *identityServer) GetPluginInfo(context.Context, *csi.GetPluginInfoRequest) (*csi.GetPluginInfoResponse, error) {
	return &csi.GetPluginInfoResponse{Name: DriverName, VendorVersion: DriverVersion}, nil
}

func (s *identityServer) GetPluginCapabilities(context.Context, *csi.GetPluginCapabilitiesRequest) (*csi.GetPluginCapabilitiesResponse, error) {
	return &csi.GetPluginCapabilitiesResponse{
		Capabilities: []*csi.PluginCapability{
			{Type: &csi.PluginCapability_Service_{Service: &csi.PluginCapability_Service{
				Type: csi.PluginCapability_Service_CONTROLLER_SERVICE}}},
			{Type: &csi.PluginCapability_Service_{Service: &csi.PluginCapability_Service{
				Type: csi.PluginCapability_Service_VOLUME_ACCESSIBILITY_CONSTRAINTS}}},
		},
	}, nil
}

func (s *identityServer) Probe(context.Context, *csi.ProbeRequest) (*csi.ProbeResponse, error) {
	return &csi.ProbeResponse{Ready: &wrapperspb.BoolValue{Value: true}}, nil
}
