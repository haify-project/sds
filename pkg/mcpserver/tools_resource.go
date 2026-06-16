package mcpserver

import (
	"context"
	"fmt"

	sdspb "github.com/liliang-cn/sds/api/proto/v1"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// ---- output types ----

type volumeOut struct {
	VolumeID      uint32 `json:"volume_id"`
	Device        string `json:"device,omitempty" jsonschema:"DRBD device path, e.g. /dev/drbd0"`
	SizeGB        uint64 `json:"size_gb"`
	Pool          string `json:"pool,omitempty"`
	BackingVolume string `json:"backing_volume,omitempty" jsonschema:"backing LV/dataset name inside the pool"`
}

type nodeStateOut struct {
	Node      string `json:"node"`
	Role      string `json:"role,omitempty" jsonschema:"DRBD role: Primary or Secondary"`
	DiskState string `json:"disk_state,omitempty" jsonschema:"DRBD disk state, e.g. UpToDate"`
}

type resourceOut struct {
	Name          string         `json:"name"`
	Port          uint32         `json:"port"`
	Protocol      string         `json:"protocol,omitempty"`
	Nodes         []string       `json:"nodes"`
	Role          string         `json:"role,omitempty"`
	Volumes       []volumeOut    `json:"volumes,omitempty"`
	NodeStates    []nodeStateOut `json:"node_states,omitempty"`
	DisklessNodes []string       `json:"diskless_nodes,omitempty"`
	QuorumRisk    bool           `json:"quorum_risk,omitempty"`
}

type resourceListOut struct {
	Resources []resourceOut `json:"resources"`
}

func volumesOut(vols []*sdspb.VolumeInfo) []volumeOut {
	out := make([]volumeOut, 0, len(vols))
	for _, v := range vols {
		out = append(out, volumeOut{
			VolumeID:      v.VolumeId,
			Device:        v.Device,
			SizeGB:        v.SizeGb,
			Pool:          v.Pool,
			BackingVolume: v.BackingVolume,
		})
	}
	return out
}

func nodeStatesOut(states map[string]*sdspb.NodeResourceState) []nodeStateOut {
	out := make([]nodeStateOut, 0, len(states))
	for node, st := range states {
		out = append(out, nodeStateOut{Node: node, Role: st.Role, DiskState: st.DiskState})
	}
	return out
}

// ---- input types ----

type resourceNameIn struct {
	Name string `json:"name" jsonschema:"DRBD resource name"`
}

type resourceCreateIn struct {
	Name        string            `json:"name" jsonschema:"DRBD resource name"`
	Port        uint32            `json:"port" jsonschema:"DRBD replication TCP port, e.g. 7001; must be unique per resource"`
	Nodes       []string          `json:"nodes" jsonschema:"nodes to replicate across, e.g. [\"orange1\",\"orange2\"]"`
	Pool        string            `json:"pool" jsonschema:"backing storage pool name"`
	SizeGB      uint32            `json:"size_gb" jsonschema:"volume size in GiB"`
	StorageType string            `json:"storage_type,omitempty" jsonschema:"backing storage type: lvm (default) or zfs"`
	Protocol    string            `json:"protocol,omitempty" jsonschema:"DRBD protocol A, B, or C (default C)"`
	DrbdOptions map[string]string `json:"drbd_options,omitempty" jsonschema:"extra DRBD options, e.g. {\"options/on-no-quorum\":\"suspend-io\"}"`
}

type resourceSetRoleIn struct {
	Resource string `json:"resource" jsonschema:"DRBD resource name"`
	Node     string `json:"node" jsonschema:"target node"`
	Role     string `json:"role" jsonschema:"desired role: primary or secondary"`
	Force    bool   `json:"force,omitempty" jsonschema:"force promotion (needed for the first promotion of a new resource)"`
}

type volumeAddIn struct {
	Resource string `json:"resource" jsonschema:"DRBD resource name"`
	Volume   string `json:"volume" jsonschema:"volume name"`
	Pool     string `json:"pool" jsonschema:"backing storage pool"`
	SizeGB   uint32 `json:"size_gb" jsonschema:"volume size in GiB"`
}

type resourceSetOptionsIn struct {
	Resource string            `json:"resource" jsonschema:"DRBD resource name"`
	Options  map[string]string `json:"options" jsonschema:"DRBD options as section/key -> value (e.g. net/max-buffers: 8000, on-no-quorum: suspend-io, disk/on-io-error: detach); a bare key defaults to the resource-level options section"`
}

type volumeRemoveIn struct {
	Resource string `json:"resource" jsonschema:"DRBD resource name"`
	VolumeID uint32 `json:"volume_id" jsonschema:"volume ID within the resource"`
}

type volumeResizeIn struct {
	Resource string `json:"resource" jsonschema:"DRBD resource name"`
	VolumeID uint32 `json:"volume_id" jsonschema:"volume ID within the resource"`
	SizeGB   uint32 `json:"size_gb" jsonschema:"new size in GiB (must be larger than current)"`
}

type filesystemIn struct {
	Resource string `json:"resource" jsonschema:"DRBD resource name"`
	VolumeID uint32 `json:"volume_id" jsonschema:"volume ID within the resource (0 for the first volume)"`
	Fstype   string `json:"fstype" jsonschema:"filesystem type: ext4 or xfs"`
	Node     string `json:"node" jsonschema:"node where the resource is Primary"`
}

type mountIn struct {
	Resource string `json:"resource" jsonschema:"DRBD resource name"`
	VolumeID uint32 `json:"volume_id" jsonschema:"volume ID within the resource (0 for the first volume)"`
	Path     string `json:"path" jsonschema:"mount point, e.g. /mnt/data"`
	Node     string `json:"node" jsonschema:"node where the resource is Primary"`
	Fstype   string `json:"fstype,omitempty" jsonschema:"filesystem type (default ext4)"`
}

type unmountIn struct {
	Resource string `json:"resource" jsonschema:"DRBD resource name"`
	VolumeID uint32 `json:"volume_id" jsonschema:"volume ID within the resource"`
	Node     string `json:"node" jsonschema:"node where the volume is mounted"`
}

// registerResourceTools adds DRBD resource and volume tools.
func (s *Server) registerResourceTools(srv *mcp.Server) {
	addRead(s, srv, readOnlyTool("sds_resource_list", "List resources",
		"List all DRBD resources with their nodes, volumes, and replication state."),
		func(ctx context.Context, _ *mcp.CallToolRequest, _ struct{}) (*mcp.CallToolResult, resourceListOut, error) {
			resources, err := s.client.ListResources(ctx)
			if err != nil {
				return nil, resourceListOut{}, err
			}
			out := resourceListOut{Resources: make([]resourceOut, 0, len(resources))}
			for _, r := range resources {
				out.Resources = append(out.Resources, resourceOut{
					Name:          r.Name,
					Port:          r.Port,
					Protocol:      r.Protocol,
					Nodes:         r.Nodes,
					Role:          r.Role,
					Volumes:       volumesOut(r.Volumes),
					NodeStates:    nodeStatesOut(r.NodeStates),
					DisklessNodes: r.DisklessNodes,
					QuorumRisk:    r.QuorumRisk,
				})
			}
			return nil, out, nil
		})

	addRead(s, srv, readOnlyTool("sds_resource_status", "Resource status",
		"Show live DRBD status for one resource: per-node role and disk state (UpToDate, Inconsistent, ...)."),
		func(ctx context.Context, _ *mcp.CallToolRequest, in resourceNameIn) (*mcp.CallToolResult, resourceOut, error) {
			st, err := s.client.ResourceStatus(ctx, in.Name)
			if err != nil {
				return nil, resourceOut{}, err
			}
			return nil, resourceOut{
				Name:       st.Name,
				Role:       st.Role,
				Nodes:      st.Nodes,
				Volumes:    volumesOut(st.Volumes),
				NodeStates: nodeStatesOut(st.NodeStates),
			}, nil
		})

	addWrite(s, srv, writeTool("sds_resource_create", "Create resource",
		"Create a replicated DRBD resource backed by a storage pool. Allocates volumes on every node, "+
			"writes the DRBD config, and brings the resource up. Initial sync starts automatically."),
		func(ctx context.Context, _ *mcp.CallToolRequest, in resourceCreateIn) (*mcp.CallToolResult, opResult, error) {
			if len(in.Nodes) == 0 {
				return nil, opResult{}, fmt.Errorf("nodes are required")
			}
			storageType := in.StorageType
			if storageType == "" {
				storageType = "lvm"
			}
			protocol := in.Protocol
			if protocol == "" {
				protocol = "C"
			}
			err := s.client.CreateResourceWithPoolAndType(ctx, in.Name, in.Port, in.Nodes,
				protocol, in.SizeGB, in.Pool, storageType, in.DrbdOptions)
			if err != nil {
				return nil, opResult{}, err
			}
			return nil, ok(fmt.Sprintf("resource %s created on %d node(s), %d GiB from pool %s",
				in.Name, len(in.Nodes), in.SizeGB, in.Pool)), nil
		})

	addWrite(s, srv, destructiveTool("sds_resource_delete", "Delete resource",
		"Delete a DRBD resource and its backing volumes on all nodes. All data on the resource is lost."),
		func(ctx context.Context, _ *mcp.CallToolRequest, in resourceNameIn) (*mcp.CallToolResult, opResult, error) {
			if err := s.client.DeleteResource(ctx, in.Name); err != nil {
				return nil, opResult{}, err
			}
			return nil, ok(fmt.Sprintf("resource %s deleted", in.Name)), nil
		})

	addWrite(s, srv, writeTool("sds_resource_set_role", "Set resource role",
		"Promote a resource to Primary or demote it to Secondary on a node. "+
			"Only the Primary node can mount and write the volume."),
		func(ctx context.Context, _ *mcp.CallToolRequest, in resourceSetRoleIn) (*mcp.CallToolResult, opResult, error) {
			switch in.Role {
			case "primary":
				if err := s.client.SetPrimary(ctx, in.Resource, in.Node, in.Force); err != nil {
					return nil, opResult{}, err
				}
			case "secondary":
				if err := s.client.SetSecondary(ctx, in.Resource, in.Node); err != nil {
					return nil, opResult{}, err
				}
			default:
				return nil, opResult{}, fmt.Errorf("invalid role %q (use primary or secondary)", in.Role)
			}
			return nil, ok(fmt.Sprintf("resource %s is now %s on %s", in.Resource, in.Role, in.Node)), nil
		})

	addWrite(s, srv, writeTool("sds_resource_add_volume", "Add volume",
		"Add another replicated volume to an existing DRBD resource."),
		func(ctx context.Context, _ *mcp.CallToolRequest, in volumeAddIn) (*mcp.CallToolResult, opResult, error) {
			if err := s.client.AddVolume(ctx, in.Resource, in.Volume, in.Pool, in.SizeGB); err != nil {
				return nil, opResult{}, err
			}
			return nil, ok(fmt.Sprintf("volume %s (%d GiB) added to resource %s", in.Volume, in.SizeGB, in.Resource)), nil
		})

	addWrite(s, srv, writeTool("sds_resource_set_options", "Set DRBD options",
		"Update DRBD options on an existing resource and apply them live with "+
			"`drbdadm adjust`, without recreating it. Options use the "+
			"\"section/key\" form (e.g. net/max-buffers, disk/on-io-error); a bare "+
			"key defaults to the resource-level options section."),
		func(ctx context.Context, _ *mcp.CallToolRequest, in resourceSetOptionsIn) (*mcp.CallToolResult, opResult, error) {
			if len(in.Options) == 0 {
				return nil, opResult{}, fmt.Errorf("options must not be empty")
			}
			if err := s.client.UpdateResourceOptions(ctx, in.Resource, in.Options); err != nil {
				return nil, opResult{}, err
			}
			return nil, ok(fmt.Sprintf("applied %d option(s) to resource %s and adjusted", len(in.Options), in.Resource)), nil
		})

	addWrite(s, srv, destructiveTool("sds_resource_remove_volume", "Remove volume",
		"Remove a volume from a DRBD resource on all nodes. Data on the volume is lost."),
		func(ctx context.Context, _ *mcp.CallToolRequest, in volumeRemoveIn) (*mcp.CallToolResult, opResult, error) {
			if err := s.client.RemoveVolume(ctx, in.Resource, in.VolumeID); err != nil {
				return nil, opResult{}, err
			}
			return nil, ok(fmt.Sprintf("volume %d removed from resource %s", in.VolumeID, in.Resource)), nil
		})

	addWrite(s, srv, writeTool("sds_resource_resize_volume", "Resize volume",
		"Grow a DRBD volume on all nodes. Shrinking is not supported. "+
			"The filesystem must be grown separately afterwards (resize2fs/xfs_growfs)."),
		func(ctx context.Context, _ *mcp.CallToolRequest, in volumeResizeIn) (*mcp.CallToolResult, opResult, error) {
			if err := s.client.ResizeVolume(ctx, in.Resource, in.VolumeID, in.SizeGB); err != nil {
				return nil, opResult{}, err
			}
			return nil, ok(fmt.Sprintf("volume %d of resource %s resized to %d GiB", in.VolumeID, in.Resource, in.SizeGB)), nil
		})

	addWrite(s, srv, destructiveTool("sds_resource_create_filesystem", "Create filesystem",
		"Format a DRBD volume with a filesystem on the Primary node. Any existing data is destroyed."),
		func(ctx context.Context, _ *mcp.CallToolRequest, in filesystemIn) (*mcp.CallToolResult, opResult, error) {
			if err := s.client.CreateFilesystem(ctx, in.Resource, in.VolumeID, in.Node, in.Fstype); err != nil {
				return nil, opResult{}, err
			}
			return nil, ok(fmt.Sprintf("%s filesystem created on %s volume %d (node %s)",
				in.Fstype, in.Resource, in.VolumeID, in.Node)), nil
		})

	addWrite(s, srv, writeTool("sds_resource_mount", "Mount volume",
		"Mount a DRBD volume on its Primary node."),
		func(ctx context.Context, _ *mcp.CallToolRequest, in mountIn) (*mcp.CallToolResult, opResult, error) {
			fstype := in.Fstype
			if fstype == "" {
				fstype = "ext4"
			}
			if err := s.client.MountResource(ctx, in.Resource, in.VolumeID, in.Path, in.Node, fstype); err != nil {
				return nil, opResult{}, err
			}
			return nil, ok(fmt.Sprintf("%s volume %d mounted at %s on %s", in.Resource, in.VolumeID, in.Path, in.Node)), nil
		})

	addWrite(s, srv, writeTool("sds_resource_unmount", "Unmount volume",
		"Unmount a DRBD volume on a node."),
		func(ctx context.Context, _ *mcp.CallToolRequest, in unmountIn) (*mcp.CallToolResult, opResult, error) {
			if err := s.client.UnmountResource(ctx, in.Resource, in.VolumeID, in.Node); err != nil {
				return nil, opResult{}, err
			}
			return nil, ok(fmt.Sprintf("%s volume %d unmounted on %s", in.Resource, in.VolumeID, in.Node)), nil
		})
}
