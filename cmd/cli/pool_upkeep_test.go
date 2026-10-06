package main

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestZFSVdevs(t *testing.T) {
	d := []string{"/dev/a", "/dev/b", "/dev/c", "/dev/d"}
	got, err := zfsVdevs("", d)
	assert.NoError(t, err)
	assert.Equal(t, d, got)
	got, _ = zfsVdevs("raid10", d)
	assert.Equal(t, []string{"mirror", "/dev/a", "/dev/b", "mirror", "/dev/c", "/dev/d"}, got)
	got, _ = zfsVdevs("raid6", d)
	assert.Equal(t, append([]string{"raidz2"}, d...), got)

	// An odd disk out of raid10 would silently be left unused.
	_, err = zfsVdevs("raid10", append(d, "/dev/e", "/dev/f", "/dev/g"))
	assert.ErrorContains(t, err, "even")
	_, err = zfsVdevs("raid5", d[:2])
	assert.ErrorContains(t, err, "at least 3")
	_, err = zfsVdevs("raid0", d)
	assert.ErrorContains(t, err, "unknown")
}
