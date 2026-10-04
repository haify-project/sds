package controller

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"strings"
	"sync"

	sdspb "github.com/haify-project/sds/api/proto/v1"
	"github.com/haify-project/sds/pkg/apptemplate"
	"github.com/haify-project/sds/pkg/database"
	"github.com/haify-project/sds/pkg/deployment"
)

// Database applications (`sds app`).
//
// An app is a single-instance database on one resource's DRBD volume, run by
// a drbd-reactor promoter the way a gateway is: on the node where the
// resource is Primary the promoter mounts the volume, starts
// sds-app-<name>.service and raises the service IP, and when that node fails
// another replica does the same with every write DRBD acknowledged. It is
// storage failover, not database replication.
//
// pkg/apptemplate generates every file and script; the code here decides
// where they run. The files:
//
//	app.go            shared helpers: the record, hosts, the Primary
//	app_create.go     prerequisites, first initialization, installation
//	app_ops.go        list, status, failover, delete
//	app_snapshot.go   freeze, resource snapshot, thaw
//	app_placement.go  keeping the promoter on exactly the diskful replicas

// appOps serializes the operations that change an app. They are rare and
// slow (SSH rounds to every replica); two of them racing on one resource —
// a delete during a create, a snapshot during a failover — is the only way
// to get them wrong, so one lock for all apps costs nothing.
var appOps sync.Mutex

// AppManager runs the app operations. It holds no state of its own: the
// record is in the database and everything else is on the nodes.
type AppManager struct {
	c *Controller
}

// appManager returns the controller's app operations.
func (c *Controller) appManager() *AppManager { return &AppManager{c: c} }

func (am *AppManager) rm() *ResourceManager { return am.c.resources }

func (am *AppManager) ready() error {
	if am.c.db == nil {
		return fmt.Errorf("database not available")
	}
	if am.rm() == nil || am.rm().deployment == nil {
		return fmt.Errorf("deployment client not set")
	}
	return nil
}

// get loads an app's record, failing when there is none.
func (am *AppManager) get(ctx context.Context, name string) (*database.App, error) {
	if err := am.ready(); err != nil {
		return nil, err
	}
	app, err := am.c.db.GetApp(ctx, name)
	if err != nil {
		return nil, err
	}
	if app == nil {
		return nil, fmt.Errorf("app %q not found", name)
	}
	return app, nil
}

// appSpec and appBinaries rebuild what generated an app's files from its
// record, so the same files can be written again.
func appSpec(a *database.App) apptemplate.Spec {
	return apptemplate.Spec{Name: a.Name, Engine: apptemplate.Engine(a.Engine), Resource: a.Resource,
		ServiceIP: a.ServiceIP, Port: a.Port, Vector: a.Vector}
}

func appBinaries(a *database.App) apptemplate.Binaries {
	return apptemplate.Binaries{Server: a.Server, Client: a.Client, Admin: a.Admin, Init: a.Init, Flavor: a.Flavor}
}

// appInfo is an app as the API shows it.
func appInfo(a *database.App) *sdspb.AppInfo {
	spec := appSpec(a)
	l := apptemplate.LayoutFor(a.Name)
	return &sdspb.AppInfo{
		Name:            a.Name,
		Engine:          a.Engine,
		Resource:        a.Resource,
		ServiceIp:       a.ServiceIP,
		Port:            uint32(a.Port),
		Vector:          a.Vector,
		MountPoint:      l.Mount,
		Unit:            apptemplate.UnitName(a.Name),
		AdminUser:       apptemplate.AdminUser(spec.Engine),
		CredentialsFile: l.Password,
		Version:         a.Version,
		Connection:      apptemplate.ConnectionHint(spec),
		CreatedAtUnix:   a.CreatedAt.Unix(),
	}
}

// runAppScript runs a root script on hosts. It is base64-wrapped: dispatch
// runs commands through sh -c "...", whose quoting empties every $variable
// a plain command would carry.
func (am *AppManager) runAppScript(ctx context.Context, hosts []string, script string) (*deployment.ExecResult, error) {
	return am.rm().deployment.Exec(ctx, hosts, "echo "+base64Std(script)+" | base64 -d | sudo /bin/bash")
}

// appHostOutput is what host printed, and whether the script succeeded there.
func appHostOutput(res *deployment.ExecResult, host string) (string, bool) {
	if res == nil {
		return "", false
	}
	hr := res.Hosts[host]
	if hr == nil {
		return "", false
	}
	return hr.Output, hr.Success
}

// primaryHost is the host among hosts whose own role is Primary in the
// `drbdadm status` output res carries. Only the first line of each output is
// the node's own role; the lines below it are its peers'.
func primaryHost(res *deployment.ExecResult, hosts []string) string {
	for _, h := range hosts {
		out, ok := appHostOutput(res, h)
		if !ok {
			continue
		}
		first, _, _ := strings.Cut(strings.TrimSpace(out), "\n")
		if strings.Contains(first, "role:Primary") {
			return h
		}
	}
	return ""
}

// primaryOf is the host among hosts where resource is Primary, or "".
func (am *AppManager) primaryOf(ctx context.Context, resource string, hosts []string) (string, error) {
	res, err := am.rm().deployment.DRBDStatus(ctx, hosts, resource)
	if err != nil {
		return "", fmt.Errorf("read the DRBD status of %s: %w", resource, err)
	}
	return primaryHost(res, hosts), nil
}

// nodeName shows a host as its node name.
func (am *AppManager) nodeName(host string) string {
	if host == "" {
		return ""
	}
	return am.rm().nodeLabel(host)
}

// keyValues reads the key=value lines a node script prints.
func keyValues(out string) map[string]string {
	kv := map[string]string{}
	for _, line := range strings.Split(out, "\n") {
		if k, v, ok := strings.Cut(strings.TrimSpace(line), "="); ok && k != "" && !strings.ContainsAny(k, " \t") {
			kv[k] = strings.TrimSpace(v)
		}
	}
	return kv
}

// generatePassword returns 24 random bytes as unpadded base64url: 32
// characters from [A-Za-z0-9_-], safe unquoted in every config file and SQL
// string the engines are given.
func generatePassword() (string, error) {
	b := make([]byte, 24)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("generate a password: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// reloadReactorScript reloads drbd-reactor where it is installed.
const reloadReactorScript = "if systemctl cat drbd-reactor.service >/dev/null 2>&1; then " +
	"systemctl reload drbd-reactor || systemctl restart drbd-reactor; fi"
