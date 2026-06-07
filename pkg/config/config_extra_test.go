package config

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLoadWithMinimalServerConfig(t *testing.T) {
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "minimal.toml")

	configContent := `
[server]
`
	err := os.WriteFile(configPath, []byte(configContent), 0644)
	require.NoError(t, err)

	cfg, err := Load(configPath)
	require.NoError(t, err)
	require.NotNil(t, cfg)

	// Check defaults are applied
	assert.Equal(t, "0.0.0.0", cfg.Server.ListenAddress)
	assert.Equal(t, 3374, cfg.Server.Port)
}

func TestLoadWithPartialServerConfig(t *testing.T) {
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "partial.toml")

	configContent := `
[server]
port = 8080
`
	err := os.WriteFile(configPath, []byte(configContent), 0644)
	require.NoError(t, err)

	cfg, err := Load(configPath)
	require.NoError(t, err)
	require.NotNil(t, cfg)

	// Port should be set
	assert.Equal(t, 8080, cfg.Server.Port)
	// ListenAddress should be default
	assert.Equal(t, "0.0.0.0", cfg.Server.ListenAddress)
}

func TestLoadWithTLSDisabled(t *testing.T) {
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "tls.toml")

	configContent := `
[tls]
enabled = false
`
	err := os.WriteFile(configPath, []byte(configContent), 0644)
	require.NoError(t, err)

	cfg, err := Load(configPath)
	require.NoError(t, err)
	require.NotNil(t, cfg)

	assert.False(t, cfg.TLS.Enabled)
}

func TestLoadWithMetricsDisabled(t *testing.T) {
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "metrics.toml")

	configContent := `
[metrics]
enabled = false
`
	err := os.WriteFile(configPath, []byte(configContent), 0644)
	require.NoError(t, err)

	cfg, err := Load(configPath)
	require.NoError(t, err)
	require.NotNil(t, cfg)

	assert.False(t, cfg.Metrics.Enabled)
}

func TestLoadWithComplexStorageConfig(t *testing.T) {
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "storage.toml")

	configContent := `
[storage]
default_pool_type = "zfs"
default_snapshot_suffix = "_backup_2024"
`
	err := os.WriteFile(configPath, []byte(configContent), 0644)
	require.NoError(t, err)

	cfg, err := Load(configPath)
	require.NoError(t, err)
	require.NotNil(t, cfg)

	assert.Equal(t, "zfs", cfg.Storage.DefaultPoolType)
	assert.Equal(t, "_backup_2024", cfg.Storage.DefaultSnapshotSuffix)
}

func TestLoadWithAllLogLevels(t *testing.T) {
	levels := []string{"debug", "info", "warn", "error"}

	for _, level := range levels {
		t.Run(level, func(t *testing.T) {
			tmpDir := t.TempDir()
			configPath := filepath.Join(tmpDir, "log_"+level+".toml")

			configContent := `
[log]
level = "` + level + `"
`
			err := os.WriteFile(configPath, []byte(configContent), 0644)
			require.NoError(t, err)

			cfg, err := Load(configPath)
			require.NoError(t, err)
			require.NotNil(t, cfg)

			assert.Equal(t, level, cfg.Log.Level)
		})
	}
}

func TestLoadWithAllLogFormats(t *testing.T) {
	formats := []string{"json", "text", "console"}

	for _, format := range formats {
		t.Run(format, func(t *testing.T) {
			tmpDir := t.TempDir()
			configPath := filepath.Join(tmpDir, "log_"+format+".toml")

			configContent := `
[log]
format = "` + format + `"
`
			err := os.WriteFile(configPath, []byte(configContent), 0644)
			require.NoError(t, err)

			cfg, err := Load(configPath)
			require.NoError(t, err)
			require.NotNil(t, cfg)

			assert.Equal(t, format, cfg.Log.Format)
		})
	}
}

func TestValidateWithEmptyConfig(t *testing.T) {
	cfg := &Config{}
	err := cfg.Validate()
	require.NoError(t, err)

	// Check defaults are applied
	assert.Equal(t, "0.0.0.0", cfg.Server.ListenAddress)
	assert.Equal(t, 3374, cfg.Server.Port)
	assert.Equal(t, "info", cfg.Log.Level)
}

