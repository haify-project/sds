// Package config provides configuration management
package config

import (
	"fmt"

	"github.com/spf13/viper"
)

// Config represents the application configuration
type Config struct {
	Server    ServerConfig    `mapstructure:"server"`
	Dispatch  DispatchConfig  `mapstructure:"dispatch"`
	Database  DatabaseConfig  `mapstructure:"database"`
	TLS       TLSConfig       `mapstructure:"tls"`
	Log       LogConfig       `mapstructure:"log"`
	Storage   StorageConfig   `mapstructure:"storage"`
}

// ServerConfig represents server configuration
type ServerConfig struct {
	ListenAddress string `mapstructure:"listen_address"`
	Port          int    `mapstructure:"port"`
}

// DispatchConfig represents dispatch configuration
type DispatchConfig struct {
	ConfigPath string `mapstructure:"config_path"` // Path to dispatch config (~/.dispatch/config.toml)
	Parallel   int    `mapstructure:"parallel"`    // Default parallelism for operations
	Hosts      []string `mapstructure:"hosts"`     // Default hosts for operations
}

// DatabaseConfig represents database configuration
type DatabaseConfig struct {
	Path string `mapstructure:"path"` // Database file path (default: /var/lib/sds/sds.db)
}

// TLSConfig represents TLS configuration
type TLSConfig struct {
	Enabled    bool   `mapstructure:"enabled"`
	CACert     string `mapstructure:"ca_cert"`
	ClientCert string `mapstructure:"client_cert"`
	ClientKey  string `mapstructure:"client_key"`
}

// LogConfig represents logging configuration
type LogConfig struct {
	Level  string `mapstructure:"level"`
	Format string `mapstructure:"format"` // json or text
}

// StorageConfig represents storage configuration
type StorageConfig struct {
	DefaultPoolType     string `mapstructure:"default_pool_type"`
	DefaultSnapshotSuffix string `mapstructure:"default_snapshot_suffix"`
}

// Load loads configuration from file
func Load(configPath string) (*Config, error) {
	// Set defaults
	setDefaults()

	// Read config file
	if configPath != "" {
		viper.SetConfigFile(configPath)
	} else {
		viper.SetConfigName("controller")
		viper.AddConfigPath("/etc/sds/")
		viper.AddConfigPath("./configs/")
		viper.AddConfigPath(".")
	}

	// Enable environment variable override
	viper.SetEnvPrefix("SDS")
	viper.AutomaticEnv()

	if err := viper.ReadInConfig(); err != nil {
		return nil, fmt.Errorf("failed to read config: %w", err)
	}

	var config Config
	if err := viper.Unmarshal(&config); err != nil {
		return nil, fmt.Errorf("failed to unmarshal config: %w", err)
	}

	// Validate config
	if err := config.Validate(); err != nil {
		return nil, fmt.Errorf("invalid config: %w", err)
	}

	return &config, nil
}

// Validate validates the configuration
func (c *Config) Validate() error {
	if c.Server.ListenAddress == "" {
		c.Server.ListenAddress = "0.0.0.0"
	}
	if c.Server.Port == 0 {
		c.Server.Port = 3374
	}
	if c.Dispatch.ConfigPath == "" {
		// Use default dispatch config path
		c.Dispatch.ConfigPath = ""
	}
	if c.Dispatch.Parallel == 0 {
		c.Dispatch.Parallel = 10
	}
	if c.Log.Level == "" {
		c.Log.Level = "info"
	}
	return nil
}

func setDefaults() {
	viper.SetDefault("server.listen_address", "0.0.0.0")
	viper.SetDefault("server.port", 3374)
	viper.SetDefault("dispatch.config_path", "") // Empty means use ~/.ssh/config only
	viper.SetDefault("dispatch.parallel", 10)
	viper.SetDefault("database.path", "/var/lib/sds/sds.db")
	viper.SetDefault("tls.enabled", false)
	viper.SetDefault("log.level", "info")
	viper.SetDefault("log.format", "json")
	viper.SetDefault("storage.default_pool_type", "vg")
	viper.SetDefault("storage.default_snapshot_suffix", "_snap")
}

// Save saves configuration to file
func (c *Config) Save(path string) error {
	config := viper.New()
	config.Set("server", c.Server)
	config.Set("dispatch", c.Dispatch)
	config.Set("database", c.Database)
	config.Set("tls", c.TLS)
	config.Set("log", c.Log)
	config.Set("storage", c.Storage)

	return config.WriteConfigAs(path)
}
