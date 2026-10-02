package gateway

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"go.uber.org/zap"
)

func TestGatewayManagerHostsAndList(t *testing.T) {
	m := New(nil, nil, zap.NewNop(), nil)
	m.SetHosts([]string{"10.0.0.1", "10.0.0.2"})
	assert.Equal(t, []string{"10.0.0.1", "10.0.0.2"}, m.Hosts())

	// ListGateways with no DB returns empty slice
	_, _ = m.ListGateways(context.Background())

}

func TestGatewayConfigHelpersExtra(t *testing.T) {
	lines, nl := splitConfigLines("a\nb\nc\n")
	assert.True(t, nl)
	assert.Equal(t, []string{"a", "b", "c"}, lines)
	assert.Equal(t, "a\nb\nc\n", joinConfigLines(lines, nl))

	matchB := func(s string) bool { return s == "b" }
	assert.Equal(t, 1, findLineIndex(lines, matchB))
	assert.Equal(t, -1, findLineIndex(lines, func(s string) bool { return s == "z" }))

	inserted, err := insertLineBefore(lines, "inserted", matchB)
	assert.NoError(t, err)
	assert.Equal(t, []string{"a", "inserted", "b", "c"}, inserted)

	removed, ok := removeLine(lines, matchB)
	assert.True(t, ok)
	assert.Equal(t, []string{"a", "c"}, removed)

	vals := uniqueSortedValues([]string{"b", "a", "b", "c"})
	assert.Equal(t, []string{"a", "b", "c"}, vals)

	valsWithoutB := removeValue(vals, "b")
	assert.Equal(t, []string{"a", "c"}, valsWithoutB)

	val, err := parseIntParam(map[string]string{"k": "123"}, "k")
	assert.NoError(t, err)
	assert.Equal(t, 123, val)
}

func TestGatewayMoreHelpers(t *testing.T) {
	assert.Equal(t, "192.168.1.0/24", nfsFormatCIDR("192.168.1.0/24"))
	assert.Equal(t, "192.168.1.100", nfsFormatCIDR("192.168.1.100"))

	ip, port, err := parsePortal("10.0.0.1:3260")
	assert.NoError(t, err)
	assert.Equal(t, "10.0.0.1", ip)
	assert.Equal(t, 3260, port)

	minor := parseDeviceMinorFromConfig("volume 0 {\n  device minor 105;\n}")
	assert.Equal(t, 105, minor)

	minor = parseDeviceMinorFromConfig("no minor here")
	assert.Equal(t, -1, minor)
}

func TestGatewayConfigHelpersMore(t *testing.T) {
	content := `ocf:heartbeat:IPaddr2 p_vip ip=10.0.0.1 cidr_netmask=24`

	params := parseOCFParams(content, 2)
	assert.Equal(t, "10.0.0.1", params["ip"])
	assert.Equal(t, "24", params["cidr_netmask"])

	// checkGatewayPrereqs empty nodes
	m := New(nil, nil, zap.NewNop(), nil)
	assert.NoError(t, m.checkGatewayPrereqs(context.Background(), nil, gatewayPrereqs{}))
}
