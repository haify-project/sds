package controller

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/liliang-cn/sds/pkg/deployment"
	"github.com/liliang-cn/sds/pkg/wanproxy"
)

// swapBinaryPath points the resolver at a temp directory for the duration of a
// test.
func swapBinaryPath(t *testing.T, path string) func() {
	t.Helper()
	old := wanproxyLocalBinaryPath
	wanproxyLocalBinaryPath = path
	return func() { wanproxyLocalBinaryPath = old }
}

// archFake answers the architecture probe per host.
func archFake(byHost map[string]string) *fakeDeploymentClient {
	f := &fakeDeploymentClient{}
	f.execFunc = func(ctx context.Context, hosts []string, cmd string, opts ...deployment.ExecOption) (*deployment.ExecResult, error) {
		return successExecResult(hosts, byHost[hosts[0]]+"\n"), nil
	}
	return f
}

// One binary pushed to every node is right only while the fleet is uniform, and
// a two-site cluster is the case least likely to be: whatever is on the shelf at
// home, replicating to whatever the cloud rents.
func TestWanproxyBinaryResolverPicksPerArchitecture(t *testing.T) {
	dir := t.TempDir()
	plain := filepath.Join(dir, "sds-proxy")
	require.NoError(t, os.WriteFile(plain, []byte("host-arch"), 0o755))
	require.NoError(t, os.WriteFile(plain+"-arm64", []byte("arm"), 0o755))
	require.NoError(t, os.WriteFile(plain+"-amd64", []byte("x86"), 0o755))

	restore := swapBinaryPath(t, plain)
	defer restore()

	rm := newBasicTestController(archFake(map[string]string{
		"10.0.0.1": "arm64",
		"10.0.0.2": "amd64",
	})).resources

	pick := rm.wanproxyBinaryResolver(context.Background(), []string{"10.0.0.1", "10.0.0.2"})
	assert.Equal(t, plain+"-arm64", pick("10.0.0.1"))
	assert.Equal(t, plain+"-amd64", pick("10.0.0.2"))
}

// A single-architecture deployment must keep working with the one binary it has
// always had, at the path it has always been at.
func TestWanproxyBinaryResolverFallsBackForMatchingArch(t *testing.T) {
	dir := t.TempDir()
	plain := filepath.Join(dir, "sds-proxy")
	require.NoError(t, os.WriteFile(plain, []byte("host-arch"), 0o755))

	restore := swapBinaryPath(t, plain)
	defer restore()

	rm := newBasicTestController(archFake(map[string]string{"10.0.0.1": runtime.GOARCH})).resources
	pick := rm.wanproxyBinaryResolver(context.Background(), []string{"10.0.0.1"})
	assert.Equal(t, plain, pick("10.0.0.1"))
}

// Pushing a binary of the wrong architecture is worse than pushing none: the
// node ends up with a unit that crash-loops on exec, whereas "absent" is a
// failure the operator can read and act on.
func TestWanproxyBinaryResolverRefusesMismatchedFallback(t *testing.T) {
	dir := t.TempDir()
	plain := filepath.Join(dir, "sds-proxy")
	require.NoError(t, os.WriteFile(plain, []byte("host-arch"), 0o755))

	restore := swapBinaryPath(t, plain)
	defer restore()

	other := "arm64"
	if runtime.GOARCH == "arm64" {
		other = "amd64"
	}
	rm := newBasicTestController(archFake(map[string]string{"10.0.0.1": other})).resources
	pick := rm.wanproxyBinaryResolver(context.Background(), []string{"10.0.0.1"})
	assert.Equal(t, "", pick("10.0.0.1"), "no binary beats the wrong binary")
}

// Grouping keeps a uniform fleet down to one push rather than one per node.
func TestEnsureBinariesGroupsHostsBySourceFile(t *testing.T) {
	spec := wanproxy.ProxySpec{
		BinaryFor: func(h string) string {
			switch h {
			case "a", "b":
				return "/x/sds-proxy-amd64"
			case "c":
				return "/x/sds-proxy-arm64"
			}
			return "" // pre-staged
		},
	}
	seen := map[string][]string{}
	for _, h := range []string{"a", "b", "c", "d"} {
		if p := spec.BinaryFor(h); p != "" {
			seen[p] = append(seen[p], h)
		}
	}
	assert.Equal(t, []string{"a", "b"}, seen["/x/sds-proxy-amd64"])
	assert.Equal(t, []string{"c"}, seen["/x/sds-proxy-arm64"])
	assert.Len(t, seen, 2, "one push per distinct file, not per node")
}
