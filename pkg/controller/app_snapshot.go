package controller

import (
	"context"
	"fmt"
	"time"

	"go.uber.org/zap"

	"github.com/haify-project/sds/pkg/apptemplate"
)

// Snapshot takes a resource snapshot of the app's resource — every volume on
// every diskful replica (resource_snapshot.go) — with the database frozen on
// its node for the duration, so the snapshot holds a cleanly flushed
// database rather than a crash image (see pkg/apptemplate/freeze.go for what
// freezing means per engine).
//
// The freeze script arms a transient timer on the node before it freezes
// anything, which thaws the database after apptemplate.FreezeWatchdog however
// the controller fares; the thaw here disarms it. An app that is not running
// has nothing to freeze, and its resource is snapshotted as it is. frozen
// reports which happened.
func (am *AppManager) Snapshot(ctx context.Context, name, snapshot string) (bool, error) {
	if !resourceSnapshotNameRe.MatchString(snapshot) {
		return false, fmt.Errorf("%q is not a valid snapshot name (letters, digits, _ and -, at most 40)", snapshot)
	}
	app, err := am.get(ctx, name)
	if err != nil {
		return false, err
	}
	appOps.Lock()
	defer appOps.Unlock()
	hosts, err := am.rm().failoverHosts(ctx, app.Resource)
	if err != nil {
		return false, err
	}
	primary, err := am.primaryOf(ctx, app.Resource, hosts)
	if err != nil {
		return false, err
	}
	if primary == "" {
		if err := am.rm().CreateResourceSnapshot(ctx, app.Resource, snapshot); err != nil {
			return false, err
		}
		return false, nil
	}

	spec, bins := appSpec(app), appBinaries(app)
	node := am.nodeName(primary)
	res, err := am.runAppScript(ctx, []string{primary}, apptemplate.FreezeForSnapshot(spec, bins))
	if err != nil {
		return false, fmt.Errorf("freeze %s on %s: %w", name, node, err)
	}
	if _, ok := appHostOutput(res, primary); !ok {
		return false, fmt.Errorf("freeze %s on %s (nothing was snapshotted; the app runs on): %s",
			name, node, hostFailure(res.Hosts[primary]))
	}
	am.c.logger.Info("App frozen for a snapshot", zap.String("app", name), zap.String("node", node),
		zap.String("snapshot", snapshot))
	defer am.thaw(name, primary, spec, bins)

	return true, am.rm().CreateResourceSnapshot(ctx, app.Resource, snapshot)
}

// thaw runs on a context of its own, so a cancelled request still thaws the
// database at once rather than at the watchdog.
func (am *AppManager) thaw(name, host string, spec apptemplate.Spec, bins apptemplate.Binaries) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	res, err := am.runAppScript(ctx, []string{host}, apptemplate.ThawAfterSnapshot(spec, bins))
	if err == nil {
		if _, ok := appHostOutput(res, host); ok {
			return
		}
		err = fmt.Errorf("%s", hostFailure(res.Hosts[host]))
	}
	am.c.logger.Warn("Thawing an app after its snapshot failed; the node's watchdog thaws it",
		zap.String("app", name), zap.String("node", am.nodeName(host)),
		zap.Duration("within", apptemplate.FreezeWatchdog), zap.Error(err))
}
