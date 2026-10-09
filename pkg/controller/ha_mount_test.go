package controller

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestCheckHaMountPointRefusesPathsHaifyMountsOver(t *testing.T) {
	for _, bad := range []string{"/var/lib/haify", "/var/lib/haify/app", "/var/lib/haify-gateway/x", "/var/lib/haify/../haify/app"} {
		assert.Error(t, checkHaMountPoint(bad), bad)
	}
	for _, ok := range []string{"", "/mnt/data", "/var/lib/haifydata", "/srv/app"} {
		assert.NoError(t, checkHaMountPoint(ok), ok)
	}
}
