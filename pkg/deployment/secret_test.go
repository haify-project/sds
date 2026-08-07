package deployment

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

// DistributeSecret exists because DistributeConfig puts its payload in a
// command line. These tests pin the two properties that make it usable for
// credentials: the path is home-relative (so no privilege is needed and the
// file is inside the login user's own directory), and the file mode is 0600.

func TestDistributeSecretRefusesAnAbsolutePath(t *testing.T) {
	c := &Client{logger: zap.NewNop()}
	_, err := c.DistributeSecret(context.Background(), []string{"10.0.0.1"}, "secret", "/etc/sds/creds.conf")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "relative to the login user's home")
}

func TestDistributeSecretWritesAPrivateFileLocally(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	// A local node is written directly rather than over SSH; the mode must be
	// the same 0600 the remote path gets from scp.
	c := &Client{logger: zap.NewNop()}
	localIPs := getLocalIPs()
	if len(localIPs) == 0 {
		t.Skip("no non-loopback local address to exercise the local-host branch")
	}

	res, err := c.DistributeSecret(context.Background(), []string{localIPs[0]}, "s3cr3t", ".sds-backup/x.conf")
	require.NoError(t, err)
	require.True(t, res.Success)

	dest := filepath.Join(home, ".sds-backup", "x.conf")
	data, err := os.ReadFile(dest)
	require.NoError(t, err)
	assert.Equal(t, "s3cr3t", string(data))

	fi, err := os.Stat(dest)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0600), fi.Mode().Perm(),
		"a credential file that any local user can read defeats the whole path")

	di, err := os.Stat(filepath.Dir(dest))
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0700), di.Mode().Perm(),
		"the parent must be private too, or the mode on the file is the only thing standing in the way")
}
