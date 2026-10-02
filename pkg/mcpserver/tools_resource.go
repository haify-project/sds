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
	Node             string  `json:"node"`
	Role             string  `json:"role,omitempty" jsonschema:"DRBD role: Primary or Secondary"`
	DiskState        string  `json:"disk_state,omitempty" jsonschema:"DRBD disk state, e.g. UpToDate"`
	ReplicationState string  `json:"replication_state,omitempty" jsonschema:"DRBD replication state, e.g. Established, SyncSource, SyncTarget"`
	SyncPercent      float64 `json:"sync_percent,omitempty" jsonschema:"resync completion 0..100; 100 in steady state, lower while a resync is in progress"`
}

type resourceOut struct {
	Name          string            `json:"name"`
	Port          uint32            `json:"port"`
	Protocol      string            `json:"protocol,omitempty"`
	Nodes         []string          `json:"nodes"`
	Role          string            `json:"role,omitempty"`
	Volumes       []volumeOut       `json:"volumes,omitempty"`
	NodeStates    []nodeStateOut    `json:"node_states,omitempty"`
	DisklessNodes []string          `json:"diskless_nodes,omitempty"`
	QuorumRisk    bool              `json:"quorum_risk,omitempty"`
	Profile       string            `json:"profile,omitempty"`
	Labels        map[string]string `json:"labels,omitempty"`
}

type resourceListOut struct {
	Resources []resourceOut `json:"resources"`
}

type resourceProfileOut struct {
	Name                string            `json:"name"`
	Protocol            string            `json:"protocol,omitempty"`
	StorageType         string            `json:"storage_type,omitempty"`
	Pool                string            `json:"pool,omitempty"`
	Replicas            uint32            `json:"replicas,omitempty"`
	ReplicasOnDifferent []string          `json:"replicas_on_different,omitempty"`
	ReplicasOnSame      []string          `json:"replicas_on_same,omitempty"`
	DrbdOptions         map[string]string `json:"drbd_options,omitempty"`
	Labels              map[string]string `json:"labels,omitempty"`
}

type resourceProfileListOut struct {
	Profiles []resourceProfileOut `json:"profiles"`
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
		out = append(out, nodeStateOut{
			Node:             node,
			Role:             st.Role,
			DiskState:        st.DiskState,
			ReplicationState: st.ReplicationState,
			SyncPercent:      st.SyncPercent,
		})
	}
	return out
}

// ---- input types ----

type resourceNameIn struct {
	Name string `json:"name" jsonschema:"DRBD resource name"`
}

type resourceProfileIn struct {
	Name                string            `json:"name" jsonschema:"resource profile name"`
	Protocol            string            `json:"protocol,omitempty" jsonschema:"default DRBD protocol A, B, or C"`
	StorageType         string            `json:"storage_type,omitempty" jsonschema:"default storage type: lvm, lvm-thin, or zfs"`
	Pool                string            `json:"pool,omitempty" jsonschema:"default storage pool"`
	Replicas            uint32            `json:"replicas,omitempty" jsonschema:"default replica count"`
	ReplicasOnDifferent []string          `json:"replicas_on_different,omitempty" jsonschema:"node-label keys used to spread replicas"`
	ReplicasOnSame      []string          `json:"replicas_on_same,omitempty" jsonschema:"node-label keys replicas must share"`
	DrbdOptions         map[string]string `json:"drbd_options,omitempty" jsonschema:"default DRBD options"`
	Labels              map[string]string `json:"labels,omitempty" jsonschema:"default resource labels"`
}

type resourceProfileNameIn struct {
	Name string `json:"name" jsonschema:"resource profile name"`
}

type volumeSpecIn struct {
	SizeGB uint32 `json:"size_gb" jsonschema:"volume size in GiB"`
	Pool   string `json:"pool,omitempty" jsonschema:"backing storage pool for this volume; auto-selected when empty"`
}

