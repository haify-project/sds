package controller

import (
	"context"
	"fmt"

	"go.uber.org/zap"

	"github.com/haify-project/sds/pkg/apptemplate"
)

// syncAppPlacement keeps an app's unit and promoter on exactly the diskful
// replicas of its resource (see promoter_placement.go for why that set).
//
// Unlike an HA config, an app's files are not copied from a node: they are
// generated again from the record, which holds the binaries, version and ids
// every node agreed on at creation. A new replica must agree with them too —
// the same engine in the same place, the same version, the same uid and gid —
// or a failover to it would start a different database, or none, over the
// data; it gets nothing and the error says why.
//
// Hosts that already hold the files are left alone: rewriting the config on
// the node running the chain and reloading drbd-reactor restarts the app.
func (rm *ResourceManager) syncAppPlacement(ctx context.Context, resource string, run, former []string) error {
	if rm.controller.db == nil {
		return nil
	}
	app, err := rm.controller.db.GetAppByResource(ctx, resource)
	if err != nil || app == nil {
		return err
	}
	am := rm.controller.appManager()
	spec, bins := appSpec(app), appBinaries(app)

	res, err := am.runAppScript(ctx, run, apptemplate.PresenceScript(app.Name))
	if err != nil {
		return fmt.Errorf("look for app %s on the replicas of %s: %w", app.Name, resource, err)
	}
	var missing []string
	for _, h := range run {
		out, ok := appHostOutput(res, h)
		if !ok {
			rm.controller.logger.Warn("Replica did not answer; its app files are left as they are",
				zap.String("app", app.Name), zap.String("host", h))
			continue
		}
		kv := keyValues(out)
		if kv["promoter"] != "yes" || kv["unit"] != "yes" {
			missing = append(missing, h)
		}
	}
	if len(missing) > 0 {
		ref := apptemplate.NodeProbe{Binaries: bins, Version: app.Version, UID: app.UID, GID: app.GID}
		if _, err := am.checkPrereqs(ctx, spec, missing, &ref); err != nil {
			return fmt.Errorf("app %s is not placed on %s: %w", app.Name, rm.nodeLabels(missing), err)
		}
		if err := am.install(ctx, spec, bins, app.Device, missing); err != nil {
			return fmt.Errorf("place app %s on %s: %w", app.Name, rm.nodeLabels(missing), err)
		}
		rm.controller.logger.Info("App placed on new replicas", zap.String("app", app.Name),
			zap.Strings("hosts", missing))
	}

	var rest []string
	for _, h := range dedupe(append(rm.allNodeAddresses(), rm.resolveAll(former)...)) {
		if !contains(run, h) {
			rest = append(rest, h)
		}
	}
	if len(rest) > 0 {
		if res, err := am.runAppScript(ctx, rest, apptemplate.RemoveScript(spec)); err != nil || !res.AllSuccess() {
			rm.controller.logger.Warn("Could not retire app files from nodes that are not replicas",
				zap.String("app", app.Name), zap.Strings("hosts", rest), zap.Error(err))
		}
	}
	return nil
}
