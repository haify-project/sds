package mcpserver

import (
	"context"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"go.uber.org/zap"

	"github.com/liliang-cn/sds/pkg/k8sapp"
)

// The Kubernetes (CSI) tools are a separate MCP server, sds-k8s, from the
// bare-metal ones: they talk to a Kubernetes API server rather than the SDS
// controller, need a kubeconfig the bare-metal server has no use for, and act
// on Kubernetes objects. Keeping them apart lets a client mount either one, and
// every tool here is named sds_k8s_* so the model can tell which side a call
// lands on.

// AppManager creates and lists databases on Kubernetes backed by SDS volumes.
// *k8sapp.Manager implements it.
type AppManager interface {
	Create(ctx context.Context, r k8sapp.Request) (*k8sapp.Created, error)
	List(ctx context.Context) ([]k8sapp.Status, error)
}

type appCreateIn struct {
	Template     string `json:"template" jsonschema:"application to run: mysql or postgres"`
	Name         string `json:"name,omitempty" jsonschema:"name of the Deployment, Service and claim (default: the template name)"`
	Namespace    string `json:"namespace,omitempty" jsonschema:"Kubernetes namespace, created if missing (default: default)"`
	Size         string `json:"size,omitempty" jsonschema:"volume size as a Kubernetes quantity, e.g. 10Gi (default 5Gi)"`
	StorageClass string `json:"storage_class,omitempty" jsonschema:"SDS StorageClass (default: one that keeps data on the database's node)"`
	Image        string `json:"image,omitempty" jsonschema:"container image override (default mysql:8.4 or postgres:17)"`
}

type appListOut struct {
	Apps      []k8sapp.Status `json:"apps"`
	Templates []string        `json:"templates" jsonschema:"applications sds_k8s_app_create can run"`
}

// NewK8s builds the sds-k8s server. AllowWrite and ReadOnly mean the same as
// on the bare-metal server.
func NewK8s(apps AppManager, logger *zap.Logger, opts Options) *Server {
	s := New(nil, logger, opts)
	s.apps, s.k8s = apps, true
	return s
}

func (s *Server) k8sMCPServer() *mcp.Server {
	srv := mcp.NewServer(&mcp.Implementation{
		Name:    "sds-k8s",
		Title:   "SDS on Kubernetes (CSI)",
		Version: s.version,
	}, nil)
	s.registerAppTools(srv)
	return srv
}

// registerAppTools adds the Kubernetes application tools.
func (s *Server) registerAppTools(srv *mcp.Server) {
	addRead(s, srv, readOnlyTool("sds_k8s_app_list", "List SDS-backed apps",
		"List databases on Kubernetes created by sds_k8s_app_create: namespace, name, template, "+
			"whether it is ready, the node it runs on, its Service address and the DRBD resource "+
			"holding its data. Also lists the templates available."),
		func(ctx context.Context, _ *mcp.CallToolRequest, _ struct{}) (*mcp.CallToolResult, appListOut, error) {
			apps, err := s.apps.List(ctx)
			if err != nil {
				return nil, appListOut{}, err
			}
			return nil, appListOut{Apps: apps, Templates: k8sapp.Templates()}, nil
		})

	addWrite(s, srv, writeTool("sds_k8s_app_create", "Create an HA database on Kubernetes",
		"Run MySQL or PostgreSQL on Kubernetes with its data on an SDS volume: DRBD keeps a "+
			"replica on two nodes and a tiebreaker on a third. One database pod; if its node fails, "+
			"Kubernetes restarts it on the other replica node after about 30 seconds plus the "+
			"database's start-up, with every committed write intact. It is storage failover, not "+
			"database replication. Creates a Secret <name>-auth (generated password), a "+
			"PersistentVolumeClaim <name>-data, a Deployment and a Service <name>. Never overwrites: "+
			"an app that already exists is an error."),
		func(ctx context.Context, _ *mcp.CallToolRequest, in appCreateIn) (*mcp.CallToolResult, opResult, error) {
			created, err := s.apps.Create(ctx, k8sapp.Request{
				Template:     in.Template,
				Name:         in.Name,
				Namespace:    in.Namespace,
				Size:         in.Size,
				StorageClass: strings.TrimSpace(in.StorageClass),
				Image:        in.Image,
			})
			if err != nil {
				return nil, opResult{}, err
			}
			return nil, ok(created.Message), nil
		})
}
