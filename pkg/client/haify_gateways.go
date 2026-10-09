package client

import (
	"context"
	"fmt"

	haifypb "github.com/haify-project/haify/api/proto/v1"
)

// CreateNFSGateway creates an NFS gateway
func (c *HaifyClient) CreateNFSGateway(ctx context.Context, req *haifypb.CreateNFSGatewayRequest) (*haifypb.CreateNFSGatewayResponse, error) {
	resp, err := c.client.CreateNFSGateway(ctx, req)
	if err != nil {
		return nil, err
	}

	if !resp.Success {
		return resp, fmt.Errorf("%s", resp.Message)
	}

	return resp, nil
}

// CreateISCSIGateway creates an iSCSI gateway
func (c *HaifyClient) CreateISCSIGateway(ctx context.Context, req *haifypb.CreateISCSIGatewayRequest) (*haifypb.CreateISCSIGatewayResponse, error) {
	resp, err := c.client.CreateISCSIGateway(ctx, req)
	if err != nil {
		return nil, err
	}

	if !resp.Success {
		return resp, fmt.Errorf("%s", resp.Message)
	}

	return resp, nil
}

// CreateNVMeGateway creates an NVMe gateway
func (c *HaifyClient) CreateNVMeGateway(ctx context.Context, req *haifypb.CreateNVMeGatewayRequest) (*haifypb.CreateNVMeGatewayResponse, error) {
	resp, err := c.client.CreateNVMeGateway(ctx, req)
	if err != nil {
		return nil, err
	}

	if !resp.Success {
		return resp, fmt.Errorf("%s", resp.Message)
	}

	return resp, nil
}

// ListGateways lists all gateways
func (c *HaifyClient) ListGateways(ctx context.Context) ([]*haifypb.GatewayInfo, error) {
	req := &haifypb.ListGatewaysRequest{}

	resp, err := c.client.ListGateways(ctx, req)
	if err != nil {
		return nil, err
	}

	if !resp.Success {
		return nil, fmt.Errorf("%s", resp.Message)
	}

	return resp.Gateways, nil
}

// GetGateway gets a gateway by resource identifier.
func (c *HaifyClient) GetGateway(ctx context.Context, id string) (*haifypb.GatewayInfo, error) {
	req := &haifypb.GetGatewayRequest{Id: id}

	resp, err := c.client.GetGateway(ctx, req)
	if err != nil {
		return nil, err
	}
	if !resp.Success {
		return nil, fmt.Errorf("%s", resp.Message)
	}

	return resp.Gateway, nil
}

// StartGateway starts a gateway
func (c *HaifyClient) StartGateway(ctx context.Context, id string) error {
	req := &haifypb.StartGatewayRequest{
		Id: id,
	}

	resp, err := c.client.StartGateway(ctx, req)
	if err != nil {
		return err
	}

	if !resp.Success {
		return fmt.Errorf("%s", resp.Message)
	}

	return nil
}

// StopGateway stops a gateway
func (c *HaifyClient) StopGateway(ctx context.Context, id string) error {
	req := &haifypb.StopGatewayRequest{
		Id: id,
	}

	resp, err := c.client.StopGateway(ctx, req)
	if err != nil {
		return err
	}

	if !resp.Success {
		return fmt.Errorf("%s", resp.Message)
	}

	return nil
}

// DeleteGateway deletes a gateway
func (c *HaifyClient) DeleteGateway(ctx context.Context, id string) error {
	req := &haifypb.DeleteGatewayRequest{
		Id: id,
	}

	resp, err := c.client.DeleteGateway(ctx, req)
	if err != nil {
		return err
	}

	if !resp.Success {
		return fmt.Errorf("%s", resp.Message)
	}

	return nil
}

// AddNFSExport adds an export to an NFS gateway.
func (c *HaifyClient) AddNFSExport(ctx context.Context, resource, exportPath string, fsid int32, clientSpec, options string) error {
	req := &haifypb.AddNFSExportRequest{
		Resource:   resource,
		ExportPath: exportPath,
		Fsid:       fsid,
		ClientSpec: clientSpec,
		Options:    options,
	}

	resp, err := c.client.AddNFSExport(ctx, req)
	if err != nil {
		return err
	}
	if !resp.Success {
		return fmt.Errorf("%s", resp.Message)
	}
	return nil
}

