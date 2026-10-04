package config

import (
	"fmt"
	"net"
	"net/url"
	"strings"

	"github.com/spf13/viper"
)

// AuditConfig controls the API audit log. When enabled, every state-changing
// API call is recorded with the caller, target, outcome and latency under the
// "audit" logger. Read-only calls (List/Get/…) are skipped unless
// IncludeReads is set, to keep the audit trail high-signal.
//
// The trail is kept in the controller database for RetentionDays, bounded by
// MaxEntries so a flood of calls cannot fill the metadata volume. Whoever
// holds the controller can rewrite that database, so a trail that has to
// survive an attacker is sent off the cluster as it is written — to Syslog,
// WebhookURL or both — onto storage the attacker does not hold.
type AuditConfig struct {
	Enabled      bool `mapstructure:"enabled"`
	IncludeReads bool `mapstructure:"include_reads"`
	// RetentionDays is how long entries stay in the database.
	RetentionDays int `mapstructure:"retention_days"`
	// MaxEntries caps the database trail whatever its age. Dropping an entry
	// younger than RetentionDays to stay under it raises audit.truncated.
	MaxEntries int `mapstructure:"max_entries"`
	// Syslog is "udp://host:514", "tcp://host:514" or "tls://host:6514".
	Syslog string `mapstructure:"syslog"`
	// SyslogCA is a PEM bundle for a tls:// Syslog; empty uses the system's.
	SyslogCA string `mapstructure:"syslog_ca"`
	// WebhookURL receives each batch of entries as a JSON POST.
	WebhookURL string `mapstructure:"webhook_url"`
	// WebhookToken, when set, is sent as "Authorization: Bearer <token>".
	WebhookToken string `mapstructure:"webhook_token"`
}

func setAuditDefaults() {
	viper.SetDefault("audit.enabled", true)
	viper.SetDefault("audit.include_reads", false)
	viper.SetDefault("audit.retention_days", 180)
	viper.SetDefault("audit.max_entries", 200000)
}

// SyslogTarget splits Syslog into a network ("udp", "tcp", "tls") and address.
func (a AuditConfig) SyslogTarget() (network, addr string, err error) {
	raw := strings.TrimSpace(a.Syslog)
	if raw == "" {
		return "", "", nil
	}
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return "", "", fmt.Errorf("audit.syslog %q is not a udp://, tcp:// or tls:// address", raw)
	}
	switch u.Scheme {
	case "udp", "tcp", "tls":
	default:
		return "", "", fmt.Errorf("audit.syslog %q: the scheme must be udp, tcp or tls", raw)
	}
	addr = u.Host
	if u.Port() == "" {
		port := "514"
		if u.Scheme == "tls" {
			port = "6514"
		}
		addr = net.JoinHostPort(u.Hostname(), port)
	}
	return u.Scheme, addr, nil
}

func (a *AuditConfig) validate() error {
	if a.RetentionDays < 0 {
		return fmt.Errorf("audit.retention_days must not be negative")
	}
	if a.MaxEntries < 0 {
		return fmt.Errorf("audit.max_entries must not be negative")
	}
	if _, _, err := a.SyslogTarget(); err != nil {
		return err
	}
	if raw := strings.TrimSpace(a.WebhookURL); raw != "" {
		u, err := url.Parse(raw)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
			return fmt.Errorf("audit.webhook_url %q is not an http:// or https:// URL", raw)
		}
	}
	return nil
}
