package mcpserver

import (
	"context"
	"fmt"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	sdspb "github.com/haify-project/sds/api/proto/v1"
	"github.com/haify-project/sds/pkg/client"
)

// Database applications on bare metal (`sds app`): one database instance on a
// resource's DRBD volume, failed over by drbd-reactor. The Kubernetes
// equivalent is the sds_k8s_app_* set on the sds-k8s server (tools_apps.go).
//
// sds_app_create never returns the generated password. Whatever a tool
// returns lands in the conversation that called it, and from there in logs
// and transcripts nobody audits as credential stores. The tool says where the
// password is kept instead: root-only, on the volume, on the node running
// the database. `sds app create` at the CLI prints it once.

type dbAppCreateIn struct {
	Name      string `json:"name" jsonschema:"app name: 1-40 lower-case letters, digits and inner hyphens"`
	Engine    string `json:"engine" jsonschema:"database engine: postgres, mysql (MySQL or MariaDB, whichever the nodes have) or redis"`
	Resource  string `json:"resource,omitempty" jsonschema:"existing resource with at least two diskful replicas (default: the app name)"`
	ServiceIP string `json:"service_ip" jsonschema:"service IP clients connect to, IPv4 in CIDR notation, e.g. 192.168.1.60/24"`
	Port      uint32 `json:"port,omitempty" jsonschema:"TCP port (default 5432, 3306 or 6379)"`
	Vector    bool   `json:"vector,omitempty" jsonschema:"postgres only: install the pgvector extension"`
}

type dbAppNameIn struct {
	Name string `json:"name" jsonschema:"app name"`
}

type dbAppDeleteIn struct {
	Name       string `json:"name" jsonschema:"app name"`
	DeleteData bool   `json:"delete_data,omitempty" jsonschema:"also delete the resource and every byte of its data"`
}

type dbAppSnapshotIn struct {
	Name     string `json:"name" jsonschema:"app name"`
	Snapshot string `json:"snapshot" jsonschema:"snapshot name: letters, digits, _ and -, at most 40"`
}

type dbAppOut struct {
	Name            string `json:"name"`
	Engine          string `json:"engine"`
	Resource        string `json:"resource"`
	ServiceIP       string `json:"service_ip"`
	Port            uint32 `json:"port"`
	Vector          bool   `json:"vector,omitempty"`
	Version         string `json:"version,omitempty"`
	AdminUser       string `json:"admin_user" jsonschema:"database account whose password was generated"`
	CredentialsFile string `json:"credentials_file" jsonschema:"where the password is kept: root-only, on the volume, on the node running the app"`
	Connection      string `json:"connection" jsonschema:"how a client reaches it through the service IP"`
}

type dbAppListOut struct {
	Apps []dbAppOut `json:"apps"`
}

type dbAppStatusOut struct {
	dbAppOut
	State        string   `json:"state" jsonschema:"running, degraded (Primary somewhere but the database is not active or does not answer) or stopped"`
	PrimaryNode  string   `json:"primary_node,omitempty" jsonschema:"node the app runs on"`
	ServiceState string   `json:"service_state,omitempty" jsonschema:"systemctl is-active of the app's unit there"`
	Healthy      bool     `json:"healthy" jsonschema:"the engine's health probe answered"`
	Nodes        []string `json:"nodes" jsonschema:"replicas the app can fail over to"`
}

func dbAppFrom(a *sdspb.AppInfo) dbAppOut {
	return dbAppOut{Name: a.GetName(), Engine: a.GetEngine(), Resource: a.GetResource(), ServiceIP: a.GetServiceIp(),
		Port: a.GetPort(), Vector: a.GetVector(), Version: a.GetVersion(), AdminUser: a.GetAdminUser(),
		CredentialsFile: a.GetCredentialsFile(), Connection: a.GetConnection()}
}

