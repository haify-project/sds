package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestInspectDefaults(t *testing.T) {
	path := filepath.Join(t.TempDir(), "c.toml")
	if err := os.WriteFile(path, []byte("[server]\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	want := InspectConfig{Enabled: true, Schedule: "0 1 * * *", Keep: 30, NotifyMin: "warn"}
	if cfg.Inspect != want {
		t.Errorf("defaults = %+v, want %+v", cfg.Inspect, want)
	}
}

func TestInspectValidation(t *testing.T) {
	for _, tc := range []struct {
		cfg  InspectConfig
		want string
	}{
		{InspectConfig{Schedule: "every day"}, "not a cron spec"},
		{InspectConfig{Keep: -1}, "must not be negative"},
		{InspectConfig{NotifyMin: "critical"}, "pass, warn or fail"},
	} {
		c := &Config{Inspect: tc.cfg}
		if err := c.Validate(); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%+v: got %v, want %q", tc.cfg, err, tc.want)
		}
	}
	c := &Config{Inspect: InspectConfig{Schedule: "30 2 * * 1", Keep: 5, NotifyMin: "fail"}}
	if err := c.Validate(); err != nil {
		t.Errorf("valid config refused: %v", err)
	}
}
