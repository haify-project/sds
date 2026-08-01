package config

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// The section was written into deployed controller.toml files long before
// anything read it, so honouring it must not move the listener for a config
// that never mentioned it.
func TestUIConfigDefaultsMatchTheHardcodedBehaviour(t *testing.T) {
	var u UIConfig
	assert.True(t, u.UIEnabled(), "an absent [ui] section still serves the UI")

	addr, port := u.UIAddress("0.0.0.0")
	assert.Equal(t, "0.0.0.0", addr, "falls back to the API listen address")
	assert.Equal(t, DefaultUIPort, port)
}

func TestUIConfigHonoursExplicitValues(t *testing.T) {
	addr, port := UIConfig{ListenAddress: "127.0.0.1", Port: 3380}.UIAddress("0.0.0.0")
	assert.Equal(t, "127.0.0.1", addr)
	assert.Equal(t, 3380, port)
}

// Enabled is a pointer precisely so "absent" and "false" differ: a plain bool
// would make every config without the section look like a request to turn the
// UI off.
func TestUIConfigCanBeDisabledButAbsenceIsNotDisabled(t *testing.T) {
	off := false
	on := true
	assert.False(t, UIConfig{Enabled: &off}.UIEnabled())
	assert.True(t, UIConfig{Enabled: &on}.UIEnabled())
	assert.True(t, UIConfig{}.UIEnabled())
}