type resourceCreateIn struct {
	Name        string            `json:"name" jsonschema:"DRBD resource name"`
	Port        uint32            `json:"port" jsonschema:"DRBD replication TCP port, e.g. 7001; must be unique per resource"`
	Nodes       []string          `json:"nodes" jsonschema:"nodes to replicate across, e.g. [\"orange1\",\"orange2\"]"`
	Pool        string            `json:"pool,omitempty" jsonschema:"backing storage pool name (single-volume shorthand; ignored when volumes is set)"`
	SizeGB      uint32            `json:"size_gb,omitempty" jsonschema:"volume size in GiB (single-volume shorthand; ignored when volumes is set)"`
	Volumes     []volumeSpecIn    `json:"volumes,omitempty" jsonschema:"optional list of volumes for a multi-volume resource; each element is {size_gb, pool}. When set, volume 0..N are created atomically and the top-level size_gb/pool are ignored. Leave empty for a single-volume resource."`
	StorageType string            `json:"storage_type,omitempty" jsonschema:"backing storage type: lvm (default), lvm-thin, or zfs (applies to all volumes)"`
	Protocol    string            `json:"protocol,omitempty" jsonschema:"DRBD protocol A, B, or C (default C)"`
	DrbdOptions map[string]string `json:"drbd_options,omitempty" jsonschema:"extra DRBD options, e.g. {\"options/on-no-quorum\":\"suspend-io\"}"`
	Profile     string            `json:"profile,omitempty" jsonschema:"optional resource profile whose defaults are applied at creation"`
	Labels      map[string]string `json:"labels,omitempty" jsonschema:"resource metadata labels"`
}

type resourceSetRoleIn struct {
	Resource      string `json:"resource" jsonschema:"DRBD resource name"`
	Node          string `json:"node" jsonschema:"target node"`
	Role          string `json:"role" jsonschema:"desired role: primary or secondary"`
	Force         bool   `json:"force,omitempty" jsonschema:"force promotion (needed for the first promotion of a new resource)"`
	QuorumGuarded bool   `json:"quorum_guarded,omitempty" jsonschema:"safe hard-failover promote (primary only): the controller force-promotes only if the node holds DRBD quorum and refuses otherwise, preventing split-brain. Use for taking over after a hard node failure. Ignored when role is secondary."`
}

type resourceDualPrimaryIn struct {
	Resource string `json:"resource" jsonschema:"DRBD resource name"`
	Enable   bool   `json:"enable" jsonschema:"true opens the dual-primary window, false closes it"`
}

type resourceAdoptIn struct {
	Resource string   `json:"resource" jsonschema:"DRBD resource name to adopt (must already exist on the nodes)"`
	Nodes    []string `json:"nodes,omitempty" jsonschema:"nodes the resource lives on; auto-discovered from the .res when omitted"`
	Port     uint32   `json:"port,omitempty" jsonschema:"DRBD replication port; auto-discovered from the .res when omitted"`
	Protocol string   `json:"protocol,omitempty" jsonschema:"DRBD protocol A, B, or C; auto-discovered from the .res when omitted"`
}

