package controller

import (
	"context"
	"fmt"
	"path"
	"strings"
	"time"

	"go.uber.org/zap"

	"github.com/haify-project/sds/pkg/apptemplate"
	"github.com/haify-project/sds/pkg/database"
)

// AppCreated is what creating an app reports. Password is set only when this
// call generated it; it is not kept anywhere but on the volume.
type AppCreated struct {
	App      *database.App
	Password string
	Primary  string // the node it was initialized on
	Reused   bool   // the volume already held this app's data
}

// appMinReplicas is the fewest diskful replicas an app runs on: with one
// there is nothing to fail over to.
const appMinReplicas = 2

// Create makes an app on an existing resource:
//
//  1. refuse anything that would put a second promoter on the resource, and
//     a resource with fewer than two diskful replicas;
//  2. probe every diskful node (agents, engine, versions, uid/gid, port);
//  3. promote the resource on one of them, stage the password there, and run
//     the initialization script (mkfs if blank, initdb, smoke test, unmount);
//  4. demote it, record the app, and install the unit and the promoter on
//     every diskful node, after which drbd-reactor starts it.
//
// Nothing is written to any node before step 3, so a refusal leaves the
// cluster as it was.
func (am *AppManager) Create(ctx context.Context, spec apptemplate.Spec) (*AppCreated, error) {
	if err := spec.Normalize(); err != nil {
		return nil, err
	}
	if err := am.ready(); err != nil {
		return nil, err
	}
	appOps.Lock()
	defer appOps.Unlock()
	logger := am.c.logger.With(zap.String("app", spec.Name), zap.String("resource", spec.Resource))

	if err := am.checkCreatable(ctx, spec); err != nil {
		return nil, err
	}
	hosts, err := am.rm().failoverHosts(ctx, spec.Resource)
	if err != nil {
		return nil, err
	}
	if len(hosts) < appMinReplicas {
		return nil, fmt.Errorf("resource %s has %d diskful replica(s) a promoter can run on; an app needs at least %d "+
			"(add one with sds resource add-replica)", spec.Resource, len(hosts), appMinReplicas)
	}
	device, err := am.dataDevice(ctx, spec.Resource)
	if err != nil {
		return nil, err
	}
	if err := am.checkNoForeignPromoter(ctx, spec, hosts); err != nil {
		return nil, err
	}
	agreed, err := am.checkPrereqs(ctx, spec, hosts, nil)
	if err != nil {
		return nil, err
	}

	primary, promoted, err := am.firstPrimary(ctx, spec.Resource, hosts)
	if err != nil {
		return nil, err
	}
	logger.Info("Initializing app", zap.String("engine", string(spec.Engine)), zap.String("node", am.nodeName(primary)))
	password, err := generatePassword()
	if err != nil {
		return nil, err
	}
	out, initErr := am.initialize(ctx, primary, spec, agreed.Binaries, device, password)
	// The promoter can take a resource only when no node holds it: demote it
	// once the volume is set up — or when initialization failed and the
	// promotion was ours. A Primary found already in place and refused by the
	// init (mounted elsewhere, say) belongs to whoever made it so.
	if promoted || initErr == nil {
		if err := am.rm().SetSecondary(ctx, spec.Resource, primary); err != nil {
			if initErr != nil {
				return nil, fmt.Errorf("%w (and demoting %s afterwards failed: %v)", initErr, spec.Resource, err)
			}
			return nil, fmt.Errorf("the app was initialized on %s, but demoting %s there failed, so drbd-reactor cannot "+
				"take it over: %w", am.nodeName(primary), spec.Resource, err)
		}
	}
	if initErr != nil {
		return nil, initErr
	}
	reused := keyValues(out)["state"] == "reused"

	app := &database.App{
		Name: spec.Name, Engine: string(spec.Engine), Resource: spec.Resource, ServiceIP: spec.ServiceIP,
		Port: spec.Port, Vector: spec.Vector, Device: device,
		Server: agreed.Binaries.Server, Client: agreed.Binaries.Client, Admin: agreed.Binaries.Admin,
		Init: agreed.Binaries.Init, Flavor: agreed.Binaries.Flavor, Version: agreed.Version,
		UID: agreed.UID, GID: agreed.GID,
	}
	// Recorded before the promoter exists, so a failure from here on leaves an
	// app `sds app delete` can find and clean up.
	if err := am.c.db.SaveApp(ctx, app); err != nil {
		return nil, fmt.Errorf("record the app: %w", err)
	}
	if err := am.install(ctx, spec, agreed.Binaries, device, hosts); err != nil {
		return nil, fmt.Errorf("%w; the app is recorded, so `sds app delete %s` removes what was installed", err, spec.Name)
	}
	logger.Info("App created", zap.Bool("data_reused", reused), zap.Strings("hosts", hosts))

	created := &AppCreated{App: app, Primary: am.nodeName(primary), Reused: reused}
	if !reused {
		created.Password = password
	}
	return created, nil
}

