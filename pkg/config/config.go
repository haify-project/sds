// Package config provides configuration management
package config

import (
	"fmt"

	"github.com/spf13/viper"
)

// Config represents the application configuration
type Config struct {
	Server   ServerConfig   `mapstructure:"server"`
	Database DatabaseConfig `mapstructure:"database"`
	Dispatch DispatchConfig `mapstructure:"dispatch"`
	WAN      WANConfig      `mapstructure:"wan"`
	Auth     AuthConfig     `mapstructure:"auth"`
	TLS      TLSConfig      `mapstructure:"tls"`
	Log      LogConfig      `mapstructure:"log"`
	Storage  StorageConfig  `mapstructure:"storage"`
	Metrics  MetricsConfig  `mapstructure:"metrics"`
	UI       UIConfig       `mapstructure:"ui"`
	Audit    AuditConfig    `mapstructure:"audit"`
	RBAC     RBACConfig     `mapstructure:"rbac"`
	Gateway  GatewayConfig  `mapstructure:"gateway"`
	Resource ResourceConfig `mapstructure:"resource"`
	Schedule ScheduleConfig `mapstructure:"schedule"`
	SelfHA   SelfHAConfig   `mapstructure:"self_ha"`
	Alert    AlertConfig    `mapstructure:"alert"`
}


// WANConfig tunes opt-in WAN replication.
//
// PKIDir is where the controller caches the shared CA + leaf used for the
// sds-proxy mTLS link. It defaults to /var/lib/sds/wanproxy-pki, which only a
// root controller can create; point it somewhere writable when the controller
// runs as an ordinary user (development, a test harness, or a packaged service
// with its own state directory).
type WANConfig struct {
	PKIDir string `mapstructure:"pki_dir"`
}

// DispatchConfig points the controller at the dispatch SSH configuration it
// uses to reach storage nodes.
//
// ConfigPath matters whenever the controller does NOT run as a user whose
// `~/.dispatch/config.toml` is the intended one — a systemd unit with a
// different HOME, a non-root operator, or a test harness keeping its config out
// of the home directory. Leave it empty to keep the dispatch default
// (`~/.dispatch/config.toml`, falling back to `~/.ssh/config`).
//
// Parallel caps how many nodes a single dispatch call fans out to; 0 keeps the
// deployment client's built-in default.
type DispatchConfig struct {
	ConfigPath string `mapstructure:"config_path"`
	Parallel   int    `mapstructure:"parallel"`
}

// AlertConfig controls background health polling and Webhook notifications for
// DRBD degraded states (StandAlone, Diskless, Syncing, Loss of Quorum).
type AlertConfig struct {
	Enabled          bool   `mapstructure:"enabled"`
	WebhookURL       string `mapstructure:"webhook_url"`
	CheckIntervalSec int    `mapstructure:"check_interval_sec"`
}

// SelfHAConfig configures controller Self-HA.
type SelfHAConfig struct {
	// ExtraServices are additional systemd units started/stopped alongside the
	// controller on the active node (appended to the Self-HA promoter start
	// list), so they follow the controller across failover. Empty by default;
	// e.g. ["sds-ai.service"] to make the AI Copilot follow the controller.
	ExtraServices []string `mapstructure:"extra_services"`
}

// ScheduleConfig is the master switch for the snapshot scheduler. When enabled,
// the active controller runs cron-driven snapshot schedules and prunes old
// snapshots per each schedule's GFS retention policy. Disabling it stops all
// scheduled snapshots without deleting the schedule definitions.
type ScheduleConfig struct {
	Enabled bool `mapstructure:"enabled"`
}

// ResourceConfig controls DRBD resource provisioning behavior. A 2-node
// resource under quorum=majority cannot keep serving I/O when either node
// fails (the survivor has no majority). When AutoTiebreaker is set, the
// controller automatically adds a third diskless node — one that only votes
// in quorum and stores no data — so the cluster keeps a majority through any
// single-node failure, matching LINSTOR's auto-quorum-tiebreaker behavior.
type ResourceConfig struct {
	AutoTiebreaker bool `mapstructure:"auto_tiebreaker"`
}

// GatewayConfig controls gateway provisioning behavior. Every gateway needs a
// small cluster-private "state" volume (volume 0) in addition to the data
// volume so the OCF agents can carry failover state. When AutoStateVolume is
// set, the controller provisions that volume automatically during gateway
// creation instead of failing when the resource has only one volume.
type GatewayConfig struct {
	AutoStateVolume   bool   `mapstructure:"auto_state_volume"`
	StateVolumeSizeGB uint32 `mapstructure:"state_volume_size_gb"`
}

// RBACConfig controls Casbin-backed role authorization. When enabled, callers
// authenticate with a per-user bearer token and every API call is checked
// against the caller's role. Users and any extra policies are declared here, so
// RBAC needs no extra service or database — it stays inside the controller
// binary. When disabled, the single-token [auth] model applies.
type RBACConfig struct {
	Enabled  bool         `mapstructure:"enabled"`
	Users    []RBACUser   `mapstructure:"users"`
	Policies []RBACPolicy `mapstructure:"policies"`
}

// RBACUser is an identity with a bearer token and a role. Built-in roles are
// admin, operator and viewer; custom role names require matching policies.
type RBACUser struct {
	Name  string `mapstructure:"name"`
	Token string `mapstructure:"token"`
	Role  string `mapstructure:"role"`
}

// RBACPolicy grants a role an action on an object (both may be "*"). These are
// additive on top of the built-in role defaults.
type RBACPolicy struct {
	Role   string `mapstructure:"role"`
	Object string `mapstructure:"object"`
	Action string `mapstructure:"action"`
}