type resourceAdoptOut struct {
	Resource string   `json:"resource"`
	Nodes    []string `json:"nodes,omitempty" jsonschema:"nodes recorded for the resource"`
	Port     uint32   `json:"port,omitempty"`
	Protocol string   `json:"protocol,omitempty"`
	Volumes  uint32   `json:"volumes" jsonschema:"number of volumes discovered and recorded"`
	Detail   string   `json:"detail,omitempty"`
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
	profileClient, profilesSupported := s.client.(interface {
		CreateResourceProfile(context.Context, *sdspb.ResourceProfile) (*sdspb.ResourceProfile, error)
		GetResourceProfile(context.Context, string) (*sdspb.ResourceProfile, error)
		ListResourceProfiles(context.Context) ([]*sdspb.ResourceProfile, error)
		DeleteResourceProfile(context.Context, string) error
	})
	if profilesSupported {
		addRead(s, srv, readOnlyTool("sds_resource_profile_list", "List resource profiles",
			"List reusable resource creation profiles."),
			func(ctx context.Context, _ *mcp.CallToolRequest, _ struct{}) (*mcp.CallToolResult, resourceProfileListOut, error) {
				profiles, err := profileClient.ListResourceProfiles(ctx)
				if err != nil {
					return nil, resourceProfileListOut{}, err
				}
				out := resourceProfileListOut{Profiles: make([]resourceProfileOut, 0, len(profiles))}
				for _, profile := range profiles {
					out.Profiles = append(out.Profiles, profileOut(profile))
				}
				return nil, out, nil
			})
		addRead(s, srv, readOnlyTool("sds_resource_profile_get", "Get resource profile",
			"Get one reusable resource creation profile."),
			func(ctx context.Context, _ *mcp.CallToolRequest, in resourceProfileNameIn) (*mcp.CallToolResult, resourceProfileOut, error) {
				profile, err := profileClient.GetResourceProfile(ctx, in.Name)
				if err != nil {
					return nil, resourceProfileOut{}, err
				}
				return nil, profileOut(profile), nil
			})
		addWrite(s, srv, writeTool("sds_resource_profile_create", "Create resource profile",
			"Create or replace a reusable resource creation profile. Existing resources are not changed."),
			func(ctx context.Context, _ *mcp.CallToolRequest, in resourceProfileIn) (*mcp.CallToolResult, resourceProfileOut, error) {
				profile, err := profileClient.CreateResourceProfile(ctx, &sdspb.ResourceProfile{
					Name: in.Name, Protocol: in.Protocol, StorageType: in.StorageType, Pool: in.Pool,
					Replicas: in.Replicas, ReplicasOnDifferent: in.ReplicasOnDifferent,
					ReplicasOnSame: in.ReplicasOnSame, DrbdOptions: in.DrbdOptions, Labels: in.Labels,
				})
				if err != nil {
					return nil, resourceProfileOut{}, err
				}
				return nil, profileOut(profile), nil
			})
		addWrite(s, srv, destructiveTool("sds_resource_profile_delete", "Delete resource profile",
			"Delete a resource profile. Refused while any resource is still a member; take members out with sds_resource_set_profile first."),
			func(ctx context.Context, _ *mcp.CallToolRequest, in resourceProfileNameIn) (*mcp.CallToolResult, opResult, error) {
				if err := profileClient.DeleteResourceProfile(ctx, in.Name); err != nil {
					return nil, opResult{}, err
				}
				return nil, ok(fmt.Sprintf("resource profile %s deleted", in.Name)), nil
			})
	}
	s.registerProfileGroupTools(srv)

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
					Profile:       r.Profile,
					Labels:        r.Labels,
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
			"writes the DRBD config, and brings the resource up. Initial sync starts automatically. "+
			"For a single volume pass size_gb (and optionally pool). For a multi-volume resource pass "+
			"the volumes array instead — each element is {size_gb, pool} and becomes DRBD volume 0..N; "+
			"the top-level size_gb/pool are then ignored."),
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
			requestClient, metadataAware := s.client.(interface {
				CreateResourceRequest(context.Context, *sdspb.CreateResourceRequest) error
			})
			if in.Profile != "" || len(in.Labels) > 0 {
				if !metadataAware {
					return nil, opResult{}, fmt.Errorf("controller client does not support resource profiles or labels")
				}
				volumes := make([]*sdspb.VolumeSpec, 0, len(in.Volumes))
				for _, v := range in.Volumes {
					volumes = append(volumes, &sdspb.VolumeSpec{SizeGb: v.SizeGB, Pool: v.Pool})
				}
				if err := requestClient.CreateResourceRequest(ctx, &sdspb.CreateResourceRequest{
					Name: in.Name, Port: in.Port, Nodes: in.Nodes, Pool: in.Pool, SizeGb: in.SizeGB,
					Volumes: volumes, StorageType: storageType, Protocol: protocol,
					DrbdOptions: in.DrbdOptions, Profile: in.Profile, Labels: in.Labels,
				}); err != nil {
					return nil, opResult{}, err
				}
				return nil, ok(fmt.Sprintf("resource %s created with profile %s", in.Name, in.Profile)), nil
			}
			if len(in.Volumes) > 0 {
				volumes := make([]*sdspb.VolumeSpec, 0, len(in.Volumes))
				for _, v := range in.Volumes {
					volumes = append(volumes, &sdspb.VolumeSpec{SizeGb: v.SizeGB, Pool: v.Pool})
				}
				if err := s.client.CreateResourceWithVolumes(ctx, in.Name, in.Port, in.Nodes,
					protocol, storageType, in.DrbdOptions, volumes); err != nil {
					return nil, opResult{}, err
				}
				return nil, ok(fmt.Sprintf("resource %s created on %d node(s) with %d volume(s)",
					in.Name, len(in.Nodes), len(volumes))), nil
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

	addWrite(s, srv, destructiveTool("sds_resource_set_role", "Set resource role",
		"Promote a resource to Primary or demote it to Secondary on a node. "+
			"Only the Primary node can mount and write the volume. Demoting fails while the volume is "+
			"mounted or in use. Do not use it on a resource an HA promoter runs: the promoter puts the "+
			"role back within seconds — use sds_ha_evict. Set quorum_guarded=true "+
			"for a safe hard-failover promote: the controller force-promotes only if the node "+
			"holds DRBD quorum and refuses otherwise, avoiding split-brain."),
		func(ctx context.Context, _ *mcp.CallToolRequest, in resourceSetRoleIn) (*mcp.CallToolResult, opResult, error) {
			switch in.Role {
			case "primary":
				if in.QuorumGuarded {
					if err := s.client.PromoteForNode(ctx, in.Resource, in.Node); err != nil {
						return nil, opResult{}, err
					}
					return nil, ok(fmt.Sprintf("resource %s safely promoted to primary on %s (quorum-guarded)", in.Resource, in.Node)), nil
				}
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

	addWrite(s, srv, writeTool("sds_resource_dual_primary", "Toggle dual-primary",
		"Open or close a DRBD dual-primary (allow-two-primaries) window on a resource. "+
			"This exists for hypervisor LIVE MIGRATION, where the source and target host both "+
			"hold the disk open during hand-off — it is not a way to share a volume between two "+
			"machines (the filesystem on it would corrupt). WAN resources are refused because "+
			"their replication is asynchronous. Always close the window after the migration; "+
			"closing is idempotent and verifies no node is left dual-primary."),
		func(ctx context.Context, _ *mcp.CallToolRequest, in resourceDualPrimaryIn) (*mcp.CallToolResult, opResult, error) {
			if in.Resource == "" {
				return nil, opResult{}, fmt.Errorf("resource is required")
			}
			if err := s.client.SetDualPrimary(ctx, in.Resource, in.Enable); err != nil {
				return nil, opResult{}, err
			}
			if in.Enable {
				return nil, ok(fmt.Sprintf("dual-primary window OPEN on %s — close it as soon as the migration finishes", in.Resource)), nil
			}
			return nil, ok(fmt.Sprintf("dual-primary window closed on %s", in.Resource)), nil
		})

	addWrite(s, srv, writeTool("sds_resource_adopt", "Adopt resource",
		"Adopt a pre-existing/foreign DRBD resource into SDS management. Auto-discovers "+
			"nodes/port/volumes from the resource's .res on a node when omitted. Writes only "+
			"SDS metadata — never touches the DRBD device or data."),
		func(ctx context.Context, _ *mcp.CallToolRequest, in resourceAdoptIn) (*mcp.CallToolResult, resourceAdoptOut, error) {
			if in.Resource == "" {
				return nil, resourceAdoptOut{}, fmt.Errorf("resource is required")
			}
			resp, err := s.client.AdoptResource(ctx, in.Resource, in.Nodes, in.Port, in.Protocol)
			if err != nil {
				return nil, resourceAdoptOut{}, err
			}
			return nil, resourceAdoptOut{
				Resource: in.Resource,
				Nodes:    resp.Nodes,
				Port:     resp.Port,
				Protocol: resp.Protocol,
				Volumes:  resp.Volumes,
				Detail: fmt.Sprintf("resource %s adopted (%d node(s), %d volume(s), port %d)",
					in.Resource, len(resp.Nodes), resp.Volumes, resp.Port),
			}, nil
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
		"Grow a DRBD volume on all nodes. Shrinking is not supported. The space is not usable "+
			"until the filesystem on the Primary is grown too (resize2fs/xfs_growfs) — this tool "+
			"does not do that."),
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

	addWrite(s, srv, destructiveTool("sds_resource_unmount", "Unmount volume",
		"Unmount a DRBD volume on a node."),
		func(ctx context.Context, _ *mcp.CallToolRequest, in unmountIn) (*mcp.CallToolResult, opResult, error) {
			if err := s.client.UnmountResource(ctx, in.Resource, in.VolumeID, in.Node); err != nil {
				return nil, opResult{}, err
			}
			return nil, ok(fmt.Sprintf("%s volume %d unmounted on %s", in.Resource, in.VolumeID, in.Node)), nil
		})
}
