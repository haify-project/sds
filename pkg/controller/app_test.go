package controller

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	sdspb "github.com/haify-project/sds/api/proto/v1"
	"github.com/haify-project/sds/pkg/apptemplate"
	"github.com/haify-project/sds/pkg/database"
	"github.com/haify-project/sds/pkg/deployment"
)

const appProbeOK = `server=/usr/lib/postgresql/16/bin/postgres
client=/usr/lib/postgresql/16/bin/psql
admin=/usr/lib/postgresql/16/bin/pg_isready
init=/usr/lib/postgresql/16/bin/initdb
version=postgres (PostgreSQL) 16.4
uid=113
gid=120
missing=
`

// appNodes is what the fake nodes answer to each kind of app script.
type appNodes struct {
	probe      map[string]string // host -> probe output (default appProbeOK)
	promoters  map[string]string // host -> foreign promoter lines
	initOut    string            // output of the init script (default: initialized)
	initFail   bool
	primary    string            // host where the resource is Primary ("" none)
	statusOut  string            // output of the status script
	evictOut   string            // output of the evict script
	removeFail map[string]bool   // hosts where the remove script fails
	presence   map[string]string // host -> presence script output
	scripts    []string          // every decoded script, in order
}

// appScriptKind names the generated script a command carries.
func appScriptKind(script string) string {
	switch {
	case strings.Contains(script, "sds app create: initialize"):
		return "init"
	case strings.Contains(script, `echo "missing=$missing"`):
		return "probe"
	case strings.Contains(script, `echo "promoter=$f"`):
		return "promoters"
	case strings.Contains(script, "drbd-reactorctl evict sds-app-"):
		return "evict"
	case strings.Contains(script, `echo "removed=$had"`):
		return "remove"
	case strings.Contains(script, `echo "frozen=yes"`):
		return "freeze"
	case strings.Contains(script, "systemctl stop sds-app-thaw-"):
		return "thaw"
	case strings.Contains(script, "health=ok"):
		return "status"
	case strings.Contains(script, `echo "promoter=$p"`):
		return "presence"
	}
	return "other"
}

func newAppTestController(t *testing.T, nodes *appNodes, resourceNodes string) (*Controller, *fakeDeploymentClient) {
	t.Helper()
	dep := &fakeDeploymentClient{}
	dep.execFunc = func(_ context.Context, hosts []string, cmd string, _ ...deployment.ExecOption) (*deployment.ExecResult, error) {
		res := successExecResult(hosts, "")
		if strings.Contains(cmd, "SDS_HOME=") {
			return successExecResult(hosts, "SDS_HOME=/home/sds\n"), nil
		}
		if !strings.HasPrefix(cmd, "echo ") || !strings.Contains(cmd, "| base64 -d |") {
			return res, nil
		}
		script := decodeB64Script(t, cmd)
		nodes.scripts = append(nodes.scripts, script)
		for _, h := range hosts {
			hr := res.Hosts[h]
			switch appScriptKind(script) {
			case "probe":
				hr.Output = appProbeOK
				if out, ok := nodes.probe[h]; ok {
					hr.Output = out
				}
			case "promoters":
				hr.Output = nodes.promoters[h]
			case "init":
				hr.Output = "formatted=yes\nstate=initialized\n"
				if nodes.initOut != "" {
					hr.Output = nodes.initOut
				}
				if nodes.initFail {
					hr.Success, hr.Output = false, "node1: initdb failed: disk full"
				}
			case "status":
				hr.Output = nodes.statusOut
			case "evict":
				hr.Output = nodes.evictOut
			case "remove":
				if nodes.removeFail[h] {
					hr.Success, hr.Output = false, "unreachable"
				}
			case "presence":
				hr.Output = "promoter=yes\nunit=yes\n"
				if out, ok := nodes.presence[h]; ok {
					hr.Output = out
				}
			}
		}
		return res, nil
	}
	dep.drbdStatusFunc = func(_ context.Context, hosts []string, resource string) (*deployment.ExecResult, error) {
		res := successExecResult(hosts, resource+" role:Secondary\n  disk:UpToDate\n  peer role:Primary\n")
		if hr, ok := res.Hosts[nodes.primary]; ok {
			hr.Output = resource + " role:Primary\n  disk:UpToDate\n"
		}
		return res, nil
	}
	ctrl := newBasicTestController(dep)
	ctrl.db = newTestDB(t)
	ctx := context.Background()
	require.NoError(t, ctrl.db.SaveResource(ctx, &database.Resource{Name: "res1", Nodes: resourceNodes, Port: 7100}))
	require.NoError(t, ctrl.db.SaveVolume(ctx, &database.Volume{ResourceName: "res1", VolumeName: "res1_data",
		VolumeID: 0, Pool: "vg0", SizeGB: 10}))
	return ctrl, dep
}

