package mcpserver

import (
	"context"
	"fmt"

	sdspb "github.com/liliang-cn/sds/api/proto/v1"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// Snapshot naming follows the controller convention used by sds-cli:
// the data volume of a resource is "<resource>_data" inside its pool, so
// LVM snapshots target LV "<resource>_data" in VG <pool>, and ZFS
// snapshots target dataset "<pool>/<resource>_data".

func zfsDataset(pool, resource string) string {
	return fmt.Sprintf("%s/%s_data", pool, resource)
}

func lvmDataLV(resource string) string {
	return fmt.Sprintf("%s_data", resource)
}

// ---- input/output types ----

type snapshotIn struct {
	Resource    string `json:"resource" jsonschema:"DRBD resource name"`
	Name        string `json:"name" jsonschema:"snapshot name"`
	Node        string `json:"node" jsonschema:"node where the resource volume exists"`
	Pool        string `json:"pool" jsonschema:"storage pool backing the resource"`
	StorageType string `json:"storage_type,omitempty" jsonschema:"lvm (default) or zfs"`
}

type snapshotCreateIn struct {
	Resource    string `json:"resource" jsonschema:"DRBD resource name"`
	Name        string `json:"name" jsonschema:"snapshot name"`
	Node        string `json:"node" jsonschema:"node where the resource volume exists"`
	Pool        string `json:"pool" jsonschema:"storage pool backing the resource"`
	StorageType string `json:"storage_type,omitempty" jsonschema:"lvm (default) or zfs"`
	SizeGiB     uint32 `json:"size_gib,omitempty" jsonschema:"copy-on-write space in GiB reserved for the LVM snapshot (default 1); ignored for ZFS"`
}

type snapshotListIn struct {
	Resource    string `json:"resource" jsonschema:"DRBD resource name"`
	Node        string `json:"node" jsonschema:"node to query"`
	Pool        string `json:"pool" jsonschema:"storage pool backing the resource"`
	StorageType string `json:"storage_type,omitempty" jsonschema:"lvm (default) or zfs"`
}

type snapshotOut struct {
	Name      string `json:"name"`
	Volume    string `json:"volume,omitempty"`
	SizeGB    uint64 `json:"size_gb,omitempty"`
	CreatedAt string `json:"created_at,omitempty"`
}

type snapshotListOut struct {
	Snapshots []snapshotOut `json:"snapshots"`
}

// registerSnapshotTools adds storage-type-aware snapshot tools.
func (s *Server) registerSnapshotTools(srv *mcp.Server) {
	addWrite(s, srv, writeTool("sds_snapshot_create", "Create snapshot",
		"Create a point-in-time snapshot of a DRBD resource's data volume. "+
			"LVM uses copy-on-write snapshots (reserve enough COW space via size_gib); ZFS uses native snapshots."),
		func(ctx context.Context, _ *mcp.CallToolRequest, in snapshotCreateIn) (*mcp.CallToolResult, opResult, error) {
			if in.StorageType == "zfs" {
				if err := s.client.CreateZFSSnapshot(ctx, zfsDataset(in.Pool, in.Resource), in.Name, in.Node); err != nil {
					return nil, opResult{}, err
				}
			} else {
				size := in.SizeGiB
				if size == 0 {
					size = 1
				}
				err := s.client.CreateLvmSnapshot(ctx, in.Pool, lvmDataLV(in.Resource), in.Name,
					in.Node, fmt.Sprintf("%dG", size))
				if err != nil {
					return nil, opResult{}, err
				}
			}
			return nil, ok(fmt.Sprintf("snapshot %s created for resource %s on %s", in.Name, in.Resource, in.Node)), nil
		})

	addRead(s, srv, readOnlyTool("sds_snapshot_list", "List snapshots",
		"List snapshots of a DRBD resource's data volume on a node."),
		func(ctx context.Context, _ *mcp.CallToolRequest, in snapshotListIn) (*mcp.CallToolResult, snapshotListOut, error) {
			var (
				infos []*sdspb.SnapshotInfo
				err   error
			)
			if in.StorageType == "zfs" {
				infos, err = s.client.ListZFSSnapshots(ctx, zfsDataset(in.Pool, in.Resource), in.Node)
			} else {
				infos, err = s.client.ListLvmSnapshots(ctx, in.Pool, in.Node)
			}
			if err != nil {
				return nil, snapshotListOut{}, err
			}
			out := snapshotListOut{Snapshots: make([]snapshotOut, 0, len(infos))}
			for _, sn := range infos {
				out.Snapshots = append(out.Snapshots, snapshotOut{
					Name:      sn.Name,
					Volume:    sn.Volume,
					SizeGB:    sn.SizeGb,
					CreatedAt: sn.CreatedAt,
				})
			}
			return nil, out, nil
		})

	addWrite(s, srv, destructiveTool("sds_snapshot_delete", "Delete snapshot",
		"Delete a snapshot of a DRBD resource's data volume."),
		func(ctx context.Context, _ *mcp.CallToolRequest, in snapshotIn) (*mcp.CallToolResult, opResult, error) {
			if in.StorageType == "zfs" {
				snapshot := fmt.Sprintf("%s@%s", zfsDataset(in.Pool, in.Resource), in.Name)
				if err := s.client.DeleteZFSSnapshot(ctx, snapshot, in.Node); err != nil {
					return nil, opResult{}, err
				}
			} else {
				if err := s.client.DeleteLvmSnapshot(ctx, in.Pool, in.Name, in.Node); err != nil {
					return nil, opResult{}, err
				}
			}
			return nil, ok(fmt.Sprintf("snapshot %s of resource %s deleted on %s", in.Name, in.Resource, in.Node)), nil
		})

	addWrite(s, srv, destructiveTool("sds_snapshot_restore", "Restore snapshot",
		"Roll a DRBD resource's data volume back to a snapshot. All changes since the snapshot are lost. "+
			"The resource should be stopped (Secondary everywhere, unmounted) before restoring."),
		func(ctx context.Context, _ *mcp.CallToolRequest, in snapshotIn) (*mcp.CallToolResult, opResult, error) {
			if in.StorageType == "zfs" {
				if err := s.client.RestoreZFSSnapshot(ctx, zfsDataset(in.Pool, in.Resource), in.Name, in.Node); err != nil {
					return nil, opResult{}, err
				}
			} else {
				if err := s.client.RestoreLvmSnapshot(ctx, in.Pool, in.Name, in.Node); err != nil {
					return nil, opResult{}, err
				}
			}
			return nil, ok(fmt.Sprintf("resource %s restored from snapshot %s on %s", in.Resource, in.Name, in.Node)), nil
		})
}
