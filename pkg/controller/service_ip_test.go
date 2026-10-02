package controller

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/haify-project/sds/pkg/deployment"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestServiceIPUnitMatchesShippedFile(t *testing.T) {
	shipped, err := os.ReadFile("../../configs/service-ip@.service")
	require.NoError(t, err)
	assert.Equal(t, strings.TrimSpace(string(shipped)), strings.TrimSpace(serviceIPUnit))
}

// serviceIPTestNodes answers the presence probe: each address maps to what the
// node prints — nothing when it has service-ip, else its uname -m.
func serviceIPTestNodes(t *testing.T, probe map[string]string) (*Controller, *fakeDeploymentClient) {
	t.Helper()
	src := filepath.Join(t.TempDir(), "service-ip")
	require.NoError(t, os.WriteFile(src, []byte("#!/bin/sh\n"), 0755))
	orig := serviceIPSourceOverride
	serviceIPSourceOverride = src
	t.Cleanup(func() { serviceIPSourceOverride = orig })

	dep := &fakeDeploymentClient{}
	dep.execFunc = func(ctx context.Context, hosts []string, cmd string, opts ...deployment.ExecOption) (*deployment.ExecResult, error) {
		res := &deployment.ExecResult{Hosts: map[string]*deployment.HostResult{}}
		for _, h := range hosts {
			out := ""
			if strings.Contains(cmd, serviceIPUnitPath) {
				out = probe[h]
			}
			res.Hosts[h] = &deployment.HostResult{Host: h, Success: true, Output: out + "\n"}
		}
		return res, nil
	}
	ctrl := newBasicTestController(dep)
	ctrl.hostsMap = map[string]string{"n1": "10.0.0.1", "n2": "10.0.0.2", "n3": "10.0.0.3"}
	return ctrl, dep
}

func TestEnsureServiceIPInstallsOnlyWhereMissing(t *testing.T) {
	arch := unameMachine(runtime.GOARCH)
	ctrl, dep := serviceIPTestNodes(t, map[string]string{
		"10.0.0.1": "", "10.0.0.2": arch, "10.0.0.3": "",
	})
	require.NoError(t, ctrl.resources.ensureServiceIP(context.Background(), []string{"10.0.0.1", "10.0.0.2", "10.0.0.3"}))

	require.Len(t, dep.installedFiles, 1)
	assert.Equal(t, []string{"10.0.0.2"}, dep.installedFiles[0].hosts)
	assert.Equal(t, serviceIPBinaryPath, dep.installedFiles[0].remotePath)
	require.Len(t, dep.distributedConfigs, 1)
	assert.Equal(t, []string{"10.0.0.2"}, dep.distributedConfigs[0].hosts)
	assert.Equal(t, serviceIPUnitPath, dep.distributedConfigs[0].remotePath)
}

func TestEnsureServiceIPNothingToDo(t *testing.T) {
	ctrl, dep := serviceIPTestNodes(t, map[string]string{"10.0.0.1": "", "10.0.0.2": ""})
	require.NoError(t, ctrl.resources.ensureServiceIP(context.Background(), []string{"10.0.0.1", "10.0.0.2"}))
	assert.Empty(t, dep.installedFiles)
	assert.Empty(t, dep.distributedConfigs)
}

// A node of another architecture must not be handed this controller's build:
// the VIP would fail only when it first moves there.
func TestEnsureServiceIPRefusesOtherArchitecture(t *testing.T) {
	ctrl, dep := serviceIPTestNodes(t, map[string]string{"10.0.0.1": "", "10.0.0.2": "riscv64-other"})
	err := ctrl.resources.ensureServiceIP(context.Background(), []string{"10.0.0.1", "10.0.0.2"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "n2 (riscv64-other)")
	assert.Empty(t, dep.installedFiles)
}

func TestEnsureServiceIPWithoutASourceNamesTheNodes(t *testing.T) {
	ctrl, _ := serviceIPTestNodes(t, map[string]string{"10.0.0.1": unameMachine(runtime.GOARCH)})
	serviceIPSourceOverride = filepath.Join(t.TempDir(), "absent")
	err := ctrl.resources.ensureServiceIP(context.Background(), []string{"10.0.0.1"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not installed on n1")
}
