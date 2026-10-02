package controller

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/haify-project/sds/pkg/deployment"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// foreignArch is an architecture other than the one the tests run on.
func foreignArch() string {
	if runtime.GOARCH == "arm64" {
		return "amd64"
	}
	return "arm64"
}

// withSelfHaBinary writes the unit with the given content and a fake running
// binary (plus optional extra files beside it), and returns the binary's path.
func withSelfHaBinary(t *testing.T, unit string, extra ...string) string {
	t.Helper()
	withSelfHaFixtureFiles(t)
	require.NoError(t, os.WriteFile(controllerUnitPath, []byte(unit), 0o644))

	dir := t.TempDir()
	exe := filepath.Join(dir, "sds-controller")
	for _, name := range append([]string{"sds-controller"}, extra...) {
		require.NoError(t, os.WriteFile(filepath.Join(dir, name), []byte("bin"), 0o755))
	}
	orig := controllerExecutable
	controllerExecutable = func() (string, error) { return exe, nil }
	t.Cleanup(func() { controllerExecutable = orig })
	return exe
}

// archDeployment answers `uname -m` on 10.0.0.2 with machine.
func archDeployment(machine string) *fakeDeploymentClient {
	dep := selfHaFakeDeployment()
	base := dep.execFunc
	dep.execFunc = func(ctx context.Context, hosts []string, cmd string, opts ...deployment.ExecOption) (*deployment.ExecResult, error) {
		if cmd == "uname -m" {
			return successExecResult(hosts, machine+"\n"), nil
		}
		return base(ctx, hosts, cmd, opts...)
	}
	return dep
}

// The binary must land where the copied unit's ExecStart says, not at a
// fixed path: the unit goes to the standby verbatim.
func TestEnableSelfHaInstallsBinaryAtUnitExecStart(t *testing.T) {
	exe := withSelfHaBinary(t, "[Unit]\nDescription=x\n\n[Service]\n"+
		"ExecStart=/usr/local/bin/sds-controller --config /etc/sds/controller.toml\n")
	dep := selfHaFakeDeployment()
	ctrl, _, _ := newSelfHaTestController(t, dep)

	_, err := ctrl.resources.EnableSelfHa(context.Background(), "10.0.0.50/24", "p0", 0, 0, nil)
	require.NoError(t, err)

	require.Len(t, dep.installedFiles, 1)
	assert.Equal(t, exe, dep.installedFiles[0].localPath)
	assert.Equal(t, "/usr/local/bin/sds-controller", dep.installedFiles[0].remotePath)
	assert.Equal(t, []string{"10.0.0.2"}, dep.installedFiles[0].hosts)

	for _, dc := range dep.distributedConfigs {
		if dc.remotePath == controllerUnitPath {
			assert.Contains(t, dc.content, "ExecStart=/usr/local/bin/sds-controller ")
		}
	}
}

// A standby of another architecture gets the per-arch build placed beside
// the running binary.
func TestEnableSelfHaUsesPerArchBinary(t *testing.T) {
	arch := foreignArch()
	exe := withSelfHaBinary(t, "[Service]\nExecStart=/opt/sds/bin/sds-controller\n", "sds-controller-"+arch)
	dep := archDeployment(unameMachine(arch))
	ctrl, _, _ := newSelfHaTestController(t, dep)

	_, err := ctrl.resources.EnableSelfHa(context.Background(), "10.0.0.50/24", "p0", 0, 0, nil)
	require.NoError(t, err)

	require.Len(t, dep.installedFiles, 1)
	assert.Equal(t, exe+"-"+arch, dep.installedFiles[0].localPath)
	assert.Equal(t, "/opt/sds/bin/sds-controller", dep.installedFiles[0].remotePath)
}

// Without a build for the standby's architecture, enable refuses before it
// changes anything.
func TestEnableSelfHaRefusesForeignArchBeforeChanges(t *testing.T) {
	withSelfHaBinary(t, "[Service]\nExecStart=/opt/sds/bin/sds-controller\n")
	dep := archDeployment(unameMachine(foreignArch()))
	ctrl, _, _ := newSelfHaTestController(t, dep)

	_, err := ctrl.resources.EnableSelfHa(context.Background(), "10.0.0.50/24", "p0", 0, 0, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "node2 ("+unameMachine(foreignArch())+")")
	assert.Contains(t, err.Error(), "sds-controller-<goarch>")

	assert.Empty(t, dep.installedFiles)
	assert.Empty(t, dep.distributedConfigs)
	for _, c := range dep.execCalls {
		assert.NotContains(t, c.cmd, "create-md")
	}
	_, err = ctrl.db.GetResource(context.Background(), SelfHaResource)
	assert.Error(t, err, "the metadata resource must not have been created")
}

func TestEnableSelfHaRefusesUnparseableUnit(t *testing.T) {
	withSelfHaBinary(t, "[Service]\nType=simple\n")
	dep := selfHaFakeDeployment()
	ctrl, _, _ := newSelfHaTestController(t, dep)

	_, err := ctrl.resources.EnableSelfHa(context.Background(), "10.0.0.50/24", "p0", 0, 0, nil)
	assert.ErrorContains(t, err, "no ExecStart")
	assert.Empty(t, dep.distributedConfigs)
}

func TestUnitExecStartPath(t *testing.T) {
	cases := []struct {
		unit, want, err string
	}{
		{"[Service]\nExecStart=/opt/sds/bin/sds-controller\n", "/opt/sds/bin/sds-controller", ""},
		{"[Service]\nExecStart=/usr/local/bin/sds-controller --config /etc/sds/controller.toml\n", "/usr/local/bin/sds-controller", ""},
		{"[Service]\nExecStart=-/usr/local/bin/sds-controller\n", "/usr/local/bin/sds-controller", ""},
		{"[Service]\nExecStart=\"/srv/sds bin/sds-controller\" --x\n", "/srv/sds bin/sds-controller", ""},
		{"[Service]\nExecStart=-\n", "", "empty ExecStart"},
		{"[Service]\n  ExecStart = /a/b \n", "/a/b", ""},
		{"[Service]\nExecStart=/old\nExecStart=\nExecStart=/new --x\n", "/new", ""},
		{"[Unit]\nExecStart=/wrong\n[Service]\nType=simple\n", "", "no ExecStart"},
		{"[Service]\nExecStart=sds-controller\n", "", "absolute"},
	}
	for _, c := range cases {
		got, err := unitExecStartPath(c.unit)
		if c.err != "" {
			assert.ErrorContains(t, err, c.err, c.unit)
			continue
		}
		require.NoError(t, err, c.unit)
		assert.Equal(t, c.want, got, c.unit)
	}
}

func TestGoarchFromMachine(t *testing.T) {
	for _, goarch := range []string{"amd64", "arm64", "loong64", "386"} {
		assert.Equal(t, goarch, goarchFromMachine(unameMachine(goarch)))
	}
	assert.Equal(t, "arm64", goarchFromMachine("arm64"))
}
