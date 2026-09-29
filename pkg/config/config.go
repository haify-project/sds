// Package config provides configuration management
package config

import (
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"os"
	"slices"
	"strings"

	"github.com/go-viper/mapstructure/v2"
	"github.com/robfig/cron/v3"
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

// AlertConfig controls background health polling and how the resulting
// notifications are delivered.
//
// Enabling this starts the detector and the event bus. Delivery is separate and
// optional: with no Webhook configured the events are still readable over
// `GET /v1/events`, streamed over `GET /v1/events/watch`, and pushed to browsers
// over `GET /v1/events/stream`.
type AlertConfig struct {
	Enabled bool `mapstructure:"enabled"`
	// WebhookURL is the single-receiver shorthand. Equivalent to one entry in
	// Webhooks; both may be set, and both receive events.
	WebhookURL string `mapstructure:"webhook_url"`
	// WebhookMinSeverity filters WebhookURL: "info" (default), "warning" or
	// "critical".
	WebhookMinSeverity string `mapstructure:"webhook_min_severity"`
	// Webhooks are additional receivers, each with its own severity threshold —
	// a pager on "critical", a chat channel on "info".
	Webhooks         []WebhookReceiver `mapstructure:"webhooks"`
	CheckIntervalSec int               `mapstructure:"check_interval_sec"`
	// CheckNodes enables SSH reachability probing of every registered node on
	// each poll. It is what produces node.unreachable events, and it is the only
	// check that costs a round trip per node, so it has its own switch.
	CheckNodes bool `mapstructure:"check_nodes"`
	// HistorySize is how many past events are retained for late-joining clients.
	// Zero uses the event package default.
	HistorySize int `mapstructure:"history_size"`
	// CheckPools enables thin pool capacity alerting. It rides on the pool
	// listing the controller already serves, so unlike CheckNodes it costs no
	// extra round trip per subject — but it is still a switch, because a
	// cluster that has deliberately overcommitted its pools does not want to be
	// told so every poll.
	CheckPools bool `mapstructure:"check_pools"`
	// PoolNearFullPercent and PoolFullPercent are the thin pool utilisation
	// thresholds, applied to data and metadata alike. Zero uses the alert
	// package defaults (85 and 95).
	PoolNearFullPercent float64 `mapstructure:"pool_near_full_percent"`
	PoolFullPercent     float64 `mapstructure:"pool_full_percent"`
	// WatchDRBDEvents keeps a `drbdsetup events2` stream open to every node,
	// so a DRBD state change is checked within seconds instead of at the next
	// poll, and a healthy cluster is polled every IdleIntervalSec instead of
	// every CheckIntervalSec. Each poll is several SSH sessions per node.
	WatchDRBDEvents bool `mapstructure:"watch_drbd_events"`
	// IdleIntervalSec is the poll interval while every node's events arrive
	// and nothing is degraded or resyncing. It bounds how late what DRBD does
	// not report — a thin pool filling — is noticed.
	IdleIntervalSec int `mapstructure:"idle_interval_sec"`
}

// WebhookReceiver is one HTTP notification target.
type WebhookReceiver struct {
	URL string `mapstructure:"url"`
	// MinSeverity drops anything less severe: "info" (default), "warning",
	// "critical".
	MinSeverity string `mapstructure:"min_severity"`
	// Headers are sent with every request — an auth token, a routing key.
	Headers map[string]string `mapstructure:"headers"`
}

// Receivers returns every configured Webhook, folding the WebhookURL shorthand
// in as the first entry. Receivers without a URL are skipped rather than
// producing a receiver that fails on every delivery.
func (a AlertConfig) Receivers() []WebhookReceiver {
	var out []WebhookReceiver
	if a.WebhookURL != "" {
		out = append(out, WebhookReceiver{
			URL:         a.WebhookURL,
			MinSeverity: a.WebhookMinSeverity,
		})
	}
	for _, w := range a.Webhooks {
		if w.URL != "" {
			out = append(out, w)
		}
	}
	return out
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

// TLSConfig controls transport security for the gRPC API (and, through the
// in-process REST gateway, for everything the web UI calls).
//
// This section used to be pure decoration: nothing in the tree read it, so
// `enabled = true` produced a plaintext listener while the startup log happily
// reported TLS as on — the worst kind of failure, a security switch that
// claims to be closed. Every field below is now read at startup and every
// unusable combination is rejected there, not at the first connection.
//
// The fields are named from the CONTROLLER's point of view, because that is
// who reads them: CertFile/KeyFile are the certificate this server presents.
// The old names (ca_cert / client_cert / client_key) described a *client*, and
// a config still carrying them is rejected rather than reinterpreted — see
// Validate. The client-side spellings live on in pkg/client, where they are
// correct.
type TLSConfig struct {
	Enabled bool `mapstructure:"enabled"`
	// CertFile and KeyFile are the server certificate chain and its private
	// key, PEM encoded. Both are required when Enabled.
	CertFile string `mapstructure:"cert_file"`
	KeyFile  string `mapstructure:"key_file"`
	// ClientCAFile is the CA bundle that signs client certificates. Setting it
	// is what turns on mutual TLS: the server then requires and verifies a
	// client certificate on every connection. There is deliberately no
	// separate "require_client_cert" switch — a CA configured but not enforced
	// is exactly the kind of security setting that looks on while being off.
	ClientCAFile string `mapstructure:"client_ca_file"`

	// Legacy client-viewpoint names. They are still decoded so that a config
	// which sets them fails loudly at startup instead of being silently
	// ignored (which, for three releases, is what happened to the whole
	// section). They are never used for anything else.
	LegacyCACert     string `mapstructure:"ca_cert,omitempty"`
	LegacyClientCert string `mapstructure:"client_cert,omitempty"`
	LegacyClientKey  string `mapstructure:"client_key,omitempty"`
}

// MutualTLS reports whether client certificates are required and verified.
func (t TLSConfig) MutualTLS() bool { return t.Enabled && t.ClientCAFile != "" }

// Validate rejects a [tls] section that cannot work, at startup rather than at
// the first connection. A controller that boots "with TLS" and only discovers
// at the first handshake that its key does not match its certificate has
// already told the operator it is secure.
func (t TLSConfig) Validate() error {
	if t.LegacyCACert != "" || t.LegacyClientCert != "" || t.LegacyClientKey != "" {
		return fmt.Errorf("tls: ca_cert/client_cert/client_key name a *client's* material and are no longer read; " +
			"the controller needs its own certificate — set tls.cert_file and tls.key_file, " +
			"and tls.client_ca_file to require client certificates. " +
			"(sds-cli keeps --tls-ca/--tls-cert/--tls-key for the client side.)")
	}
	if !t.Enabled {
		return nil
	}
	if t.CertFile == "" || t.KeyFile == "" {
		return fmt.Errorf("tls.cert_file and tls.key_file are required when tls.enabled is true")
	}
	// LoadX509KeyPair is the check, not a formality: it catches a missing or
	// unreadable file, a PEM block that is not a certificate, and — the one an
	// existence check would wave through — a key that does not belong to the
	// certificate.
	if _, err := tls.LoadX509KeyPair(t.CertFile, t.KeyFile); err != nil {
		return fmt.Errorf("tls: cannot load cert_file %q with key_file %q: %w", t.CertFile, t.KeyFile, err)
	}
	if t.ClientCAFile != "" {
		pem, err := os.ReadFile(t.ClientCAFile)
		if err != nil {
			return fmt.Errorf("tls: cannot read client_ca_file %q: %w", t.ClientCAFile, err)
		}
		// An unparsable bundle would otherwise produce an empty pool, and an
		// empty ClientCAs pool rejects every client — mutual TLS that locks
		// out the whole cluster instead of failing to start.
		if !x509.NewCertPool().AppendCertsFromPEM(pem) {
			return fmt.Errorf("tls: client_ca_file %q contains no PEM certificate", t.ClientCAFile)
		}
	}
	return nil
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
	// VerifySchedule is a cron spec on which every resource's replicas are
	// compared block by block (DRBD online verify), one resource at a time.
	// Differences raise resource.out_of_sync. Empty turns it off.
	VerifySchedule string `mapstructure:"verify_schedule"`
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
// validPoolTypeDefaults are the values storage.default_pool_type may take.
//
// ZFS is deliberately absent even though `sds-cli pool create --type zfs` works:
// a zpool is built from vdevs by a separate RPC, so an unspecified type never
// resolves to one. Accepting "zfs" here would produce a controller that starts
// cleanly and then fails every pool creation that omits a type, with an error
// about LVM that names a setting the operator wrote on purpose.
var validPoolTypeDefaults = []string{"vg", "lvm", "lvm-thin", "thin-pool", "thin_pool"}

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
	if t := strings.ToLower(strings.TrimSpace(c.Storage.DefaultPoolType)); t != "" {
		if !slices.Contains(validPoolTypeDefaults, t) {
			return fmt.Errorf("storage.default_pool_type %q is not a pool type this controller can create (want one of %s)",
				c.Storage.DefaultPoolType, strings.Join(validPoolTypeDefaults, ", "))
		}
	}

	if spec := strings.TrimSpace(c.Storage.VerifySchedule); spec != "" {
		if _, err := cron.ParseStandard(spec); err != nil {
			return fmt.Errorf("storage.verify_schedule %q is not a cron spec: %w", spec, err)
		}
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
	if err := c.TLS.Validate(); err != nil {
		return err
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
	// Thin by default: a thick LVM pool reserves a fixed COW area per snapshot,
	// so it cannot hold a retention history. See cmd/cli/pool.go for the numbers.
	// Thin by default. A thick pool cannot hold a snapshot history: LVM makes
	// every snapshot reserve a fixed COW area up front (SDS reserves 20% of the
	// origin), so a 10 GiB pool holding a 6 GiB volume fits two snapshots —
	// which is not a retention policy. Thin snapshots cost only the blocks that
	// diverge. Set "vg" here for the thick behaviour on every client at once.
	viper.SetDefault("storage.default_pool_type", "thin_pool")
	viper.SetDefault("storage.default_snapshot_suffix", "_snap")
	// Monthly, 03:00 on the 1st. Two replicas that disagree both report
	// UpToDate; nothing but reading both finds it.
	viper.SetDefault("storage.verify_schedule", "0 3 1 * *")
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
	viper.SetDefault("alert.check_nodes", true)
	viper.SetDefault("alert.watch_drbd_events", true)
	viper.SetDefault("alert.idle_interval_sec", 300)
	viper.SetDefault("alert.history_size", 500)
	// On by default: a thin pool filling up is silent until it is fatal, and it
	// costs nothing extra to watch.
	viper.SetDefault("alert.check_pools", true)
	viper.SetDefault("alert.pool_near_full_percent", 85.0)
	viper.SetDefault("alert.pool_full_percent", 95.0)
}

// Save saves configuration to file
// Save writes the configuration back out.
//
// Sections go through mapstructure rather than straight into viper.Set: viper
// serialises a struct under its Go field names ("DefaultPoolType"), while Load
// reads the mapstructure tags ("default_pool_type"). Setting the struct
// directly produces a file that looks right and silently loses every value on
// the next Load — every section, not just one. mapstructure.Decode turns the
// struct into a map keyed by those same tags, so what is written is what is
// read back.
func (c *Config) Save(path string) error {
	config := viper.New()
	sections := []struct {
		name string
		v    any
	}{
		{"server", c.Server}, {"database", c.Database}, {"auth", c.Auth},
		{"tls", c.TLS}, {"log", c.Log}, {"storage", c.Storage},
		{"metrics", c.Metrics}, {"audit", c.Audit}, {"rbac", c.RBAC},
		{"gateway", c.Gateway}, {"resource", c.Resource},
		{"schedule", c.Schedule}, {"alert", c.Alert},
	}
	for _, s := range sections {
		var m map[string]any
		if err := mapstructure.Decode(s.v, &m); err != nil {
			return fmt.Errorf("encode %s section: %w", s.name, err)
		}
		config.Set(s.name, m)
	}

	return config.WriteConfigAs(path)
}