func appSpecFor(engine string) apptemplate.Spec {
	return apptemplate.Spec{Name: "orders", Engine: apptemplate.Engine(engine), Resource: "res1", ServiceIP: "10.0.0.50/24"}
}

func (n *appNodes) ran(kind string) int {
	count := 0
	for _, s := range n.scripts {
		if appScriptKind(s) == kind {
			count++
		}
	}
	return count
}

func TestCreateAppInitializesAndInstallsThePromoter(t *testing.T) {
	nodes := &appNodes{}
	ctrl, dep := newAppTestController(t, nodes, "node1,node2")
	var demoted []string
	dep.drbdSecondaryFunc = func(_ context.Context, host, _ string) (*deployment.HostResult, error) {
		demoted = append(demoted, host)
		return &deployment.HostResult{Host: host, Success: true}, nil
	}
	created, err := ctrl.appManager().Create(context.Background(), appSpecFor("postgres"))
	require.NoError(t, err)

	assert.Len(t, created.Password, 32)
	assert.False(t, created.Reused)
	assert.Equal(t, "node1", created.Primary)
	assert.Equal(t, []string{"node1"}, demoted, "the resource is handed back to drbd-reactor")

	// The password travels only over the secret channel, to the node that
	// initializes, and never in a command line.
	require.Len(t, dep.distributedSecrets, 1)
	assert.Equal(t, created.Password, dep.distributedSecrets[0].content)
	assert.Equal(t, ".sds-app/orders.pw", dep.distributedSecrets[0].relPath)
	assert.Equal(t, []string{"node1"}, dep.distributedSecrets[0].hosts)
	for _, c := range dep.execCalls {
		assert.NotContains(t, c.cmd, created.Password)
	}
	for _, s := range nodes.scripts {
		assert.NotContains(t, s, created.Password)
	}
	init := nodes.scripts[len(nodes.scripts)-2] // init, then the reactor reload
	require.Equal(t, "init", appScriptKind(init))
	assert.Contains(t, init, "staged=/home/sds/.sds-app/orders.pw\n")
	assert.Contains(t, init, "dev=/dev/drbd/by-res/res1/0\n")

	// The unit, then the promoter, on both diskful replicas.
	var unitAt, promoterAt = -1, -1
	for i, d := range dep.distributedConfigs {
		switch d.remotePath {
		case "/etc/systemd/system/sds-app-orders.service":
			unitAt = i
			assert.Equal(t, []string{"node1", "node2"}, d.hosts)
			assert.Contains(t, d.content, "ExecStart=/usr/lib/postgresql/16/bin/postgres -D /var/lib/sds-app/orders/data")
		case "/etc/drbd-reactor.d/sds-app-orders.toml":
			promoterAt = i
			assert.Equal(t, []string{"node1", "node2"}, d.hosts)
			assert.Contains(t, d.content, "[promoter.resources.res1]")
		}
	}
	require.True(t, unitAt >= 0 && promoterAt >= 0)
	assert.Less(t, unitAt, promoterAt, "a promoter must never name a unit systemd does not know yet")

	rec, err := ctrl.db.GetApp(context.Background(), "orders")
	require.NoError(t, err)
	require.NotNil(t, rec)
	assert.Equal(t, "/usr/lib/postgresql/16/bin/postgres", rec.Server)
	assert.Equal(t, 113, rec.UID)
	assert.Equal(t, 5432, rec.Port)
	assert.Equal(t, "/dev/drbd/by-res/res1/0", rec.Device)
}

