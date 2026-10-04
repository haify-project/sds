package deployment

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The options used to be accepted and dropped: a pool asked to compress did
// whatever OpenZFS defaulted to.
func TestZFSCreateFlagsApplyTheOptions(t *testing.T) {
	var o zfsOptions
	WithZFSCompression("zstd-3")(&o)
	WithZFSDedup(true)(&o)
	flags, err := o.createFlags()
	require.NoError(t, err)
	assert.Equal(t, " -O compression=zstd-3 -O dedup=on", flags)

	flags, err = zfsOptions{}.createFlags()
	require.NoError(t, err)
	assert.Equal(t, "", flags, "nothing asked, the OpenZFS defaults stand")

	_, err = zfsOptions{compression: "lz4; rm -rf /"}.createFlags()
	require.Error(t, err, "the value goes into a shell command, so only known algorithms pass")
}

func TestValidZFSCompression(t *testing.T) {
	for _, ok := range []string{"on", "off", "lz4", "zstd", "zstd-1", "zstd-19", "zstd-fast", "zstd-fast-10", "gzip", "gzip-9", "lzjb", "zle"} {
		assert.True(t, ValidZFSCompression(ok), ok)
	}
	for _, bad := range []string{"", "zstd-20", "gzip-10", "LZ4", "lz4 ", "snappy"} {
		assert.False(t, ValidZFSCompression(bad), bad)
	}
}
