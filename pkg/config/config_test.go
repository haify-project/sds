package config

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLoadWithValidConfig(t *testing.T) {
	// Create a temporary config file
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "controller.toml")

	configContent := `
[server]
listen_address = "0.0.0.0"
port = 3374

[database]
path = "/var/lib/sds/sds.db"

[log]
level = "info"
format = "json"

[storage]
default_pool_type = "vg"
default_snapshot_suffix = "_snap"

[metrics]
enabled = true
listen_address = "0.0.0.0"
port = 9433
`
	err := os.WriteFile(configPath, []byte(configContent), 0644)
	require.NoError(t, err)

	cfg, err := Load(configPath)
	require.NoError(t, err)
	require.NotNil(t, cfg)

	// Verify server config
	assert.Equal(t, "0.0.0.0", cfg.Server.ListenAddress)
	assert.Equal(t, 3374, cfg.Server.Port)

	// Verify database config
	assert.Equal(t, "/var/lib/sds/sds.db", cfg.Database.Path)

	// Verify log config
	assert.Equal(t, "info", cfg.Log.Level)
	assert.Equal(t, "json", cfg.Log.Format)

	// Verify storage config
	assert.Equal(t, "vg", cfg.Storage.DefaultPoolType)
	assert.Equal(t, "_snap", cfg.Storage.DefaultSnapshotSuffix)

	// Verify metrics config
	assert.True(t, cfg.Metrics.Enabled)
	assert.Equal(t, "0.0.0.0", cfg.Metrics.ListenAddress)
	assert.Equal(t, 9433, cfg.Metrics.Port)
}

func TestLoadWithNonExistentFile(t *testing.T) {
	cfg, err := Load("/nonexistent/path/config.toml")
	assert.Error(t, err)
	assert.Nil(t, cfg)
}

func TestLoadDefaultValues(t *testing.T) {
	// Create a minimal config file
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "minimal.toml")

	configContent := `
[server]
# Just to have a valid config
`
	err := os.WriteFile(configPath, []byte(configContent), 0644)
	require.NoError(t, err)

	cfg, err := Load(configPath)
	require.NoError(t, err)
	require.NotNil(t, cfg)

	// Check default values
	assert.Equal(t, 3374, cfg.Server.Port)
	assert.Equal(t, "0.0.0.0", cfg.Server.ListenAddress)
	assert.Equal(t, "info", cfg.Log.Level)
	assert.Equal(t, "json", cfg.Log.Format)
	assert.Equal(t, "vg", cfg.Storage.DefaultPoolType)
}

func TestLoadWithStorageConfig(t *testing.T) {
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "storage.toml")

	configContent := `
[storage]
default_pool_type = "zfs"
default_snapshot_suffix = "_backup"
`
	err := os.WriteFile(configPath, []byte(configContent), 0644)
	require.NoError(t, err)

	cfg, err := Load(configPath)
	require.NoError(t, err)
	require.NotNil(t, cfg)

	assert.Equal(t, "zfs", cfg.Storage.DefaultPoolType)
	assert.Equal(t, "_backup", cfg.Storage.DefaultSnapshotSuffix)
}

func TestLoadWithLogConfig(t *testing.T) {
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "log.toml")

	configContent := `
[log]
level = "debug"
format = "console"
`
	err := os.WriteFile(configPath, []byte(configContent), 0644)
	require.NoError(t, err)

	cfg, err := Load(configPath)
	require.NoError(t, err)
	require.NotNil(t, cfg)

	assert.Equal(t, "debug", cfg.Log.Level)
	assert.Equal(t, "console", cfg.Log.Format)
}

func TestLoadWithTLSConfig(t *testing.T) {
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "tls.toml")

	configContent := `
[tls]
enabled = true
ca_cert = "/path/to/ca.crt"
client_cert = "/path/to/client.crt"
client_key = "/path/to/client.key"
`
	err := os.WriteFile(configPath, []byte(configContent), 0644)
	require.NoError(t, err)

	cfg, err := Load(configPath)
	require.NoError(t, err)
	require.NotNil(t, cfg)

	assert.True(t, cfg.TLS.Enabled)
	assert.Equal(t, "/path/to/ca.crt", cfg.TLS.CACert)
	assert.Equal(t, "/path/to/client.crt", cfg.TLS.ClientCert)
	assert.Equal(t, "/path/to/client.key", cfg.TLS.ClientKey)
}

func TestLoadWithServerAddress(t *testing.T) {
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "server.toml")

	configContent := `
[server]
listen_address = "192.168.1.100"
port = 8080
`
	err := os.WriteFile(configPath, []byte(configContent), 0644)
	require.NoError(t, err)

	cfg, err := Load(configPath)
	require.NoError(t, err)
	require.NotNil(t, cfg)

	assert.Equal(t, "192.168.1.100", cfg.Server.ListenAddress)
	assert.Equal(t, 8080, cfg.Server.Port)
}

func TestConfigValidate(t *testing.T) {
	tests := []struct {
		name     string
		config   *Config
		hasError bool
	}{
		{
			name: "valid config",
			config: &Config{
				Server: ServerConfig{
					ListenAddress: "0.0.0.0",
					Port:          3374,
				},
			},
			hasError: false,
		},
		{
			name: "empty listen address gets default",
			config: &Config{
				Server: ServerConfig{},
			},
			hasError: false,
		},
		{
			name: "zero port gets default",
			config: &Config{
				Server: ServerConfig{
					ListenAddress: "0.0.0.0",
				},
			},
			hasError: false,
		},
		{
			name: "empty log level gets default",
			config: &Config{
				Log: LogConfig{},
			},
			hasError: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.config.Validate()
			if tt.hasError {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

func TestConfigSave(t *testing.T) {
	// First, load a config
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "original.toml")

	configContent := `
[server]
listen_address = "0.0.0.0"
port = 3374
`
	err := os.WriteFile(configPath, []byte(configContent), 0644)
	require.NoError(t, err)

	cfg, err := Load(configPath)
	require.NoError(t, err)

	// Modify and save
	cfg.Server.Port = 8080
	cfg.Log.Level = "debug"

	savePath := filepath.Join(tmpDir, "saved.toml")
	err = cfg.Save(savePath)
	require.NoError(t, err)

	// Verify file was created
	_, err = os.Stat(savePath)
	require.NoError(t, err)

	// Load saved config and verify
	savedCfg, err := Load(savePath)
	require.NoError(t, err)
	assert.Equal(t, 8080, savedCfg.Server.Port)
	assert.Equal(t, "debug", savedCfg.Log.Level)
}

func TestLoadWithMetricsConfig(t *testing.T) {
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "metrics.toml")

	configContent := `
[metrics]
enabled = false
listen_address = "127.0.0.1"
port = 9090
`
	err := os.WriteFile(configPath, []byte(configContent), 0644)
	require.NoError(t, err)

	cfg, err := Load(configPath)
	require.NoError(t, err)
	require.NotNil(t, cfg)

	assert.False(t, cfg.Metrics.Enabled)
	assert.Equal(t, "127.0.0.1", cfg.Metrics.ListenAddress)
	assert.Equal(t, 9090, cfg.Metrics.Port)
}

func TestLoadWithDatabaseConfig(t *testing.T) {
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "database.toml")

	configContent := `
[database]
path = "/custom/path/sds.db"
`
	err := os.WriteFile(configPath, []byte(configContent), 0644)
	require.NoError(t, err)

	cfg, err := Load(configPath)
	require.NoError(t, err)
	require.NotNil(t, cfg)

	assert.Equal(t, "/custom/path/sds.db", cfg.Database.Path)
}
