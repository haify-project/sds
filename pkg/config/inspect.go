package config

import (
	"fmt"
	"strings"

	"github.com/robfig/cron/v3"
	"github.com/spf13/viper"
)

// InspectConfig controls the scheduled cluster inspection (`haify inspect`).
//
// The schedule rides the snapshot scheduler's cron, so it only runs on the
// active controller and only while [schedule] enabled is true. A manual run
// works either way.
type InspectConfig struct {
	Enabled bool `mapstructure:"enabled"`
	// Schedule is a standard 5-field cron spec in the controller's time zone.
	Schedule string `mapstructure:"schedule"`
	// Keep is how many reports are stored; older ones are dropped.
	Keep int `mapstructure:"keep"`
	// NotifyMin is the least serious outcome that publishes the
	// inspection.completed event: pass (every run), warn, or fail.
	NotifyMin string `mapstructure:"notify_min"`
}

// DefaultInspectKeep is the report history kept when keep is unset.
const DefaultInspectKeep = 30

func inspectDefaults() {
	viper.SetDefault("inspect.enabled", true)
	viper.SetDefault("inspect.schedule", "0 1 * * *")
	viper.SetDefault("inspect.keep", DefaultInspectKeep)
	viper.SetDefault("inspect.notify_min", "warn")
}

// Validate rejects a schedule or threshold the controller could not use.
func (i *InspectConfig) Validate() error {
	if spec := strings.TrimSpace(i.Schedule); spec != "" {
		if _, err := cron.ParseStandard(spec); err != nil {
			return fmt.Errorf("inspect.schedule %q is not a cron spec: %w", spec, err)
		}
	}
	if i.Keep < 0 {
		return fmt.Errorf("inspect.keep %d must not be negative", i.Keep)
	}
	switch strings.TrimSpace(i.NotifyMin) {
	case "", "pass", "warn", "fail":
	default:
		return fmt.Errorf("inspect.notify_min %q must be pass, warn or fail", i.NotifyMin)
	}
	return nil
}
