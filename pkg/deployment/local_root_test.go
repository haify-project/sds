package deployment

import (
	"os/exec"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The shim must make every form the commands use run without sudo: a plain
// call, one fed through a pipe, and one inside a nested bash -c script.
func TestAsRootShimRunsSudoCommandsDirectly(t *testing.T) {
	script := asRootShim + `sudo echo plain; echo piped | sudo cat; bash -c 'sudo echo nested'; sudo sh -c 'exit 3'; echo rc=$?`
	out, err := exec.Command("bash", "-c", script).CombinedOutput()
	require.NoError(t, err, string(out))
	assert.Equal(t, "plain\npiped\nnested\nrc=3\n", string(out))
}
