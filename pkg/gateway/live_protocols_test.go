package gateway

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The live scripts run against a fake configfs (and, for iSCSI, a fake
// targetcli that keeps it in step), so the reconcile logic itself is
// exercised, not only the text it is made of.

func runLiveScript(t *testing.T, script string, env ...string) string {
	t.Helper()
	cmd := exec.Command("/bin/sh", "-c", script)
	cmd.Env = append(os.Environ(), env...)
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, string(out))
	return string(out)
}

func TestNVMeLiveScriptReconcilesAllowedHosts(t *testing.T) {
	root := t.TempDir()
	old := nvmetConfigfs
	nvmetConfigfs = root
	t.Cleanup(func() { nvmetConfigfs = old })
	nqn := "nqn.2024-01.com.example:blk"
	sub := filepath.Join(root, "subsystems", nqn)
	require.NoError(t, os.MkdirAll(filepath.Join(sub, "allowed_hosts"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(sub, "attr_allow_any_host"), []byte("1\n"), 0o644))

	params := func(list string) map[string]string {
		return map[string]string{"nqn": nqn, "serial": "abc", "allowed_initiators": list}
	}
	links := func() []string {
		entries, err := os.ReadDir(filepath.Join(sub, "allowed_hosts"))
		require.NoError(t, err)
		var names []string
		for _, e := range entries {
			names = append(names, e.Name())
		}
		return names
	}
	anyHost := func() string {
		b, err := os.ReadFile(filepath.Join(sub, "attr_allow_any_host"))
		require.NoError(t, err)
		return strings.TrimSpace(string(b))
	}

	s, err := nvmeSubsystemLiveScript(params(""), params("nqn.2024-01.com.example:a nqn.2024-01.com.example:b"))
	require.NoError(t, err)
	runLiveScript(t, s)
	assert.Equal(t, []string{"nqn.2024-01.com.example:a", "nqn.2024-01.com.example:b"}, links())
	assert.Equal(t, "0", anyHost())
	assert.DirExists(t, filepath.Join(root, "hosts", "nqn.2024-01.com.example:a"))

	s, err = nvmeSubsystemLiveScript(params("x"), params("nqn.2024-01.com.example:b"))
	require.NoError(t, err)
	runLiveScript(t, s)
	assert.Equal(t, []string{"nqn.2024-01.com.example:b"}, links())

	s, err = nvmeSubsystemLiveScript(params("x"), params(""))
	require.NoError(t, err)
	runLiveScript(t, s)
	assert.Empty(t, links())
	assert.Equal(t, "1", anyHost())

	_, err = nvmeSubsystemLiveScript(params(""), map[string]string{"nqn": nqn, "serial": "other"})
	require.Error(t, err, "a changed serial cannot be applied live")
}

// fakeTargetcli logs its arguments and mirrors ACL create/delete in configfs.
const fakeTargetcli = `#!/bin/sh
echo "$*" >> "$TARGETCLI_LOG"
case "$2 $3" in
  "create "*) mkdir -p "$TARGETCLI_TPG/acls/$3" ;;
  "delete "*) rmdir "$TARGETCLI_TPG/acls/$3" ;;
esac
`

func TestISCSILiveScriptReconcilesACLs(t *testing.T) {
	root := t.TempDir()
	old := lioConfigfs
	lioConfigfs = filepath.Join(root, "iscsi")
	t.Cleanup(func() { lioConfigfs = old })
	iqn := "iqn.2024-01.com.example:blk"
	tpg := filepath.Join(lioConfigfs, iqn, "tpgt_1")
	require.NoError(t, os.MkdirAll(filepath.Join(tpg, "acls"), 0o755))
	bin := filepath.Join(root, "bin")
	require.NoError(t, os.MkdirAll(bin, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(bin, "targetcli"), []byte(fakeTargetcli), 0o755))
	logFile := filepath.Join(root, "log")
	env := []string{"PATH=" + bin + ":" + os.Getenv("PATH"), "TARGETCLI_LOG=" + logFile, "TARGETCLI_TPG=" + tpg}

	params := func(list, user string) map[string]string {
		return map[string]string{"iqn": iqn, "portals": "1.2.3.4:3260", "implementation": "lio-t",
			"allowed_initiators": list, "incoming_username": user, "incoming_password": "s3cret"}
	}
	calls := func() []string {
		b, err := os.ReadFile(logFile)
		require.NoError(t, err)
		_ = os.Remove(logFile)
		return strings.Split(strings.TrimSpace(string(b)), "\n")
	}
	acls := func() []string {
		entries, err := os.ReadDir(filepath.Join(tpg, "acls"))
		require.NoError(t, err)
		var names []string
		for _, e := range entries {
			names = append(names, e.Name())
		}
		return names
	}

	// Allow-all -> two initiators with CHAP.
	s, err := iscsiTargetLiveScript(params("", ""), params("iqn.2024-01.com.example:a iqn.2024-01.com.example:b", "user"))
	require.NoError(t, err)
	runLiveScript(t, s, env...)
	assert.Equal(t, []string{"iqn.2024-01.com.example:a", "iqn.2024-01.com.example:b"}, acls())
	got := calls()
	assert.Equal(t, "/iscsi/"+iqn+"/tpg1/acls create iqn.2024-01.com.example:a add_mapped_luns=true", got[0])
	assert.Contains(t, got, "/iscsi/"+iqn+"/tpg1/acls/iqn.2024-01.com.example:b set auth password=s3cret")
	last := strings.Join(got, "\n")
	assert.Less(t, strings.Index(last, "create iqn.2024-01.com.example:b"), strings.Index(last, "generate_node_acls=0"),
		"ACLs exist before the TPG leaves demo mode")

	// Remove one: only its ACL is deleted.
	s, err = iscsiTargetLiveScript(params("x", "user"), params("iqn.2024-01.com.example:b", "user"))
	require.NoError(t, err)
	runLiveScript(t, s, env...)
	assert.Equal(t, []string{"iqn.2024-01.com.example:b"}, acls())
	assert.Contains(t, calls(), "/iscsi/"+iqn+"/tpg1/acls delete iqn.2024-01.com.example:a")

	// Back to allow-all.
	s, err = iscsiTargetLiveScript(params("x", "user"), params("", "user"))
	require.NoError(t, err)
	runLiveScript(t, s, env...)
	assert.Empty(t, acls())
	assert.Contains(t, calls(), "/iscsi/"+iqn+"/tpg1 set attribute authentication=1 demo_mode_write_protect=0 generate_node_acls=1 cache_dynamic_acls=1")
}

func TestLiveEditScriptsParse(t *testing.T) {
	old := testISCSIConfig(t, "iqn.2024-01.com.example:blk")
	edited := strings.Replace(old, "allowed_initiators=", "allowed_initiators=iqn.2024-01.com.example:a", 1)
	e, err := planLiveEdit(old, edited)
	require.NoError(t, err)
	for name, script := range map[string]string{"edit": e.script(), "hook": pendingHookScript} {
		out, err := exec.Command("/bin/sh", "-n", "-c", script).CombinedOutput()
		assert.NoError(t, err, "%s: %s", name, out)
	}
	assert.Contains(t, pendingHookScript, `$${p%%.pending}`, "systemd needs $$ and %% for a literal $ and %")
}
