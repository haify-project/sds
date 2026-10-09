package mcpserver

import (
	"context"
	"fmt"

	haifypb "github.com/haify-project/haify/api/proto/v1"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type smbGatewayCreateIn struct {
	Resource   string   `json:"resource" jsonschema:"existing DRBD resource to share"`
	ServiceIP  string   `json:"service_ip" jsonschema:"floating service IP in CIDR notation, e.g. 192.168.1.210/24"`
	Workgroup  string   `json:"workgroup,omitempty" jsonschema:"workgroup; default WORKGROUP"`
	ShareName  string   `json:"share_name,omitempty" jsonschema:"name of the first share, over the whole volume; default the resource name"`
	ReadOnly   bool     `json:"read_only,omitempty"`
	ValidUsers []string `json:"valid_users,omitempty" jsonschema:"users allowed on the first share; default every user of the gateway"`
}

type smbSharesIn struct {
	Action     string   `json:"action" jsonschema:"list, add, or remove"`
	Resource   string   `json:"resource" jsonschema:"SMB gateway resource name"`
	Name       string   `json:"name,omitempty" jsonschema:"share name (required for add/remove)"`
	Path       string   `json:"path,omitempty" jsonschema:"directory under the gateway volume for add; default the whole volume"`
	ReadOnly   bool     `json:"read_only,omitempty"`
	ValidUsers []string `json:"valid_users,omitempty" jsonschema:"users allowed on the share (add); default all"`
}

type smbShareOut struct {
	Name       string   `json:"name"`
	Path       string   `json:"path"`
	ReadOnly   bool     `json:"read_only"`
	ValidUsers []string `json:"valid_users,omitempty"`
}

type smbSharesOut struct {
	Detail string        `json:"detail,omitempty"`
	Shares []smbShareOut `json:"shares,omitempty"`
}

// registerSMB adds the SMB gateway tools. Setting a user's password is left
// to the CLI on purpose: a password typed into a model conversation is a
// password stored in its transcript.
func (s *Server) registerSMB(srv *mcp.Server) {
	addWrite(s, srv, writeTool("haify_gateway_create_smb", "Create SMB gateway",
		"Share a DRBD resource over SMB (standalone Samba, workgroup) with automatic failover. "+
			"Users are added afterwards with `haify gateway smb user set`, which prompts for the password."),
		func(ctx context.Context, _ *mcp.CallToolRequest, in smbGatewayCreateIn) (*mcp.CallToolResult, opResult, error) {
			resp, err := s.client.CreateSMBGateway(ctx, &haifypb.CreateSMBGatewayRequest{
				Resource: in.Resource, ServiceIp: in.ServiceIP, Workgroup: in.Workgroup,
				ShareName: in.ShareName, ReadOnly: in.ReadOnly, ValidUsers: in.ValidUsers,
			})
			if err != nil {
				return nil, opResult{}, err
			}
			if !resp.Success {
				return nil, opResult{}, fmt.Errorf("create SMB gateway: %s", resp.Message)
			}
			return nil, ok(resp.Message), nil
		})

	addWrite(s, srv, destructiveTool("haify_gateway_smb_shares", "Manage SMB shares",
		"List, add or remove the shares of a running SMB gateway. Removing a share leaves its data in place."),
		func(ctx context.Context, _ *mcp.CallToolRequest, in smbSharesIn) (*mcp.CallToolResult, smbSharesOut, error) {
			switch in.Action {
			case "list":
				shares, err := s.client.ListSMBShares(ctx, in.Resource)
				if err != nil {
					return nil, smbSharesOut{}, err
				}
				out := smbSharesOut{}
				for _, sh := range shares {
					out.Shares = append(out.Shares, smbShareOut{Name: sh.Name, Path: "/" + sh.Path, ReadOnly: sh.ReadOnly, ValidUsers: sh.ValidUsers})
				}
				return nil, out, nil
			case "add":
				err := s.client.AddSMBShare(ctx, in.Resource, &haifypb.SMBShareInfo{
					Name: in.Name, Path: in.Path, ReadOnly: in.ReadOnly, ValidUsers: in.ValidUsers,
				})
				if err != nil {
					return nil, smbSharesOut{}, err
				}
				return nil, smbSharesOut{Detail: "share " + in.Name + " added"}, nil
			case "remove":
				if err := s.client.RemoveSMBShare(ctx, in.Resource, in.Name); err != nil {
					return nil, smbSharesOut{}, err
				}
				return nil, smbSharesOut{Detail: "share " + in.Name + " removed; its data was left in place"}, nil
			}
			return nil, smbSharesOut{}, badAction(in.Action, "list, add, remove")
		})

	addRead(s, srv, readOnlyTool("haify_gateway_smb_users", "List SMB users",
		"List the users of a running SMB gateway."),
		func(ctx context.Context, _ *mcp.CallToolRequest, in gatewayResourceIn) (*mcp.CallToolResult, stringListOut, error) {
			users, err := s.client.ListSMBUsers(ctx, in.Resource)
			if err != nil {
				return nil, stringListOut{}, err
			}
			return nil, stringListOut{Items: users}, nil
		})
}
