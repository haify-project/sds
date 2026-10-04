package config

import (
	"fmt"

	"github.com/spf13/viper"
)

// WriteAnomalyConfig is [alert.write_anomaly]: watching each resource's write
// rate for the wholesale rewrite that encryption by ransomware looks like.
//
// It rides the health poll, so it runs only with [alert] enabled. Each
// resource learns its own normal write rate, by hour of the week; a rate
// Factor times that, and at least MinMBps, for two polls in a row raises
// resource.write_anomaly, freezes the resource's snapshot schedule for
// FreezeHours and takes a snapshot. It cannot tell an attack from a bulk
// import, a reindex or a restore — it says that the volume is being
// rewritten unusually fast, and keeps the history from before it.
type WriteAnomalyConfig struct {
	Enabled     bool    `mapstructure:"enabled"`
	Factor      float64 `mapstructure:"factor"`
	MinMBps     float64 `mapstructure:"min_mbps"`
	FreezeHours int     `mapstructure:"freeze_hours"`
}

func setWriteAnomalyDefaults() {
	viper.SetDefault("alert.write_anomaly.enabled", true)
	viper.SetDefault("alert.write_anomaly.factor", 5.0)
	viper.SetDefault("alert.write_anomaly.min_mbps", 20.0)
	viper.SetDefault("alert.write_anomaly.freeze_hours", 168)
}

func (w *WriteAnomalyConfig) validate() error {
	if !w.Enabled {
		return nil
	}
	if w.Factor <= 1 {
		return fmt.Errorf("alert.write_anomaly.factor must be above 1 (it is how many times the usual rate counts as unusual)")
	}
	if w.MinMBps < 0 {
		return fmt.Errorf("alert.write_anomaly.min_mbps must not be negative")
	}
	if w.FreezeHours < 0 || w.FreezeHours > 365*24 {
		return fmt.Errorf("alert.write_anomaly.freeze_hours must be between 0 and %d", 365*24)
	}
	return nil
}