// checkCreatable refuses an app whose name is taken, whose resource does not
// exist, or whose resource already has a promoter of any kind: drbd-reactor
// runs one promoter per resource, and two would fight over its Primary.
func (am *AppManager) checkCreatable(ctx context.Context, spec apptemplate.Spec) error {
	db := am.c.db
	if existing, err := db.GetApp(ctx, spec.Name); err != nil {
		return err
	} else if existing != nil {
		return fmt.Errorf("app %s already exists (on resource %s)", spec.Name, existing.Resource)
	}
	if spec.Resource == SelfHaResource {
		return fmt.Errorf("%s is the controller's own metadata resource; an app needs a resource of its own", spec.Resource)
	}
	res, err := db.GetResource(ctx, spec.Resource)
	if err != nil || res == nil {
		return fmt.Errorf("resource %s not found; create it first (sds resource create --name %s --size ... --nodes ...)",
			spec.Resource, spec.Resource)
	}
	if other, err := db.GetAppByResource(ctx, spec.Resource); err != nil {
		return err
	} else if other != nil {
		return fmt.Errorf("resource %s already runs the app %s", spec.Resource, other.Name)
	}
	if ha, err := db.GetHaConfig(ctx, spec.Resource); err == nil && ha != nil {
		return fmt.Errorf("resource %s already has an HA config (sds ha create); remove it first (sds ha delete %s)",
			spec.Resource, spec.Resource)
	}
	if gw, err := db.GetGatewayByResource(ctx, spec.Resource); err == nil && gw != nil {
		return fmt.Errorf("resource %s already exports a gateway; delete it first (sds gateway delete %s)",
			spec.Resource, spec.Resource)
	}
	return am.c.assertPromoterAllowed(ctx, spec.Resource, "an app")
}

// checkNoForeignPromoter looks on the nodes themselves for a promoter the
// database does not know about: an HA config or gateway made by hand or left
// behind. Its own config left by an earlier, failed create is not foreign.
func (am *AppManager) checkNoForeignPromoter(ctx context.Context, spec apptemplate.Spec, hosts []string) error {
	own := path.Base(apptemplate.PromoterPath(spec.Name))
	script := fmt.Sprintf(`for f in %[1]s/sds-*-%[2]s.toml %[1]s/sds-*-%[2]s.toml.disabled; do
  [ -e "$f" ] || continue
  case "$(basename "$f")" in %[3]s|%[3]s.disabled) continue ;; esac
  echo "promoter=$f"
done
true
`, apptemplate.ReactorConfigDir, spec.Resource, own)
	res, err := am.runAppScript(ctx, hosts, script)
	if err != nil {
		return fmt.Errorf("look for existing promoters of %s: %w", spec.Resource, err)
	}
	var found []string
	for _, h := range hosts {
		out, ok := appHostOutput(res, h)
		if !ok {
			return fmt.Errorf("%s did not answer when looking for existing promoters of %s", am.nodeName(h), spec.Resource)
		}
		for _, line := range strings.Split(out, "\n") {
			if f, ok := strings.CutPrefix(strings.TrimSpace(line), "promoter="); ok {
				found = append(found, am.nodeName(h)+":"+f)
			}
		}
	}
	if len(found) > 0 {
		return fmt.Errorf("resource %s already has a drbd-reactor promoter (%s); a resource runs one promoter, so remove "+
			"that one first", spec.Resource, strings.Join(found, ", "))
	}
	return nil
}

// checkPrereqs probes hosts and requires them to agree with each other and,
// when ref is given (a replica joining an existing app), with what the app
// was created with.
func (am *AppManager) checkPrereqs(ctx context.Context, spec apptemplate.Spec, hosts []string, ref *apptemplate.NodeProbe) (apptemplate.Agreed, error) {
	res, err := am.runAppScript(ctx, hosts, apptemplate.ProbeScript(spec))
	if err != nil {
		return apptemplate.Agreed{}, fmt.Errorf("probe the nodes: %w", err)
	}
	var nodes []apptemplate.NodeResult
	if ref != nil {
		nodes = append(nodes, apptemplate.NodeResult{Node: "the app as created", Probe: *ref})
	}
	for _, h := range hosts {
		out, ok := appHostOutput(res, h)
		if !ok {
			return apptemplate.Agreed{}, fmt.Errorf("%s could not be probed: %s", am.nodeName(h), hostFailure(res.Hosts[h]))
		}
		nodes = append(nodes, apptemplate.NodeResult{Node: am.nodeName(h), Probe: apptemplate.ParseProbe(out)})
	}
	return apptemplate.CheckNodes(spec, nodes)
}

// dataDevice is the DRBD device of the resource's first volume.
func (am *AppManager) dataDevice(ctx context.Context, resource string) (string, error) {
	vols, err := am.c.db.ListVolumes(ctx, resource)
	if err != nil {
		return "", fmt.Errorf("read the volumes of %s: %w", resource, err)
	}
	if len(vols) == 0 {
		return "", fmt.Errorf("resource %s has no volumes recorded", resource)
	}
	first := vols[0]
	for _, v := range vols[1:] {
		if v.VolumeID < first.VolumeID {
			first = v
		}
	}
	return apptemplate.DataDevice(resource, first.VolumeID), nil
}

