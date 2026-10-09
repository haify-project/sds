package controller

import (
	"context"
	"fmt"
	"strings"

	"go.uber.org/zap"

	"github.com/haify-project/haify/pkg/apptemplate"
	"github.com/haify-project/haify/pkg/database"
)

// List returns every app.
func (am *AppManager) List(ctx context.Context) ([]*database.App, error) {
	if am.c.db == nil {
		return nil, fmt.Errorf("database not available")
	}
	return am.c.db.ListApps(ctx)
}

// AppStatus is where an app runs and whether it answers there.
type AppStatus struct {
	App          *database.App
	Primary      string   // node name; empty when the resource is Primary nowhere
	ServiceState string   // systemctl is-active of the unit on the Primary
	Healthy      bool     // the engine's health probe answered
	State        string   // running, degraded or stopped
	Nodes        []string // node names the promoter runs on
}

// App states. degraded means the resource is Primary somewhere but the
// database there is not active or does not answer: starting up, recovering,
// or failing (in which case drbd-reactor moves it).
const (
	AppRunning  = "running"
	AppDegraded = "degraded"
	AppStopped  = "stopped"
)

// Status finds the node where the app's resource is Primary and asks the
// database there whether it answers.
func (am *AppManager) Status(ctx context.Context, name string) (*AppStatus, error) {
	app, err := am.get(ctx, name)
	if err != nil {
		return nil, err
	}
	hosts, err := am.rm().failoverHosts(ctx, app.Resource)
	if err != nil {
		return nil, err
	}
	st := &AppStatus{App: app, State: AppStopped}
	for _, h := range hosts {
		st.Nodes = append(st.Nodes, am.nodeName(h))
	}
	primary, err := am.primaryOf(ctx, app.Resource, hosts)
	if err != nil {
		return nil, err
	}
	if primary == "" {
		return st, nil
	}
	st.Primary = am.nodeName(primary)
	res, err := am.runAppScript(ctx, []string{primary}, apptemplate.StatusScript(appSpec(app), appBinaries(app)))
	if err != nil {
		return nil, fmt.Errorf("ask %s about %s: %w", st.Primary, name, err)
	}
	out, _ := appHostOutput(res, primary)
	kv := keyValues(out)
	st.ServiceState = kv["active"]
	st.Healthy = kv["health"] == "ok"
	st.State = AppDegraded
	if st.ServiceState == "active" && st.Healthy {
		st.State = AppRunning
	}
	return st, nil
}

// Failover moves the app to another replica through drbd-reactor and returns
// the node it left and the node that took over.
func (am *AppManager) Failover(ctx context.Context, name string) (string, string, error) {
	app, err := am.get(ctx, name)
	if err != nil {
		return "", "", err
	}
	appOps.Lock()
	defer appOps.Unlock()
	hosts, err := am.rm().failoverHosts(ctx, app.Resource)
	if err != nil {
		return "", "", err
	}
	if len(hosts) < 2 {
		return "", "", fmt.Errorf("resource %s has no other diskful replica to fail over to", app.Resource)
	}
	primary, err := am.primaryOf(ctx, app.Resource, hosts)
	if err != nil {
		return "", "", err
	}
	if primary == "" {
		return "", "", fmt.Errorf("app %s is not running anywhere (%s is Primary on no node); drbd-reactor starts it "+
			"wherever it can, see haify app status %s", name, app.Resource, name)
	}
	from := am.nodeName(primary)
	res, err := am.runAppScript(ctx, []string{primary}, apptemplate.EvictScript(name))
	if err != nil {
		return from, "", fmt.Errorf("evict %s from %s: %w", name, from, err)
	}
	out, ok := appHostOutput(res, primary)
	if !ok {
		return from, "", fmt.Errorf("evict %s from %s: %s", name, from, hostFailure(res.Hosts[primary]))
	}
	to := apptemplate.TookOver(out)
	am.c.logger.Info("App failed over", zap.String("app", name), zap.String("from", from), zap.String("to", to))
	return from, to, nil
}

// Delete takes the app off every node and drops its record. The resource and
// the data on it stay unless deleteData, in which case the resource is
// deleted afterwards — a step of its own, so a failure there leaves an app
// that is already gone rather than half of one.
func (am *AppManager) Delete(ctx context.Context, name string, deleteData bool) (string, error) {
	app, err := am.get(ctx, name)
	if err != nil {
		return "", err
	}
	if err := am.teardown(ctx, app); err != nil {
		return "", err
	}
	msg := fmt.Sprintf("app %s deleted; resource %s and its data are kept", name, app.Resource)
	if !deleteData {
		return msg, nil
	}
	if err := am.rm().DeleteResource(ctx, app.Resource, false); err != nil {
		return "", fmt.Errorf("app %s deleted, but deleting resource %s failed: %w", name, app.Resource, err)
	}
	return fmt.Sprintf("app %s and resource %s deleted", name, app.Resource), nil
}

// teardown removes the app from its nodes and the database. Every replica
// must answer: one that keeps the promoter config would start the app again
// the next time it can promote the resource. Other nodes are cleaned too, in
// case a replica was removed since, but only as a courtesy.
func (am *AppManager) teardown(ctx context.Context, app *database.App) error {
	appOps.Lock()
	defer appOps.Unlock()
	replicas, err := am.rm().failoverHosts(ctx, app.Resource)
	if err != nil {
		// The resource may be gone already; clean wherever the app could be.
		replicas = nil
	}
	others := withoutAll(dedupe(am.rm().allNodeAddresses()), replicas)

	script := apptemplate.RemoveScript(appSpec(app))
	if len(replicas) > 0 {
		res, err := am.runAppScript(ctx, replicas, script)
		if err != nil {
			return fmt.Errorf("remove app %s from its nodes: %w", app.Name, err)
		}
		var failed []string
		for _, h := range replicas {
			if _, ok := appHostOutput(res, h); !ok {
				failed = append(failed, fmt.Sprintf("%s: %s", am.nodeName(h), hostFailure(res.Hosts[h])))
			}
		}
		if len(failed) > 0 {
			return fmt.Errorf("app %s could not be removed everywhere, so it is kept (run the delete again once "+
				"they answer): %s", app.Name, strings.Join(failed, "; "))
		}
	}
	if len(others) > 0 {
		if res, err := am.runAppScript(ctx, others, script); err != nil || !res.AllSuccess() {
			am.c.logger.Warn("Could not clean up app files on nodes that are not replicas",
				zap.String("app", app.Name), zap.Error(err))
		}
	}
	if err := am.c.db.DeleteApp(ctx, app.Name); err != nil {
		return fmt.Errorf("drop the record of app %s: %w", app.Name, err)
	}
	am.c.logger.Info("App deleted", zap.String("app", app.Name), zap.String("resource", app.Resource))
	return nil
}

// dedupe drops empty and repeated entries, keeping the first of each.
func dedupe(list []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, v := range list {
		if v != "" && !seen[v] {
			seen[v] = true
			out = append(out, v)
		}
	}
	return out
}
