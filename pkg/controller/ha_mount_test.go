package controller

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestCheckHaMountPointRefusesPathsSDSMountsOver(t *testing.T) {
	for _, bad := range []string{"/var/lib/sds", "/var/lib/sds/app", "/var/lib/sds-gateway/x", "/var/lib/sds/../sds/app"} {
		assert.Error(t, checkHaMountPoint(bad), bad)
	}
	for _, ok := range []string{"", "/mnt/data", "/var/lib/sdsdata", "/srv/app"} {
		assert.NoError(t, checkHaMountPoint(ok), ok)
	}
}