func TestCreateAppReusesExistingData(t *testing.T) {
	nodes := &appNodes{initOut: "state=reused\n", primary: "node2"}
	ctrl, _ := newAppTestController(t, nodes, "node1,node2")
	created, err := ctrl.appManager().Create(context.Background(), appSpecFor("postgres"))
	require.NoError(t, err)
	assert.True(t, created.Reused)
	assert.Empty(t, created.Password, "the volume's own credentials stay; the new password was never used")
	assert.Equal(t, "node2", created.Primary, "the node already Primary initializes")
}

func TestCreateAppRefusals(t *testing.T) {
	tests := []struct {
		name          string
		nodes         *appNodes
		resourceNodes string
		setup         func(t *testing.T, c *Controller)
		spec          apptemplate.Spec
		want          string
	}{
		{name: "unknown resource", spec: apptemplate.Spec{Name: "orders", Engine: "postgres", Resource: "nope",
			ServiceIP: "10.0.0.50/24"}, want: "resource nope not found"},
		{name: "one replica", resourceNodes: "node1", want: "at least 2"},
		{name: "uid differs", nodes: &appNodes{probe: map[string]string{
			"node2": strings.Replace(appProbeOK, "uid=113", "uid=114", 1)}},
			want: "node1: uid=113 gid=120; node2: uid=114 gid=120"},
		{name: "engine missing", nodes: &appNodes{probe: map[string]string{"node2": "missing= postgres user:postgres\n"}},
			want: "node2 lacks postgres user:postgres"},
		{name: "foreign promoter", nodes: &appNodes{promoters: map[string]string{
			"node2": "promoter=/etc/drbd-reactor.d/sds-ha-res1.toml\n"}},
			want: "node2:/etc/drbd-reactor.d/sds-ha-res1.toml"},
		{name: "ha config", setup: func(t *testing.T, c *Controller) {
			require.NoError(t, c.db.SaveHaConfig(context.Background(), &database.HaConfig{Resource: "res1"}))
		}, want: "already has an HA config"},
		{name: "name taken", setup: func(t *testing.T, c *Controller) {
			require.NoError(t, c.db.SaveApp(context.Background(), &database.App{Name: "orders", Resource: "other"}))
		}, want: "app orders already exists"},
		{name: "resource taken", setup: func(t *testing.T, c *Controller) {
			require.NoError(t, c.db.SaveApp(context.Background(), &database.App{Name: "billing", Resource: "res1"}))
		}, want: "already runs the app billing"},
		{name: "controller metadata", spec: apptemplate.Spec{Name: "meta", Engine: "redis", Resource: SelfHaResource,
			ServiceIP: "10.0.0.50/24"}, want: "metadata resource"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			nodes := tc.nodes
			if nodes == nil {
				nodes = &appNodes{}
			}
			rn := tc.resourceNodes
			if rn == "" {
				rn = "node1,node2"
			}
			ctrl, dep := newAppTestController(t, nodes, rn)
			if tc.setup != nil {
				tc.setup(t, ctrl)
			}
			spec := tc.spec
			if spec.Name == "" {
				spec = appSpecFor("postgres")
			}
			primaries := 0
			dep.drbdPrimaryFunc = func(_ context.Context, host, _ string, _ bool) (*deployment.HostResult, error) {
				primaries++
				return &deployment.HostResult{Host: host, Success: true}, nil
			}
			_, err := ctrl.appManager().Create(context.Background(), spec)
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.want)
			// Refused before anything was done to a node.
			assert.Zero(t, primaries, "nothing promoted")
			assert.Empty(t, dep.distributedSecrets)
			assert.Empty(t, dep.distributedConfigs)
			assert.Zero(t, nodes.ran("init"))
		})
	}
}