// RemoveNFSExport removes an export from an NFS gateway.
func (c *HaifyClient) RemoveNFSExport(ctx context.Context, resource, exportPath string) error {
	req := &haifypb.RemoveNFSExportRequest{
		Resource:   resource,
		ExportPath: exportPath,
	}

	resp, err := c.client.RemoveNFSExport(ctx, req)
	if err != nil {
		return err
	}
	if !resp.Success {
		return fmt.Errorf("%s", resp.Message)
	}
	return nil
}

// ListNFSExports lists exports for an NFS gateway.
func (c *HaifyClient) ListNFSExports(ctx context.Context, resource string) ([]*haifypb.NFSExportInfo, error) {
	req := &haifypb.ListNFSExportsRequest{Resource: resource}

	resp, err := c.client.ListNFSExports(ctx, req)
	if err != nil {
		return nil, err
	}
	if !resp.Success {
		return nil, fmt.Errorf("%s", resp.Message)
	}
	return resp.Exports, nil
}

// AddISCSILUN adds a LUN to an iSCSI gateway.
func (c *HaifyClient) AddISCSILUN(ctx context.Context, resource string, lun int32, device string) error {
	req := &haifypb.AddISCSILUNRequest{Resource: resource, Lun: lun, Device: device}

	resp, err := c.client.AddISCSILUN(ctx, req)
	if err != nil {
		return err
	}
	if !resp.Success {
		return fmt.Errorf("%s", resp.Message)
	}
	return nil
}

// RemoveISCSILUN removes a LUN from an iSCSI gateway.
func (c *HaifyClient) RemoveISCSILUN(ctx context.Context, resource string, lun int32) error {
	req := &haifypb.RemoveISCSILUNRequest{Resource: resource, Lun: lun}

	resp, err := c.client.RemoveISCSILUN(ctx, req)
	if err != nil {
		return err
	}
	if !resp.Success {
		return fmt.Errorf("%s", resp.Message)
	}
	return nil
}

// ListISCSILUNs lists LUNs for an iSCSI gateway.
func (c *HaifyClient) ListISCSILUNs(ctx context.Context, resource string) ([]*haifypb.ISCSILUNInfo, error) {
	req := &haifypb.ListISCSILUNsRequest{Resource: resource}

	resp, err := c.client.ListISCSILUNs(ctx, req)
	if err != nil {
		return nil, err
	}
	if !resp.Success {
		return nil, fmt.Errorf("%s", resp.Message)
	}
	return resp.Luns, nil
}

// AddISCSIInitiator adds an initiator ACL to an iSCSI gateway.
func (c *HaifyClient) AddISCSIInitiator(ctx context.Context, resource, initiator string) error {
	req := &haifypb.AddISCSIInitiatorRequest{Resource: resource, Initiator: initiator}

	resp, err := c.client.AddISCSIInitiator(ctx, req)
	if err != nil {
		return err
	}
	if !resp.Success {
		return fmt.Errorf("%s", resp.Message)
	}
	return nil
}

// RemoveISCSIInitiator removes an initiator ACL from an iSCSI gateway.
func (c *HaifyClient) RemoveISCSIInitiator(ctx context.Context, resource, initiator string) error {
	req := &haifypb.RemoveISCSIInitiatorRequest{Resource: resource, Initiator: initiator}

	resp, err := c.client.RemoveISCSIInitiator(ctx, req)
	if err != nil {
		return err
	}
	if !resp.Success {
		return fmt.Errorf("%s", resp.Message)
	}
	return nil
}

// ListISCSIInitiators lists initiator ACLs for an iSCSI gateway.
func (c *HaifyClient) ListISCSIInitiators(ctx context.Context, resource string) ([]string, error) {
	req := &haifypb.ListISCSIInitiatorsRequest{Resource: resource}

	resp, err := c.client.ListISCSIInitiators(ctx, req)
	if err != nil {
		return nil, err
	}
	if !resp.Success {
		return nil, fmt.Errorf("%s", resp.Message)
	}
	return resp.Initiators, nil
}