func TestValidateWithCustomValues(t *testing.T) {
	cfg := &Config{
		Server: ServerConfig{
			ListenAddress: "127.0.0.1",
			Port:          9999,
		},
		Log: LogConfig{
			Level:  "debug",
			Format: "console",
		},
	}
	err := cfg.Validate()
	require.NoError(t, err)

	// Values should be preserved
	assert.Equal(t, "127.0.0.1", cfg.Server.ListenAddress)
	assert.Equal(t, 9999, cfg.Server.Port)
	assert.Equal(t, "debug", cfg.Log.Level)
	assert.Equal(t, "console", cfg.Log.Format)
}

func TestConfigSaveWithAllSections(t *testing.T) {
	tmpDir := t.TempDir()
	savePath := filepath.Join(tmpDir, "saved_full.toml")

	cfg := &Config{
		Server: ServerConfig{
			ListenAddress: "0.0.0.0",
			Port:          3374,
		},
		Database: DatabaseConfig{
			Path: "/var/lib/sds/sds.db",
		},
		TLS: TLSConfig{
			Enabled:    false,
			CACert:     "",
			ClientCert: "",
			ClientKey:  "",
		},
		Log: LogConfig{
			Level:  "info",
			Format: "json",
		},
		Storage: StorageConfig{
			DefaultPoolType:       "vg",
			DefaultSnapshotSuffix: "_snap",
		},
		Metrics: MetricsConfig{
			Enabled:       true,
			ListenAddress: "0.0.0.0",
			Port:          9433,
		},
	}

	err := cfg.Save(savePath)
	require.NoError(t, err)

	// Verify file exists
	_, err = os.Stat(savePath)
	require.NoError(t, err)

	// Load and verify
	loaded, err := Load(savePath)
	require.NoError(t, err)
	assert.Equal(t, cfg.Server.Port, loaded.Server.Port)
	assert.Equal(t, cfg.Log.Level, loaded.Log.Level)
	assert.Equal(t, cfg.Storage.DefaultPoolType, loaded.Storage.DefaultPoolType)
}

func TestDatabaseConfig(t *testing.T) {
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "db.toml")

	configContent := `
[database]
path = "/custom/db/path.db"
`
	err := os.WriteFile(configPath, []byte(configContent), 0644)
	require.NoError(t, err)

	cfg, err := Load(configPath)
	require.NoError(t, err)
	require.NotNil(t, cfg)

	assert.Equal(t, "/custom/db/path.db", cfg.Database.Path)
}

func TestMetricsConfigPort(t *testing.T) {
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "metrics_port.toml")

	configContent := `
[metrics]
enabled = true
listen_address = "127.0.0.1"
port = 9090
`
	err := os.WriteFile(configPath, []byte(configContent), 0644)
	require.NoError(t, err)

	cfg, err := Load(configPath)
	require.NoError(t, err)
	require.NotNil(t, cfg)

	assert.True(t, cfg.Metrics.Enabled)
	assert.Equal(t, "127.0.0.1", cfg.Metrics.ListenAddress)
	assert.Equal(t, 9090, cfg.Metrics.Port)
}

func TestTLSConfigPaths(t *testing.T) {
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "tls_paths.toml")

	configContent := `
[tls]
enabled = true
ca_cert = "/etc/sds/ca.crt"
client_cert = "/etc/sds/client.crt"
client_key = "/etc/sds/client.key"
`
	err := os.WriteFile(configPath, []byte(configContent), 0644)
	require.NoError(t, err)

	cfg, err := Load(configPath)
	require.NoError(t, err)
	require.NotNil(t, cfg)

	assert.True(t, cfg.TLS.Enabled)
	assert.Equal(t, "/etc/sds/ca.crt", cfg.TLS.CACert)
	assert.Equal(t, "/etc/sds/client.crt", cfg.TLS.ClientCert)
	assert.Equal(t, "/etc/sds/client.key", cfg.TLS.ClientKey)
}
