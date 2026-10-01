package client

import (
	"context"
	"fmt"

	sdspb "github.com/liliang-cn/sds/api/proto/v1"
)

// CreateNFSGateway creates an NFS gateway
func (c *SDSClient) CreateNFSGateway(ctx context.Context, req *sdspb.CreateNFSGatewayRequest) (*sdspb.CreateNFSGatewayResponse, error) {
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
func (c *SDSClient) CreateISCSIGateway(ctx context.Context, req *sdspb.CreateISCSIGatewayRequest) (*sdspb.CreateISCSIGatewayResponse, error) {
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
func (c *SDSClient) CreateNVMeGateway(ctx context.Context, req *sdspb.CreateNVMeGatewayRequest) (*sdspb.CreateNVMeGatewayResponse, error) {
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
func (c *SDSClient) ListGateways(ctx context.Context) ([]*sdspb.GatewayInfo, error) {
	req := &sdspb.ListGatewaysRequest{}

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
func (c *SDSClient) GetGateway(ctx context.Context, id string) (*sdspb.GatewayInfo, error) {
	req := &sdspb.GetGatewayRequest{Id: id}

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
func (c *SDSClient) StartGateway(ctx context.Context, id string) error {
	req := &sdspb.StartGatewayRequest{
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
func (c *SDSClient) StopGateway(ctx context.Context, id string) error {
	req := &sdspb.StopGatewayRequest{
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
func (c *SDSClient) DeleteGateway(ctx context.Context, id string) error {
	req := &sdspb.DeleteGatewayRequest{
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
func (c *SDSClient) AddNFSExport(ctx context.Context, resource, exportPath string, fsid int32, clientSpec, options string) error {
	req := &sdspb.AddNFSExportRequest{
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
func (c *SDSClient) RemoveNFSExport(ctx context.Context, resource, exportPath string) error {
	req := &sdspb.RemoveNFSExportRequest{
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
func (c *SDSClient) ListNFSExports(ctx context.Context, resource string) ([]*sdspb.NFSExportInfo, error) {
	req := &sdspb.ListNFSExportsRequest{Resource: resource}

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
func (c *SDSClient) AddISCSILUN(ctx context.Context, resource string, lun int32, device string) error {
	req := &sdspb.AddISCSILUNRequest{Resource: resource, Lun: lun, Device: device}

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
func (c *SDSClient) RemoveISCSILUN(ctx context.Context, resource string, lun int32) error {
	req := &sdspb.RemoveISCSILUNRequest{Resource: resource, Lun: lun}

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
func (c *SDSClient) ListISCSILUNs(ctx context.Context, resource string) ([]*sdspb.ISCSILUNInfo, error) {
	req := &sdspb.ListISCSILUNsRequest{Resource: resource}

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
func (c *SDSClient) AddISCSIInitiator(ctx context.Context, resource, initiator string) error {
	req := &sdspb.AddISCSIInitiatorRequest{Resource: resource, Initiator: initiator}

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
func (c *SDSClient) RemoveISCSIInitiator(ctx context.Context, resource, initiator string) error {
	req := &sdspb.RemoveISCSIInitiatorRequest{Resource: resource, Initiator: initiator}

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
func (c *SDSClient) ListISCSIInitiators(ctx context.Context, resource string) ([]string, error) {
	req := &sdspb.ListISCSIInitiatorsRequest{Resource: resource}

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
func (c *SDSClient) SetISCSIChap(ctx context.Context, resource, username, password string, mutual bool) error {
	req := &sdspb.SetISCSIChapRequest{
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
func (c *SDSClient) GetISCSIChap(ctx context.Context, resource string) (*sdspb.GetISCSIChapResponse, error) {
	req := &sdspb.GetISCSIChapRequest{Resource: resource}

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
func (c *SDSClient) AddNVMeNamespace(ctx context.Context, resource, device string) error {
	req := &sdspb.AddNVMeNamespaceRequest{Resource: resource, Device: device}

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
func (c *SDSClient) RemoveNVMeNamespace(ctx context.Context, resource string, namespaceID int32) error {
	req := &sdspb.RemoveNVMeNamespaceRequest{Resource: resource, NamespaceId: namespaceID}

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
func (c *SDSClient) ListNVMeNamespaces(ctx context.Context, resource string) ([]*sdspb.NVMeNamespaceInfo, error) {
	req := &sdspb.ListNVMeNamespacesRequest{Resource: resource}

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
func (c *SDSClient) AddNVMeHost(ctx context.Context, resource, hostNQN string) error {
	req := &sdspb.AddNVMeHostRequest{Resource: resource, HostNqn: hostNQN}

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
func (c *SDSClient) RemoveNVMeHost(ctx context.Context, resource, hostNQN string) error {
	req := &sdspb.RemoveNVMeHostRequest{Resource: resource, HostNqn: hostNQN}

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
func (c *SDSClient) ListNVMeHosts(ctx context.Context, resource string) ([]string, error) {
	req := &sdspb.ListNVMeHostsRequest{Resource: resource}

	resp, err := c.client.ListNVMeHosts(ctx, req)
	if err != nil {
		return nil, err
	}
	if !resp.Success {
		return nil, fmt.Errorf("%s", resp.Message)
	}
	return resp.Hosts, nil
}
