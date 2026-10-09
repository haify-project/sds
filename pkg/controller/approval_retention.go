package controller

import (
	"context"
	"strings"

	"google.golang.org/protobuf/proto"

	haifypb "github.com/haify-project/haify/api/proto/v1"
	"github.com/haify-project/haify/pkg/database"
)

// Replacing a schedule is held back only when it weakens one.
//
// A snapshot or backup schedule prunes, on its next run, whatever its
// retention no longer keeps. Replacing a schedule with one that keeps fewer of
// any kind is therefore DeleteSnapshot or DeleteBackup by another name, and
// one stolen token could empty the history without a second person. Creating
// a schedule, or replacing one with the same or a longer retention, deletes
// nothing and runs at once.

// conditionalApproval holds the listed methods that need approval only when
// the call lowers what an existing schedule keeps.
var conditionalApproval = map[string]func(ctx context.Context, db *database.DB, req proto.Message) bool{
	"CreateSnapshotSchedule": func(ctx context.Context, db *database.DB, req proto.Message) bool {
		r, ok := req.(*haifypb.CreateSnapshotScheduleRequest)
		if !ok {
			return true
		}
		old, err := db.GetSnapshotSchedule(ctx, r.Resource)
		if err != nil {
			return !strings.Contains(err.Error(), "not found")
		}
		return keepsLess(gfsFromProto(r.Keep), old.Keep)
	},
	"CreateBackupSchedule": func(ctx context.Context, db *database.DB, req proto.Message) bool {
		r, ok := req.(*haifypb.CreateBackupScheduleRequest)
		if !ok {
			return true
		}
		old, err := db.GetBackupSchedule(ctx, database.BackupScheduleName(r.Resource, r.Target))
		if err != nil {
			return !strings.Contains(err.Error(), "not found")
		}
		return keepsLess(gfsFromProto(r.Keep), old.Keep)
	},
}

// keepsLess reports whether next keeps fewer of any kind than old.
func keepsLess(next, old database.GFSPolicy) bool {
	return next.Hourly < old.Hourly || next.Daily < old.Daily || next.Weekly < old.Weekly ||
		next.Monthly < old.Monthly || next.Yearly < old.Yearly
}
