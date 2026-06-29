package csi

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestSanitizeResourceName(t *testing.T) {
	cases := []struct{ in, want string }{
		{"pvc-0e8b1f3a-1c2d-4e5f-8a9b-0c1d2e3f4a5b", "pvc_0e8b1f3a_1c2d_4e5f_8a9b_0c1d2e3f4a5b"},
		{"my.vol", "my_vol"},
		{"9starts-with-digit", "v9starts_with_digit"},
	}
	for _, c := range cases {
		assert.Equal(t, c.want, sanitizeResourceName(c.in), "input %q", c.in)
	}
}

func TestSanitizeResourceNameTruncates(t *testing.T) {
	long := ""
	for i := 0; i < 100; i++ {
		long += "a"
	}
	got := sanitizeResourceName(long)
	assert.LessOrEqual(t, len(got), 64)
}
