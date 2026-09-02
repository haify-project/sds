package gateway

import (
	"context"
	"encoding/base64"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

// Tests for taking a gateway in and out of drbd-reactor's hands. They assert on
// the decoded script that reaches the nodes, because the whole point of the
// disable-then-stop ordering (and of base64-wrapping the script) is lost if the
// command is merely well-formed.

func TestManagerStopGateway(t *testing.T) {
	logger := zap.NewNop()
	mockDeployment := &MockDeploymentClient{}
	manager := New(nil, mockDeployment, logger, []string{"node1", "node2"})

	err := manager.StopGateway(context.Background(), "test-resource")
	require.NoError(t, err)
	require.GreaterOrEqual(t, len(mockDeployment.ExecCommands), 3)
	// Stop must disable the reactor config FIRST: a plain systemctl stop is
	// undone within seconds because reactor re-promotes the resource. The
	// script travels base64-encoded to survive dispatch's sh -c "..."
	// quoting, which empties $variables.
	decoded := decodeScriptCommand(t, mockDeployment.ExecCommands[0])
	assert.Contains(t, decoded, `mv "$f" "$f.disabled"`)
	assert.Contains(t, strings.Join(mockDeployment.ExecCommands, "\n"), "reload drbd-reactor")
	assert.Contains(t, strings.Join(mockDeployment.ExecCommands, "\n"), "drbd-services@test\\x2dresource.target")
}

// decodeScriptCommand extracts and decodes the base64 payload from a
// runScript-style command ("echo <b64> | base64 -d | sudo /bin/sh").
func decodeScriptCommand(t *testing.T, cmd string) string {
	t.Helper()
	require.Contains(t, cmd, "base64 -d")
	fields := strings.Fields(cmd)
	require.GreaterOrEqual(t, len(fields), 2)
	raw, err := base64.StdEncoding.DecodeString(fields[1])
	require.NoError(t, err)
	return string(raw)
}

func TestManagerStartGatewayReenablesConfig(t *testing.T) {
	logger := zap.NewNop()
	mockDeployment := &MockDeploymentClient{}
	manager := New(nil, mockDeployment, logger, []string{"node1"})

	err := manager.StartGateway(context.Background(), "test-resource")
	require.NoError(t, err)
	require.GreaterOrEqual(t, len(mockDeployment.ExecCommands), 3)
	// Command 0 flushes stale portblock rules (failback safety); command 1
	// re-enables the config.
	flushed := decodeScriptCommand(t, mockDeployment.ExecCommands[0])
	assert.Contains(t, flushed, "iptables -D INPUT")
	decoded := decodeScriptCommand(t, mockDeployment.ExecCommands[1])
	assert.Contains(t, decoded, `mv "$f.disabled" "$f"`)
}
