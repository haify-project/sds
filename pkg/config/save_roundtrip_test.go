package config

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Save wrote sections under their Go field names while Load read the
// mapstructure tags, so a saved file loaded back gave defaults for everything.
// It went unnoticed because the values the tests saved happened to equal the
// defaults; changing one default is what exposed it.
func TestSaveSurvivesALoad(t *testing.T) {
	path := filepath.Join(t.TempDir(), "c.toml")
	cfg := &Config{
		Server:   ServerConfig{ListenAddress: "10.0.0.9", Port: 43511},
		Storage:  StorageConfig{DefaultPoolType: "vg", DefaultSnapshotSuffix: "_x"},
		Alert:    AlertConfig{Enabled: true, WebhookURL: "https://example.invalid/hook"},
		Log:      LogConfig{Level: "debug", Format: "console"},
		Database: DatabaseConfig{Path: "/tmp/haify.db"},
	}
	require.NoError(t, cfg.Save(path))

	loaded, err := Load(path)
	require.NoError(t, err)

	// Explicitly a value that differs from the default, or the assertion would
	// pass on a Save that wrote nothing at all.
	assert.Equal(t, "vg", loaded.Storage.DefaultPoolType)
	assert.Equal(t, "_x", loaded.Storage.DefaultSnapshotSuffix)
	assert.Equal(t, "10.0.0.9", loaded.Server.ListenAddress)
	assert.Equal(t, 43511, loaded.Server.Port)
	assert.Equal(t, "https://example.invalid/hook", loaded.Alert.WebhookURL)
	assert.True(t, loaded.Alert.Enabled)
	assert.Equal(t, "debug", loaded.Log.Level)
	assert.Equal(t, "console", loaded.Log.Format)
}

// The keys on disk have to be the ones Load looks for; a reader other than
// viper (an operator, a config-management tool) sees this file too.
func TestSaveWritesSnakeCaseKeys(t *testing.T) {
	path := filepath.Join(t.TempDir(), "c.toml")
	cfg := &Config{Storage: StorageConfig{DefaultPoolType: "vg"}}
	require.NoError(t, cfg.Save(path))

	raw, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Contains(t, string(raw), "default_pool_type")
	assert.NotContains(t, string(raw), "DefaultPoolType")
}

// Thin by default: a thick LVM pool reserves a fixed copy-on-write area per
// snapshot, so it cannot hold a retention history.
func TestDefaultPoolTypeIsThin(t *testing.T) {
	path := filepath.Join(t.TempDir(), "empty.toml")
	require.NoError(t, os.WriteFile(path, []byte("[server]\nport = 3374\n"), 0o600))

	loaded, err := Load(path)
	require.NoError(t, err)
	assert.Equal(t, "thin_pool", loaded.Storage.DefaultPoolType)
}
