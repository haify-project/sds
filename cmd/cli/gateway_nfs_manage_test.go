package main

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestGatewayExportDirectory(t *testing.T) {
	assert.Equal(t, "/srv/gateway-exports/data/mysql", gatewayExportDirectory("data", "/mysql"))
	assert.Equal(t, "/srv/gateway-exports/data/mysql", gatewayExportDirectory("data", "/srv/gateway-exports/data/mysql"))
	assert.Equal(t, "/srv/gateway-exports/data", gatewayExportDirectory("data", ""))
}

func TestBuildNFSMountArgs(t *testing.T) {
	args := buildNFSMountArgs("10.0.0.10", "/srv/gateway-exports/data/mysql", "/mnt/mysql", "vers=4.2")
	assert.Equal(t, []string{
		"mount",
		"-t",
		"nfs",
		"-o",
		"vers=4.2",
		"10.0.0.10:/srv/gateway-exports/data/mysql",
		"/mnt/mysql",
	}, args)
}
