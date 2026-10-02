package mcpserver

import (
	"context"
	"fmt"

	sdspb "github.com/liliang-cn/sds/api/proto/v1"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// Snapshot naming follows the controller convention used by sds:
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
	// Origin is the volume the snapshot was taken from, which is what says
	// which resource it belongs to when a pool holds several.
	Origin string `json:"origin,omitempty"`
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
				infos, err = s.client.ListLvmSnapshots(ctx, in.Pool, in.Node, in.Resource)
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
					Origin:    sn.Origin,
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

	s.registerSnapshotScheduleTools(srv)
}

type scheduleCreateIn struct {
	Resource string `json:"resource" jsonschema:"DRBD resource to snapshot on a schedule"`
	Cron     string `json:"cron" jsonschema:"standard 5-field cron expression, e.g. '0 * * * *' for hourly"`
	Hourly   int32  `json:"keep_hourly,omitempty" jsonschema:"hourly snapshots to retain"`
	Daily    int32  `json:"keep_daily,omitempty" jsonschema:"daily snapshots to retain"`
	Weekly   int32  `json:"keep_weekly,omitempty" jsonschema:"weekly snapshots to retain"`
	Monthly  int32  `json:"keep_monthly,omitempty" jsonschema:"monthly snapshots to retain"`
	Yearly   int32  `json:"keep_yearly,omitempty" jsonschema:"yearly snapshots to retain"`
	Disabled bool   `json:"disabled,omitempty" jsonschema:"create the schedule disabled"`
}

type scheduleNameIn struct {
	Name string `json:"name" jsonschema:"schedule name (equals the resource name)"`
}

type scheduleOut struct {
	Name     string `json:"name"`
	Resource string `json:"resource"`
	Cron     string `json:"cron"`
	Enabled  bool   `json:"enabled"`
	Keep     string `json:"keep"`
	LastRun  string `json:"last_run,omitempty"`
	NextRun  string `json:"next_run,omitempty"`
}

type scheduleListOut struct {
	Schedules []scheduleOut `json:"schedules"`
}

// registerSnapshotScheduleTools adds cron-driven snapshot schedule tools.
func (s *Server) registerSnapshotScheduleTools(srv *mcp.Server) {
	addWrite(s, srv, writeTool("sds_snapshot_schedule_create", "Create snapshot schedule",
		"Create (or replace) a cron-driven snapshot schedule for a resource. Snapshots are taken on every "+
			"diskful node and pruned by a grandfather-father-son retention policy. One schedule per resource. "+
			"Set at least one keep_* count."),
		func(ctx context.Context, _ *mcp.CallToolRequest, in scheduleCreateIn) (*mcp.CallToolResult, opResult, error) {
			keep := &sdspb.GFSRetention{
				Hourly: in.Hourly, Daily: in.Daily, Weekly: in.Weekly, Monthly: in.Monthly, Yearly: in.Yearly,
			}
			if err := s.client.CreateSnapshotSchedule(ctx, in.Resource, in.Cron, keep, !in.Disabled); err != nil {
				return nil, opResult{}, err
			}
			return nil, ok(fmt.Sprintf("snapshot schedule for %s created (cron %q)", in.Resource, in.Cron)), nil
		})

	addRead(s, srv, readOnlyTool("sds_snapshot_schedule_list", "List snapshot schedules",
		"List all cron-driven snapshot schedules with their retention policy, last run and next run time."),
		func(ctx context.Context, _ *mcp.CallToolRequest, _ struct{}) (*mcp.CallToolResult, scheduleListOut, error) {
			schedules, err := s.client.ListSnapshotSchedules(ctx)
			if err != nil {
				return nil, scheduleListOut{}, err
			}
			out := scheduleListOut{Schedules: make([]scheduleOut, 0, len(schedules))}
			for _, sc := range schedules {
				k := sc.Keep
				out.Schedules = append(out.Schedules, scheduleOut{
					Name:     sc.Name,
					Resource: sc.Resource,
					Cron:     sc.Cron,
					Enabled:  sc.Enabled,
					Keep: fmt.Sprintf("hourly=%d daily=%d weekly=%d monthly=%d yearly=%d",
						k.GetHourly(), k.GetDaily(), k.GetWeekly(), k.GetMonthly(), k.GetYearly()),
					LastRun: sc.LastRun,
					NextRun: sc.NextRun,
				})
			}
			return nil, out, nil
		})

	addWrite(s, srv, destructiveTool("sds_snapshot_schedule_delete", "Delete snapshot schedule",
		"Delete a snapshot schedule by name. Existing snapshots are kept; only future scheduled snapshots stop."),
		func(ctx context.Context, _ *mcp.CallToolRequest, in scheduleNameIn) (*mcp.CallToolResult, opResult, error) {
			if err := s.client.DeleteSnapshotSchedule(ctx, in.Name); err != nil {
				return nil, opResult{}, err
			}
			return nil, ok(fmt.Sprintf("snapshot schedule %s deleted", in.Name)), nil
		})
}