// AuditConfig controls the API audit log. When enabled, every state-changing
// API call is recorded with the caller, target, outcome and latency under the
// "audit" logger. Read-only calls (List/Get/…) are skipped unless
// IncludeReads is set, to keep the audit trail high-signal.
type AuditConfig struct {
	Enabled      bool `mapstructure:"enabled"`
	IncludeReads bool `mapstructure:"include_reads"`
}

// AuthConfig controls API authentication. When enabled, every gRPC and REST
// request must carry "Authorization: Bearer <token>"; only gRPC health
// checks stay open for liveness probes.
type AuthConfig struct {
	Enabled bool   `mapstructure:"enabled"`
	Token   string `mapstructure:"token"`
}

// ServerConfig represents server configuration
type ServerConfig struct {
	ListenAddress string `mapstructure:"listen_address"`
	Port          int    `mapstructure:"port"`
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
	DefaultPoolType       string `mapstructure:"default_pool_type"`
	DefaultSnapshotSuffix string `mapstructure:"default_snapshot_suffix"`
}

// MetricsConfig represents metrics configuration
type MetricsConfig struct {
	Enabled       bool   `mapstructure:"enabled"`
	ListenAddress string `mapstructure:"listen_address"`
	Port          int    `mapstructure:"port"`
}

// UIConfig controls the embedded web UI server. It was documented and written
// into deployed controller.toml files, but nothing read it: the listener was
// hardcoded, so a config asking for a different port bound the default instead
// and said nothing.
type UIConfig struct {
	// Enabled defaults to true — the UI has always been served, and a config
	// that omits the section must keep getting it.
	Enabled       *bool  `mapstructure:"enabled"`
	ListenAddress string `mapstructure:"listen_address"`
	Port          int    `mapstructure:"port"`
}

// UIEnabled reports whether the embedded UI should be served.
func (u UIConfig) UIEnabled() bool { return u.Enabled == nil || *u.Enabled }

// UIAddress returns the address the UI should bind, falling back to the API
// listen address and the historic port.
func (u UIConfig) UIAddress(serverListen string) (string, int) {
	addr, port := u.ListenAddress, u.Port
	if addr == "" {
		addr = serverListen
	}
	if port == 0 {
		port = DefaultUIPort
	}
	return addr, port
}

// DefaultUIPort is the port the UI bound before [ui] was honoured.
const DefaultUIPort = 3376

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
	if c.Log.Level == "" {
		c.Log.Level = "info"
	}
	if c.Auth.Enabled {
		if len(c.Auth.Token) < 16 {
			return fmt.Errorf("auth.token must be at least 16 characters when auth is enabled")
		}
	}
	if c.RBAC.Enabled {
		if len(c.RBAC.Users) == 0 {
			return fmt.Errorf("rbac.users must declare at least one user when rbac is enabled")
		}
		seen := make(map[string]struct{}, len(c.RBAC.Users))
		for i, u := range c.RBAC.Users {
			if u.Name == "" {
				return fmt.Errorf("rbac.users[%d].name is required", i)
			}
			if u.Role == "" {
				return fmt.Errorf("rbac.users[%q].role is required", u.Name)
			}
			if len(u.Token) < 16 {
				return fmt.Errorf("rbac.users[%q].token must be at least 16 characters", u.Name)
			}
			if _, dup := seen[u.Token]; dup {
				return fmt.Errorf("rbac.users[%q] reuses a token assigned to another user", u.Name)
			}
			seen[u.Token] = struct{}{}
		}
	}
	return nil
}

func setDefaults() {
	viper.SetDefault("server.listen_address", "0.0.0.0")
	viper.SetDefault("server.port", 3374)
	viper.SetDefault("database.path", "/var/lib/sds/sds.db")
	viper.SetDefault("auth.enabled", false)
	viper.SetDefault("tls.enabled", false)
	viper.SetDefault("log.level", "info")
	viper.SetDefault("log.format", "json")
	viper.SetDefault("storage.default_pool_type", "vg")
	viper.SetDefault("storage.default_snapshot_suffix", "_snap")
	viper.SetDefault("metrics.enabled", true)
	viper.SetDefault("metrics.listen_address", "0.0.0.0")
	viper.SetDefault("metrics.port", 9433)
	viper.SetDefault("audit.enabled", true)
	viper.SetDefault("audit.include_reads", false)
	viper.SetDefault("rbac.enabled", false)
	viper.SetDefault("gateway.auto_state_volume", true)
	viper.SetDefault("gateway.state_volume_size_gb", 1)
	viper.SetDefault("resource.auto_tiebreaker", true)
	viper.SetDefault("schedule.enabled", true)
	viper.SetDefault("alert.enabled", false)
	viper.SetDefault("alert.check_interval_sec", 30)
}

// Save saves configuration to file
func (c *Config) Save(path string) error {
	config := viper.New()
	config.Set("server", c.Server)
	config.Set("database", c.Database)
	config.Set("auth", c.Auth)
	config.Set("tls", c.TLS)
	config.Set("log", c.Log)
	config.Set("storage", c.Storage)
	config.Set("metrics", c.Metrics)
	config.Set("audit", c.Audit)
	config.Set("rbac", c.RBAC)
	config.Set("gateway", c.Gateway)
	config.Set("resource", c.Resource)
	config.Set("schedule", c.Schedule)
	config.Set("alert", c.Alert)

	return config.WriteConfigAs(path)
}