// registerDBAppTools adds the database application tools.
func (s *Server) registerDBAppTools(srv *mcp.Server) {
	addRead(s, srv, readOnlyTool("sds_app_list", "List database apps",
		"List the database applications (sds app): single-instance PostgreSQL, MySQL/MariaDB or Redis "+
			"on a resource's DRBD volume, failed over by drbd-reactor. Shows engine, resource, service IP and port."),
		func(ctx context.Context, _ *mcp.CallToolRequest, _ struct{}) (*mcp.CallToolResult, dbAppListOut, error) {
			apps, err := s.client.ListApps(ctx)
			if err != nil {
				return nil, dbAppListOut{}, err
			}
			out := dbAppListOut{Apps: make([]dbAppOut, 0, len(apps))}
			for _, a := range apps {
				out.Apps = append(out.Apps, dbAppFrom(a))
			}
			return nil, out, nil
		})

	addRead(s, srv, readOnlyTool("sds_app_status", "Database app status",
		"Where a database app runs (the node its resource is Primary on), whether its unit is active "+
			"there and whether the database answers its health probe (pg_isready, mysqladmin ping, redis PING)."),
		func(ctx context.Context, _ *mcp.CallToolRequest, in dbAppNameIn) (*mcp.CallToolResult, dbAppStatusOut, error) {
			st, err := s.client.GetAppStatus(ctx, in.Name)
			if err != nil {
				return nil, dbAppStatusOut{}, err
			}
			return nil, dbAppStatusOut{dbAppOut: dbAppFrom(st.App), State: st.State, PrimaryNode: st.PrimaryNode,
				ServiceState: st.ServiceState, Healthy: st.Healthy, Nodes: st.Nodes}, nil
		})

	addWrite(s, srv, writeTool("sds_app_create", "Create a database app",
		"Run PostgreSQL (optionally with pgvector), MySQL/MariaDB or Redis on an existing resource with at "+
			"least two diskful replicas. Checks every replica first (engine installed in the same version and "+
			"place, same daemon uid/gid, OCF agents, port free) and refuses with the reason; then formats the "+
			"volume if blank, initializes the database once, and hands it to drbd-reactor: mount, database, "+
			"service IP last. Failover keeps every acknowledged write (DRBD protocol C); clients reconnect to "+
			"the service IP. The generated password is NOT returned here: it is kept root-only on the volume "+
			"(credentials_file), and the CLI's `sds app create` prints it once."),
		func(ctx context.Context, _ *mcp.CallToolRequest, in dbAppCreateIn) (*mcp.CallToolResult, opResult, error) {
			resp, err := s.client.CreateApp(ctx, client.AppCreateRequest{Name: in.Name, Engine: in.Engine,
				Resource: in.Resource, ServiceIP: in.ServiceIP, Port: in.Port, Vector: in.Vector})
			if err != nil {
				return nil, opResult{}, err
			}
			a := resp.App
			detail := fmt.Sprintf("app %s (%s) created on resource %s; connect with %s as %s; the password is "+
				"in %s (root, on the node running it)", a.GetName(), a.GetEngine(), a.GetResource(),
				a.GetConnection(), a.GetAdminUser(), a.GetCredentialsFile())
			if resp.DataReused {
				detail = fmt.Sprintf("app %s (%s) created on resource %s, which already held its data; the data "+
					"and its credentials (%s) are kept", a.GetName(), a.GetEngine(), a.GetResource(), a.GetCredentialsFile())
			}
			return nil, ok(detail), nil
		})

	addWrite(s, srv, writeTool("sds_app_snapshot", "Snapshot a database app",
		"Take a snapshot of every volume of a database app's resource on every replica, with the database "+
			"flushed and frozen on its node for the seconds it takes (the node thaws it by itself after 60s "+
			"if the controller does not)."),
		func(ctx context.Context, _ *mcp.CallToolRequest, in dbAppSnapshotIn) (*mcp.CallToolResult, opResult, error) {
			resp, err := s.client.SnapshotApp(ctx, in.Name, in.Snapshot)
			if err != nil {
				return nil, opResult{}, err
			}
			return nil, ok(resp.Message), nil
		})

	addWrite(s, srv, destructiveTool("sds_app_failover", "Fail a database app over",
		"Planned switchover: the node running the app stops it (service IP, database, mount) and another "+
			"replica starts it. Clients are disconnected for the seconds that takes and reconnect to the "+
			"service IP. Fails when no other replica took over."),
		func(ctx context.Context, _ *mcp.CallToolRequest, in dbAppNameIn) (*mcp.CallToolResult, opResult, error) {
			resp, err := s.client.FailoverApp(ctx, in.Name)
			if err != nil {
				return nil, opResult{}, err
			}
			return nil, ok(resp.Message), nil
		})

	addWrite(s, srv, destructiveTool("sds_app_delete", "Delete a database app",
		"Stop a database app and remove it from drbd-reactor and every node. The resource and its data are "+
			"kept (creating the app again picks them up) unless delete_data is set, which deletes the resource."),
		func(ctx context.Context, _ *mcp.CallToolRequest, in dbAppDeleteIn) (*mcp.CallToolResult, opResult, error) {
			msg, err := s.client.DeleteApp(ctx, in.Name, in.DeleteData)
			if err != nil {
				return nil, opResult{}, err
			}
			return nil, ok(msg), nil
		})
}
