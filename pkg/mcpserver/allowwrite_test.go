package mcpserver

import (
	"context"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

// The allowlist exists so an MCP client can be given "everything you can look
// at, plus one thing you can do". The property that matters is that the
// exception is enforced by not registering the other tools: a client-side
// filter constrains one client, while an unregistered tool cannot be called by
// anything that connects.

// toolNames lists what a server actually registers, by asking it the way a
// client would rather than by reading its internals.
func toolNames(t *testing.T, s *Server) map[string]bool {
	t.Helper()
	ctx := context.Background()

	client := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "1"}, nil)
	ct, st := mcp.NewInMemoryTransports()
	serverSession, err := s.MCPServer().Connect(ctx, st, nil)
	require.NoError(t, err)
	defer serverSession.Close()

	session, err := client.Connect(ctx, ct, nil)
	require.NoError(t, err)
	defer session.Close()

	res, err := session.ListTools(ctx, nil)
	require.NoError(t, err)

	names := make(map[string]bool, len(res.Tools))
	for _, tool := range res.Tools {
		names[tool.Name] = true
	}
	return names
}

func newAllowServer(allow ...string) *Server {
	return New(&mockExtraClient{}, zap.NewNop(), Options{AllowWrite: allow})
}

func TestAllowWriteRegistersOnlyTheNamedExceptions(t *testing.T) {
	names := toolNames(t, newAllowServer("sds_ha_evict"))

	assert.True(t, names["sds_ha_evict"], "the named exception must be reachable")
	assert.True(t, names["sds_pool_list"], "read-only tools stay available")

	// Everything else that mutates must be absent — not hidden, absent.
	for _, forbidden := range []string{
		"sds_pool_delete", "sds_resource_delete", "sds_ha_delete",
		"sds_snapshot_delete", "sds_zfs_pool_delete", "sds_gateway_delete",
		"sds_pool_create", "sds_resource_create",
	} {
		assert.False(t, names[forbidden], "%s must not be registered", forbidden)
	}
}

func TestAllowWriteImpliesReadOnly(t *testing.T) {
	// Without this, "--allow sds_ha_evict" on an otherwise open server would
	// read as granting an exception while actually granting nothing, leaving
	// every destructive tool exposed.
	s := newAllowServer("sds_ha_evict")
	assert.True(t, s.readOnly)

	names := toolNames(t, s)
	assert.False(t, names["sds_pool_delete"])
}

func TestNoAllowListIsUnchanged(t *testing.T) {
	// Read-only alone must keep excluding every mutation.
	ro := toolNames(t, New(&mockExtraClient{}, zap.NewNop(), Options{ReadOnly: true}))
	assert.True(t, ro["sds_pool_list"])
	assert.False(t, ro["sds_ha_evict"])
	assert.False(t, ro["sds_pool_delete"])

	// And a server with neither flag stays fully open.
	open := toolNames(t, New(&mockExtraClient{}, zap.NewNop(), Options{}))
	assert.True(t, open["sds_ha_evict"])
	assert.True(t, open["sds_pool_delete"])
}

func TestAllowWriteAcceptsSeveralNamesAndTrimsThem(t *testing.T) {
	names := toolNames(t, newAllowServer(" sds_ha_evict ", "sds_node_drain", ""))

	assert.True(t, names["sds_ha_evict"])
	assert.True(t, names["sds_node_drain"])
	assert.False(t, names["sds_pool_delete"])
}

func TestUnmatchedAllowedReportsTypos(t *testing.T) {
	// A typo is silent in the worst direction: the operator believes the tool
	// is reachable and finds out at the moment they need it.
	s := newAllowServer("sds_ha_evict", "sds_ha_evict_typo")
	_ = s.MCPServer() // records the names an allowlist can match

	assert.Equal(t, []string{"sds_ha_evict_typo"}, s.UnmatchedAllowed())
}

func TestUnmatchedAllowedIsEmptyWhenEveryNameMatches(t *testing.T) {
	s := newAllowServer("sds_ha_evict")
	_ = s.MCPServer()
	assert.Empty(t, s.UnmatchedAllowed())
}

func TestReadOnlyToolNameInAllowListIsATypo(t *testing.T) {
	// A read-only tool is registered anyway, so naming one in --allow means the
	// operator misunderstood something. It matches no *write* tool, and saying
	// so is more useful than silently accepting it.
	s := newAllowServer("sds_pool_list")
	_ = s.MCPServer()
	assert.Equal(t, []string{"sds_pool_list"}, s.UnmatchedAllowed())
}

func TestRunRefusesToStartOnAnUnmatchedAllowEntry(t *testing.T) {
	s := newAllowServer("sds_no_such_tool")
	err := s.Run(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "sds_no_such_tool")
}
