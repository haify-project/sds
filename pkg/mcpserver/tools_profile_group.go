package mcpserver

import (
	"context"
	"fmt"

	haifypb "github.com/haify-project/haify/api/proto/v1"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func profileOut(profile *haifypb.ResourceProfile) resourceProfileOut {
	if profile == nil {
		return resourceProfileOut{}
	}
	return resourceProfileOut{
		Name: profile.Name, Protocol: profile.Protocol, StorageType: profile.StorageType,
		Pool: profile.Pool, Replicas: profile.Replicas,
		ReplicasOnDifferent: profile.ReplicasOnDifferent, ReplicasOnSame: profile.ReplicasOnSame,
		DrbdOptions: profile.DrbdOptions, Labels: profile.Labels,
	}
}

type profileOptionsIn struct {
	Name    string            `json:"name" jsonschema:"resource profile name"`
	Options map[string]string `json:"options" jsonschema:"DRBD options as section/key to value, e.g. net/max-buffers: 8000"`
}

type profileAdjustIn struct {
	Name   string `json:"name" jsonschema:"resource profile name"`
	DryRun bool   `json:"dry_run,omitempty" jsonschema:"report what would change without changing it"`
}

type setProfileIn struct {
	Resource string `json:"resource" jsonschema:"DRBD resource name"`
	Profile  string `json:"profile,omitempty" jsonschema:"profile to join; empty takes the resource out of its profile"`
}

type profileMemberOut struct {
	Resource string `json:"resource"`
	OK       bool   `json:"ok"`
	Message  string `json:"message"`
}

type profileGroupOut struct {
	OK      bool               `json:"ok"`
	Message string             `json:"message"`
	Members []profileMemberOut `json:"members"`
}

type profileMaxSizeOut struct {
	MaxSizeGB uint64   `json:"max_size_gb"`
	Nodes     []string `json:"nodes"`
	Thin      bool     `json:"thin"`
}

func membersOut(ms []*haifypb.ProfileMemberResult) []profileMemberOut {
	out := make([]profileMemberOut, 0, len(ms))
	for _, m := range ms {
		out = append(out, profileMemberOut{Resource: m.Resource, OK: m.Success, Message: m.Message})
	}
	return out
}

// registerProfileGroupTools adds the tools that treat a profile as the group
// of resources created from or attached to it.
func (s *Server) registerProfileGroupTools(srv *mcp.Server) {
	c, supported := s.client.(interface {
		SetResourceProfileOptions(context.Context, string, map[string]string) (*haifypb.SetResourceProfileOptionsResponse, error)
		AdjustResourceProfile(context.Context, string, bool) (*haifypb.AdjustResourceProfileResponse, error)
		GetResourceProfileMaxSize(context.Context, string) (*haifypb.GetResourceProfileMaxSizeResponse, error)
		SetResourceProfile(context.Context, string, string) error
	})
	if !supported {
		return
	}
	addRead(s, srv, readOnlyTool("haify_resource_profile_max_size", "Largest volume for a profile",
		"The largest volume a new resource in this profile could get now, given its pool, replica count and label constraints, and the nodes it would land on."),
		func(ctx context.Context, _ *mcp.CallToolRequest, in resourceProfileNameIn) (*mcp.CallToolResult, profileMaxSizeOut, error) {
			resp, err := c.GetResourceProfileMaxSize(ctx, in.Name)
			if err != nil {
				return nil, profileMaxSizeOut{}, err
			}
			return nil, profileMaxSizeOut{MaxSizeGB: resp.MaxSizeGb, Nodes: resp.Nodes, Thin: resp.Thin}, nil
		})
	addWrite(s, srv, writeTool("haify_resource_profile_set_options", "Set profile DRBD options",
		"Record DRBD options on a resource profile and apply them to every resource in it. Returns each member's outcome."),
		func(ctx context.Context, _ *mcp.CallToolRequest, in profileOptionsIn) (*mcp.CallToolResult, profileGroupOut, error) {
			resp, err := c.SetResourceProfileOptions(ctx, in.Name, in.Options)
			if err != nil {
				return nil, profileGroupOut{}, err
			}
			return nil, profileGroupOut{OK: resp.Success, Message: resp.Message, Members: membersOut(resp.Members)}, nil
		})
	addWrite(s, srv, writeTool("haify_resource_profile_adjust", "Adjust profile members",
		"Bring every resource in a profile into line with it: apply its DRBD options and add replicas to members with fewer than it asks for. Never removes a replica. Use dry_run first."),
		func(ctx context.Context, _ *mcp.CallToolRequest, in profileAdjustIn) (*mcp.CallToolResult, profileGroupOut, error) {
			resp, err := c.AdjustResourceProfile(ctx, in.Name, in.DryRun)
			if err != nil {
				return nil, profileGroupOut{}, err
			}
			return nil, profileGroupOut{OK: resp.Success, Message: resp.Message, Members: membersOut(resp.Members)}, nil
		})
	addWrite(s, srv, writeTool("haify_resource_set_profile", "Set a resource's profile",
		"Make a resource a member of a profile, applying the profile's DRBD options to it; an empty profile takes it out of the one it is in."),
		func(ctx context.Context, _ *mcp.CallToolRequest, in setProfileIn) (*mcp.CallToolResult, opResult, error) {
			if err := c.SetResourceProfile(ctx, in.Resource, in.Profile); err != nil {
				return nil, opResult{}, err
			}
			if in.Profile == "" {
				return nil, ok(in.Resource + " is in no profile"), nil
			}
			return nil, ok(fmt.Sprintf("%s is a member of %s", in.Resource, in.Profile)), nil
		})
}
