package controller

import (
	"context"
	"fmt"
	"strings"
	"time"

	"go.uber.org/zap"

	"github.com/haify-project/sds/pkg/event"
)

// Moving a replica.
//
// A replica moves by adding the new one, waiting for it to be UpToDate, and
// only then removing the old one: the resource has one copy more for the
// duration, never one fewer. The removal is recorded on the resource
// (MoveFrom/MoveTo) before the add, so a controller that restarts or fails
// over mid-sync picks the move up again (resumeMoves) instead of leaving the
// extra replica behind.

const (
	movePollInterval = 15 * time.Second
	moveTimeout      = 48 * time.Hour
)

// MoveReplica starts moving resource's replica from one node to another.
// It returns once the new replica is added; the old one goes when the new
// one is in sync.
func (rm *ResourceManager) MoveReplica(ctx context.Context, resource, from, to string) error {
	dbRes, err := rm.controller.db.GetResource(ctx, resource)
	if err != nil || dbRes == nil {
		return fmt.Errorf("resource %q not found", resource)
	}
	switch {
	case dbRes.MoveFrom != "":
		return fmt.Errorf("%s is already moving its replica from %s to %s", resource, dbRes.MoveFrom, dbRes.MoveTo)
	case !contains(splitCSV(dbRes.Nodes), from):
		return fmt.Errorf("%s holds no replica of %s", from, resource)
	case dbRes.WANMode && from == dbRes.DRNode:
		return fmt.Errorf("%s is the DR node of %s; moving it is `resource remove-dr` and `add-dr`", from, resource)
	case resource == SelfHaResource:
		return fmt.Errorf("the controller's own metadata is moved with `ha self`, not here")
	}
	if info, err := rm.GetResource(ctx, resource); err == nil {
		for node, st := range info.NodeStates {
			if strings.EqualFold(st.Role, "Primary") && (node == from || rm.controller.ResolveHost(node) == rm.controller.ResolveHost(from)) {
				return fmt.Errorf("%s is Primary on %s; move the role first (`sds node drain %s`)", resource, from, from)
			}
		}
	}
	dbRes.MoveFrom, dbRes.MoveTo = from, to
	if err := rm.controller.db.SaveResource(ctx, dbRes); err != nil {
		return err
	}
	if err := rm.AddReplica(ctx, resource, to); err != nil {
		rm.clearMove(context.WithoutCancel(ctx), resource)
		return err
	}
	go rm.finishMove(rm.controller.ctx, resource, from, to)
	return nil
}

// resumeMoves picks up moves a previous controller left half-done.
func (rm *ResourceManager) resumeMoves(ctx context.Context) {
	if rm.controller.db == nil {
		return
	}
	resources, err := rm.controller.db.ListResources(ctx)
	if err != nil {
		return
	}
	for _, r := range resources {
		if r.MoveFrom != "" {
			go rm.finishMove(ctx, r.Name, r.MoveFrom, r.MoveTo)
		}
	}
}

// finishMove waits for to's replica to be UpToDate, then removes from's.
func (rm *ResourceManager) finishMove(ctx context.Context, resource, from, to string) {
	deadline := time.Now().Add(moveTimeout)
	for time.Now().Before(deadline) {
		select {
		case <-ctx.Done():
			return
		case <-time.After(movePollInterval):
		}
		if !rm.replicaUpToDate(ctx, resource, to) {
			continue
		}
		err := rm.RemoveReplica(ctx, resource, from)
		if err != nil {
			rm.moveEvent(resource, event.SeverityWarning, fmt.Sprintf("the new replica of %s on %s is in sync, but removing "+
				"the one on %s failed: %v; remove it with `sds resource remove-replica %s --node %s`", resource, to, from, err, resource, from))
		} else {
			rm.moveEvent(resource, event.SeverityInfo, fmt.Sprintf("the replica of %s moved from %s to %s", resource, from, to))
		}
		rm.clearMove(ctx, resource)
		return
	}
	rm.moveEvent(resource, event.SeverityWarning, fmt.Sprintf("the new replica of %s on %s did not reach UpToDate in %s; "+
		"the one on %s is kept", resource, to, moveTimeout, from))
	rm.clearMove(ctx, resource)
}

func (rm *ResourceManager) replicaUpToDate(ctx context.Context, resource, node string) bool {
	info, err := rm.GetResource(ctx, resource)
	if err != nil {
		return false
	}
	addr := rm.controller.ResolveHost(node)
	for name, st := range info.NodeStates {
		if (name == node || rm.controller.ResolveHost(name) == addr) && st.DiskState == "UpToDate" {
			return true
		}
	}
	return false
}

func (rm *ResourceManager) clearMove(ctx context.Context, resource string) {
	if r, err := rm.controller.db.GetResource(ctx, resource); err == nil && r != nil {
		r.MoveFrom, r.MoveTo = "", ""
		if err := rm.controller.db.SaveResource(ctx, r); err != nil {
			rm.controller.logger.Warn("Could not clear a finished move", zap.String("resource", resource), zap.Error(err))
		}
	}
}

func (rm *ResourceManager) moveEvent(resource string, sev event.Severity, msg string) {
	rm.controller.logger.Info(msg)
	if rm.controller.events != nil {
		rm.controller.events.Publish(event.Event{Type: event.TypeReplicaMoved, Severity: sev, Status: event.StatusInfo,
			Resource: resource, Message: msg})
	}
}