func TestCreateAppInitFailureDemotesAndRecordsNothing(t *testing.T) {
	nodes := &appNodes{initFail: true}
	ctrl, dep := newAppTestController(t, nodes, "node1,node2")
	demoted := 0
	dep.drbdSecondaryFunc = func(_ context.Context, host, _ string) (*deployment.HostResult, error) {
		demoted++
		return &deployment.HostResult{Host: host, Success: true}, nil
	}
	_, err := ctrl.appManager().Create(context.Background(), appSpecFor("postgres"))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "initdb failed")
	assert.Equal(t, 1, demoted, "the promotion made for the init is undone")
	assert.Empty(t, dep.distributedConfigs)
	rec, err := ctrl.db.GetApp(context.Background(), "orders")
	require.NoError(t, err)
	assert.Nil(t, rec)
	// The staged password is removed even though the script may not have run.
	cleaned := false
	for _, c := range dep.execCalls {
		cleaned = cleaned || strings.Contains(c.cmd, `rm -f "$HOME/.sds-app/orders.pw"`)
	}
	assert.True(t, cleaned)
}

func TestCreateAppValidationIsInvalidArgument(t *testing.T) {
	ctrl, _ := newAppTestController(t, &appNodes{}, "node1,node2")
	s := NewServer(ctrl)
	_, err := s.CreateApp(context.Background(), &sdspb.CreateAppRequest{Name: "Bad Name", Engine: "postgres",
		ServiceIp: "10.0.0.50/24"})
	require.Error(t, err)
	assert.Equal(t, codes.InvalidArgument, status.Code(err))

	_, err = ctrl.appManager().Create(context.Background(), apptemplate.Spec{Name: "x", Engine: "oracle",
		ServiceIP: "10.0.0.50/24"})
	assert.True(t, errors.Is(err, apptemplate.ErrInvalid))

	resp, err := s.CreateApp(context.Background(), &sdspb.CreateAppRequest{Name: "orders", Engine: "postgres",
		Resource: "res1", ServiceIp: "10.0.0.50/24"})
	require.NoError(t, err)
	require.True(t, resp.Success, resp.Message)
	assert.Len(t, resp.Password, 32)
	assert.Equal(t, "postgres", resp.App.AdminUser)
	assert.Equal(t, "/var/lib/sds-app/orders/sds/password", resp.App.CredentialsFile)
	assert.Contains(t, resp.Message, "postgresql://postgres@10.0.0.50:5432/postgres")
	assert.NotContains(t, resp.Message, resp.Password)
}

func TestPrimaryHostReadsOnlyTheNodesOwnRole(t *testing.T) {
	res := &deployment.ExecResult{Hosts: map[string]*deployment.HostResult{
		"a": {Host: "a", Success: true, Output: "r role:Secondary\n  disk:UpToDate\n  b role:Primary\n"},
		"b": {Host: "b", Success: true, Output: "r role:Primary\n  disk:UpToDate\n"},
		"c": {Host: "c", Success: false, Output: "r role:Primary"},
	}}
	assert.Equal(t, "b", primaryHost(res, []string{"a", "b", "c"}))
	assert.Equal(t, "", primaryHost(res, []string{"a", "c"}), "a peer line and a failed host are not a Primary")
}

func TestPromoterOwnerRefusesAResourceWithAnApp(t *testing.T) {
	ctrl, _ := newAppTestController(t, &appNodes{}, "node1,node2")
	require.NoError(t, ctrl.db.SaveApp(context.Background(), &database.App{Name: "orders", Engine: "postgres", Resource: "res1"}))
	err := ctrl.assertPromoterAllowed(context.Background(), "res1", "ha create")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "the postgres app orders runs on it")
	assert.True(t, ctrl.resources.reactorManaged(context.Background(), "res1"), "a drain evicts it through drbd-reactor")
}
