package controller

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestLowestFreePort(t *testing.T) {
	assert.Equal(t, uint32(7000), lowestFreePort(nil, 7000))
	assert.Equal(t, uint32(7001), lowestFreePort([]uint32{7000}, 7000))
	assert.Equal(t, uint32(7002), lowestFreePort([]uint32{7000, 7001, 7003}, 7000))
}

func TestParsePortsFromResConfigs(t *testing.T) {
	in := "resource r {\n  on a { address 10.0.0.1:7000; }\n  on b { address ipv4 10.0.0.2:7005; }\n}\n"
	got := parsePortsFromResConfigs(in)
	assert.ElementsMatch(t, []uint32{7000, 7005}, got)
}
