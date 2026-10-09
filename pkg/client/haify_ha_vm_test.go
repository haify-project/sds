package client

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestVirtualDomainAgent(t *testing.T) {
	a, err := VirtualDomainAgent(" web-01.prod ")
	require.NoError(t, err)
	assert.Equal(t, "heartbeat", a.Provider)
	assert.Equal(t, "VirtualDomain", a.Name)
	assert.Equal(t, "vm_web_01_prod", a.Instance, "an OCF instance id carries no dots or hyphens")
	assert.Equal(t, map[string]string{
		"config":     "/etc/libvirt/qemu/web-01.prod.xml",
		"hypervisor": "qemu:///system",
	}, a.Params)

	// The name ends up in a file path and an unquoted promoter line.
	for _, bad := range []string{"", "a b", "../x", "x/y", "-lead", "a;rm"} {
		_, err := VirtualDomainAgent(bad)
		assert.Error(t, err, bad)
	}
}