// firstPrimary picks the node to initialize on: the one already Primary, or
// the first diskful replica, promoted now. promoted says which.
func (am *AppManager) firstPrimary(ctx context.Context, resource string, hosts []string) (string, bool, error) {
	primary, err := am.primaryOf(ctx, resource, hosts)
	if err != nil {
		return "", false, err
	}
	if primary != "" {
		return primary, false, nil
	}
	if err := am.rm().SetPrimary(ctx, resource, hosts[0], false); err != nil {
		return "", false, fmt.Errorf("promote %s on %s to initialize it: %w", resource, am.nodeName(hosts[0]), err)
	}
	return hosts[0], true, nil
}

// initialize stages the password on host and runs the initialization script
// there. The staged copy is removed by the script, and again here in case the
// script never ran.
func (am *AppManager) initialize(ctx context.Context, host string, spec apptemplate.Spec, bins apptemplate.Binaries, device, password string) (string, error) {
	rel := ".sds-app/" + spec.Name + ".pw"
	staged, err := am.stagePassword(ctx, host, rel, password)
	defer func() {
		cctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		_, _ = am.rm().deployment.Exec(cctx, []string{host}, fmt.Sprintf(`rm -f "$HOME/%s"`, rel))
	}()
	if err != nil {
		return "", err
	}
	res, err := am.runAppScript(ctx, []string{host}, apptemplate.InitScript(spec, bins, device, staged))
	if err != nil {
		return "", fmt.Errorf("initialize %s on %s: %w", spec.Name, am.nodeName(host), err)
	}
	out, ok := appHostOutput(res, host)
	if !ok {
		return "", fmt.Errorf("initialize %s on %s: %s", spec.Name, am.nodeName(host), hostFailure(res.Hosts[host]))
	}
	return out, nil
}

// stagePassword copies the password to host over the SSH stream into a 0600
// file in a 0700 directory of the login user's home, and returns its
// absolute path. It never appears in a command line.
func (am *AppManager) stagePassword(ctx context.Context, host, rel, password string) (string, error) {
	dir := path.Dir(rel)
	res, err := am.rm().deployment.Exec(ctx, []string{host},
		fmt.Sprintf(`mkdir -p "$HOME/%[1]s" && chmod 0700 "$HOME/%[1]s" && echo "SDS_HOME=$HOME"`, dir))
	if err != nil {
		return "", fmt.Errorf("prepare %s on %s: %w", dir, am.nodeName(host), err)
	}
	out, ok := appHostOutput(res, host)
	home := keyValues(out)["SDS_HOME"]
	if !ok || !strings.HasPrefix(home, "/") {
		return "", fmt.Errorf("prepare %s on %s: %s", dir, am.nodeName(host), hostFailure(res.Hosts[host]))
	}
	put, err := am.rm().deployment.DistributeSecret(ctx, []string{host}, password, rel)
	if err != nil {
		return "", fmt.Errorf("copy the generated password to %s: %w", am.nodeName(host), err)
	}
	if put == nil || !put.Success || len(put.FailedHosts()) > 0 {
		return "", fmt.Errorf("copy the generated password to %s failed", am.nodeName(host))
	}
	return path.Join(home, rel), nil
}

// install writes the unit and then the promoter config to hosts and reloads
// drbd-reactor there, which starts the app on whichever node promotes first.
// The unit goes first: a promoter naming a unit systemd does not know fails.
func (am *AppManager) install(ctx context.Context, spec apptemplate.Spec, bins apptemplate.Binaries, device string, hosts []string) error {
	cfg, err := apptemplate.PromoterConfig(spec, device)
	if err != nil {
		return err
	}
	dep := am.rm().deployment
	unitRes, err := dep.DistributeConfig(ctx, hosts, apptemplate.Unit(spec, bins), apptemplate.UnitPath(spec.Name))
	if err != nil || (unitRes != nil && len(unitRes.FailedHosts()) > 0) {
		return fmt.Errorf("install %s: %v", apptemplate.UnitName(spec.Name), installFailure(unitRes, err))
	}
	if err := am.rm().execAllSuccess(ctx, hosts, "sudo systemctl daemon-reload", "reload systemd"); err != nil {
		return err
	}
	cfgRes, err := dep.DistributeConfig(ctx, hosts, cfg, apptemplate.PromoterPath(spec.Name))
	if err != nil || (cfgRes != nil && len(cfgRes.FailedHosts()) > 0) {
		return fmt.Errorf("install the promoter config: %v", installFailure(cfgRes, err))
	}
	if res, err := am.runAppScript(ctx, hosts, reloadReactorScript); err != nil || !res.AllSuccess() {
		am.c.logger.Warn("drbd-reactor reload failed after installing an app", zap.String("app", spec.Name),
			zap.Error(err))
	}
	return nil
}

func installFailure(res interface{ FailedHosts() []string }, err error) string {
	if err != nil {
		return err.Error()
	}
	return fmt.Sprintf("failed on %v", res.FailedHosts())
}
