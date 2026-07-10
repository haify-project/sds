package csi

import (
	"context"
	"fmt"
	"net"
	"net/url"
	"os"

	"github.com/container-storage-interface/spec/lib/go/csi"
	sdspb "github.com/liliang-cn/sds/api/proto/v1"
	"go.uber.org/zap"
	"google.golang.org/grpc"
)

const (
	// DriverName is the CSI driver name (reverse-DNS, used in CSIDriver object).
	DriverName = "sds.csi.liliang-cn.com"
	// DriverVersion is reported via Identity.GetPluginInfo.
	DriverVersion = "0.1.0"
	// TopologyKeyNode segments a volume to the node(s) holding a replica.
	TopologyKeyNode = DriverName + "/node"
)

// SDSBackend is the subset of the sds-controller gRPC client the CSI driver
// uses. *client.SDSClient satisfies it directly; tests use a fake.
type SDSBackend interface {
	CreateResourceWithPoolAndType(ctx context.Context, name string, port uint32, nodes []string, protocol string, sizeGB uint32, pool, storageType string, drbdOptions map[string]string) error
	GetResource(ctx context.Context, name string) (*sdspb.ResourceInfo, error)
	DeleteResource(ctx context.Context, name string) error
	ListNodes(ctx context.Context) ([]*sdspb.NodeInfo, error)
	RegisterNode(ctx context.Context, name, address string) (*sdspb.NodeInfo, error)
	SetPrimary(ctx context.Context, resource, node string, force bool) error
	// PromoteForNode performs a quorum-guarded promote for hard-failover: the
	// controller tries a normal promote and only force-promotes if this node
	// holds DRBD quorum, refusing otherwise to avoid split-brain.
	PromoteForNode(ctx context.Context, resource, node string) error
	SetSecondary(ctx context.Context, resource, node string) error
	// AttachDisklessClient adds node to resource as a diskless data client so a
	// Pod on a node with no local replica can still mount the volume (I/O over
	// the DRBD network). Idempotent.
	AttachDisklessClient(ctx context.Context, resource, node string) error
	// DetachDisklessClient removes a diskless client added via
	// AttachDisklessClient. Idempotent.
	DetachDisklessClient(ctx context.Context, resource, node string) error
}

// Driver wires the CSI services onto a gRPC server over a unix socket.
type Driver struct {
	endpoint string
	srv      *grpc.Server
	log      *zap.Logger

	identity   csi.IdentityServer
	controller csi.ControllerServer
	node       csi.NodeServer
}

// NewDriver builds a Driver. Pass nil for services that this binary doesn't
// serve (the controller binary leaves node nil and vice-versa).
func NewDriver(endpoint string, log *zap.Logger, id csi.IdentityServer, ctrl csi.ControllerServer, node csi.NodeServer) *Driver {
	if log == nil {
		log = zap.NewNop()
	}
	return &Driver{endpoint: endpoint, log: log, identity: id, controller: ctrl, node: node}
}

// Run listens on the unix socket and serves until the context is cancelled.
func (d *Driver) Run(ctx context.Context) error {
	proto, addr, err := parseEndpoint(d.endpoint)
	if err != nil {
		return err
	}
	if proto == "unix" {
		_ = os.Remove(addr) // clear a stale socket
	}
	lis, err := net.Listen(proto, addr)
	if err != nil {
		return fmt.Errorf("listen %s: %w", d.endpoint, err)
	}
	d.srv = grpc.NewServer()
	if d.identity != nil {
		csi.RegisterIdentityServer(d.srv, d.identity)
	}
	if d.controller != nil {
		csi.RegisterControllerServer(d.srv, d.controller)
	}
	if d.node != nil {
		csi.RegisterNodeServer(d.srv, d.node)
	}
	stopped := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			d.srv.GracefulStop()
		case <-stopped:
		}
	}()
	d.log.Info("CSI driver serving", zap.String("endpoint", d.endpoint))
	err = d.srv.Serve(lis)
	close(stopped)
	if proto == "unix" {
		_ = os.Remove(addr)
	}
	return err
}

// parseEndpoint splits "unix:///path" or "tcp://host:port" into proto + addr.
func parseEndpoint(ep string) (string, string, error) {
	u, err := url.Parse(ep)
	if err != nil {
		return "", "", fmt.Errorf("invalid endpoint %q: %w", ep, err)
	}
	switch u.Scheme {
	case "unix":
		if u.Host != "" {
			return "", "", fmt.Errorf("invalid unix endpoint %q (host must be empty)", ep)
		}
		return "unix", u.Path, nil
	case "tcp":
		return "tcp", u.Host, nil
	default:
		return "", "", fmt.Errorf("invalid endpoint %q (want unix:// or tcp://)", ep)
	}
}
