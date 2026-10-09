package controller

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// evictScript must evict through whichever promoter config exists — a gateway
// is haify-iscsi-<res>, not haify-ha-<res> — and fail when there is none, because
// drbd-reactorctl reports success for a config name that does not exist.
func TestEvictScriptPicksTheExistingPromoter(t *testing.T) {
	script := evictScript("isc1")
	for _, n := range []string{"haify-ha-isc1", "haify-nfs-isc1", "haify-iscsi-isc1", "haify-nvmeof-isc1"} {
		assert.Contains(t, script, n)
	}

	// Run it against a fake /etc/drbd-reactor.d and a drbd-reactorctl that
	// prints what it was asked to evict.
	root := t.TempDir()
	confDir := filepath.Join(root, "etc", "drbd-reactor.d")
	bin := filepath.Join(root, "bin")
	require.NoError(t, os.MkdirAll(confDir, 0755))
	require.NoError(t, os.MkdirAll(bin, 0755))
	reactorctl := filepath.Join(bin, "drbd-reactorctl")
	fakeReactorctl := func(output string) {
		require.NoError(t, os.WriteFile(reactorctl, []byte("#!/bin/sh\necho \"$@\"\nprintf '%s\\n' \""+output+"\"\n"), 0755))
	}
	fakeReactorctl("Node 'n2' took over")
	local := strings.ReplaceAll(script, "/etc/drbd-reactor.d", confDir)
	run := func() (string, error) {
		cmd := exec.Command("/bin/sh", "-c", local)
		cmd.Env = append(os.Environ(), "PATH="+bin+":"+os.Getenv("PATH"))
		out, err := cmd.CombinedOutput()
		return strings.TrimSpace(string(out)), err
	}

	out, err := run()
	require.Error(t, err)
	assert.Contains(t, out, "no drbd-reactor promoter manages isc1")

	require.NoError(t, os.WriteFile(filepath.Join(confDir, "haify-iscsi-isc1.toml"), nil, 0644))
	out, err = run()
	require.NoError(t, err)
	assert.Equal(t, "evict haify-iscsi-isc1\nNode 'n2' took over", out)

	// reactorctl exits 0 when it gave up and re-enabled the resource here.
	fakeReactorctl("Local node still DRBD Primary, not all services stopped in time locally\\nRe-enabling isc1")
	out, err = run()
	require.Error(t, err)
	assert.Contains(t, out, "no other node took over isc1")
	fakeReactorctl("Unfortunately no other node took over, resource in unknown state")
	_, err = run()
	require.Error(t, err)
}
