package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The secret must not be reachable through a flag. A flag lands in the
// operator's shell history and in this process's argv, which every other user
// on the machine can read — and that would undo the care taken everywhere else
// on this path.
func TestBackupTargetAddHasNoSecretFlag(t *testing.T) {
	cmd := backupTargetAddCommand()
	for _, name := range []string{"secret", "secret-key", "password", "access-secret"} {
		assert.Nil(t, cmd.Flags().Lookup(name), "flag %q would put the key in argv", name)
	}
	require.NotNil(t, cmd.Flags().Lookup("secret-file"))
}

func TestReadBackupSecretSources(t *testing.T) {
	t.Run("environment", func(t *testing.T) {
		t.Setenv(secretEnvVar, "from-env")
		got, err := readBackupSecret(strings.NewReader(""), "")
		require.NoError(t, err)
		assert.Equal(t, "from-env", got)
	})

	t.Run("stdin", func(t *testing.T) {
		got, err := readBackupSecret(strings.NewReader("from-stdin\n"), "-")
		require.NoError(t, err)
		assert.Equal(t, "from-stdin", got, "a trailing newline is echo's, not the operator's")
	})

	t.Run("file", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "secret")
		require.NoError(t, os.WriteFile(path, []byte("from-file\r\n"), 0600))
		got, err := readBackupSecret(strings.NewReader(""), path)
		require.NoError(t, err)
		assert.Equal(t, "from-file", got)
	})

	t.Run("nothing supplied says where to put it", func(t *testing.T) {
		t.Setenv(secretEnvVar, "")
		_, err := readBackupSecret(strings.NewReader(""), "")
		require.Error(t, err)
		assert.Contains(t, err.Error(), secretEnvVar)
		assert.Contains(t, err.Error(), "--secret-file")
	})
}

// The full-only limitation has to be visible where someone plans a schedule,
// not only in a design doc they will never read.
func TestBackupHelpStatesTheFullOnlyLimitation(t *testing.T) {
	for _, cmd := range []string{backupCommand().Long, backupCreateCommand().Long} {
		assert.Contains(t, cmd, "FULL image")
		assert.Contains(t, cmd, "NO incremental")
	}
}