// SetISCSIChap updates CHAP settings on an iSCSI gateway.
func (c *HaifyClient) SetISCSIChap(ctx context.Context, resource, username, password string, mutual bool) error {
	req := &haifypb.SetISCSIChapRequest{
		Resource: resource,
		Username: username,
		Password: password,
		Mutual:   mutual,
	}

	resp, err := c.client.SetISCSIChap(ctx, req)
	if err != nil {
		return err
	}
	if !resp.Success {
		return fmt.Errorf("%s", resp.Message)
	}
	return nil
}

// GetISCSIChap returns CHAP settings for an iSCSI gateway.
func (c *HaifyClient) GetISCSIChap(ctx context.Context, resource string) (*haifypb.GetISCSIChapResponse, error) {
	req := &haifypb.GetISCSIChapRequest{Resource: resource}

	resp, err := c.client.GetISCSIChap(ctx, req)
	if err != nil {
		return nil, err
	}
	if !resp.Success {
		return nil, fmt.Errorf("%s", resp.Message)
	}
	return resp, nil
}

// AddNVMeNamespace adds a namespace to an NVMe gateway.
func (c *HaifyClient) AddNVMeNamespace(ctx context.Context, resource, device string) error {
	req := &haifypb.AddNVMeNamespaceRequest{Resource: resource, Device: device}

	resp, err := c.client.AddNVMeNamespace(ctx, req)
	if err != nil {
		return err
	}
	if !resp.Success {
		return fmt.Errorf("%s", resp.Message)
	}
	return nil
}

// RemoveNVMeNamespace removes a namespace from an NVMe gateway.
func (c *HaifyClient) RemoveNVMeNamespace(ctx context.Context, resource string, namespaceID int32) error {
	req := &haifypb.RemoveNVMeNamespaceRequest{Resource: resource, NamespaceId: namespaceID}

	resp, err := c.client.RemoveNVMeNamespace(ctx, req)
	if err != nil {
		return err
	}
	if !resp.Success {
		return fmt.Errorf("%s", resp.Message)
	}
	return nil
}

// ListNVMeNamespaces lists namespaces for an NVMe gateway.
func (c *HaifyClient) ListNVMeNamespaces(ctx context.Context, resource string) ([]*haifypb.NVMeNamespaceInfo, error) {
	req := &haifypb.ListNVMeNamespacesRequest{Resource: resource}

	resp, err := c.client.ListNVMeNamespaces(ctx, req)
	if err != nil {
		return nil, err
	}
	if !resp.Success {
		return nil, fmt.Errorf("%s", resp.Message)
	}
	return resp.Namespaces, nil
}

// AddNVMeHost adds a host allow-list entry to an NVMe gateway.
func (c *HaifyClient) AddNVMeHost(ctx context.Context, resource, hostNQN string) error {
	req := &haifypb.AddNVMeHostRequest{Resource: resource, HostNqn: hostNQN}

	resp, err := c.client.AddNVMeHost(ctx, req)
	if err != nil {
		return err
	}
	if !resp.Success {
		return fmt.Errorf("%s", resp.Message)
	}
	return nil
}

// RemoveNVMeHost removes a host allow-list entry from an NVMe gateway.
func (c *HaifyClient) RemoveNVMeHost(ctx context.Context, resource, hostNQN string) error {
	req := &haifypb.RemoveNVMeHostRequest{Resource: resource, HostNqn: hostNQN}

	resp, err := c.client.RemoveNVMeHost(ctx, req)
	if err != nil {
		return err
	}
	if !resp.Success {
		return fmt.Errorf("%s", resp.Message)
	}
	return nil
}

// ListNVMeHosts lists hosts configured for an NVMe gateway.
func (c *HaifyClient) ListNVMeHosts(ctx context.Context, resource string) ([]string, error) {
	req := &haifypb.ListNVMeHostsRequest{Resource: resource}

	resp, err := c.client.ListNVMeHosts(ctx, req)
	if err != nil {
		return nil, err
	}
	if !resp.Success {
		return nil, fmt.Errorf("%s", resp.Message)
	}
	return resp.Hosts, nil
}
