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
	// Both sessions ride an in-memory transport that dies with the test; their
	// close errors describe teardown of a pipe, not the tool list this helper
	// is here to read.
	defer func() { _ = serverSession.Close() }()

	session, err := client.Connect(ctx, ct, nil)
	require.NoError(t, err)
	defer func() { _ = session.Close() }()

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
	names := toolNames(t, newAllowServer("haify_ha_evict"))

	assert.True(t, names["haify_ha_evict"], "the named exception must be reachable")
	assert.True(t, names["haify_pool_list"], "read-only tools stay available")

	// Everything else that mutates must be absent — not hidden, absent.
	for _, forbidden := range []string{
		"haify_pool_delete", "haify_resource_delete", "haify_ha_delete",
		"haify_snapshot_delete", "haify_zfs_pool_delete", "haify_gateway_delete",
		"haify_pool_create", "haify_resource_create",
	} {
		assert.False(t, names[forbidden], "%s must not be registered", forbidden)
	}
}

func TestAllowWriteImpliesReadOnly(t *testing.T) {
	// Without this, "--allow haify_ha_evict" on an otherwise open server would
	// read as granting an exception while actually granting nothing, leaving
	// every destructive tool exposed.
	s := newAllowServer("haify_ha_evict")
	assert.True(t, s.readOnly)

	names := toolNames(t, s)
	assert.False(t, names["haify_pool_delete"])
}

func TestNoAllowListIsUnchanged(t *testing.T) {
	// Read-only alone must keep excluding every mutation.
	ro := toolNames(t, New(&mockExtraClient{}, zap.NewNop(), Options{ReadOnly: true}))
	assert.True(t, ro["haify_pool_list"])
	assert.False(t, ro["haify_ha_evict"])
	assert.False(t, ro["haify_pool_delete"])

	// And a server with neither flag stays fully open.
	open := toolNames(t, New(&mockExtraClient{}, zap.NewNop(), Options{}))
	assert.True(t, open["haify_ha_evict"])
	assert.True(t, open["haify_pool_delete"])
}

func TestAllowWriteAcceptsSeveralNamesAndTrimsThem(t *testing.T) {
	names := toolNames(t, newAllowServer(" haify_ha_evict ", "haify_node_drain", ""))

	assert.True(t, names["haify_ha_evict"])
	assert.True(t, names["haify_node_drain"])
	assert.False(t, names["haify_pool_delete"])
}

func TestUnmatchedAllowedReportsTypos(t *testing.T) {
	// A typo is silent in the worst direction: the operator believes the tool
	// is reachable and finds out at the moment they need it.
	s := newAllowServer("haify_ha_evict", "haify_ha_evict_typo")
	_ = s.MCPServer() // records the names an allowlist can match

	assert.Equal(t, []string{"haify_ha_evict_typo"}, s.UnmatchedAllowed())
}

func TestUnmatchedAllowedIsEmptyWhenEveryNameMatches(t *testing.T) {
	s := newAllowServer("haify_ha_evict")
	_ = s.MCPServer()
	assert.Empty(t, s.UnmatchedAllowed())
}

func TestReadOnlyToolNameInAllowListIsATypo(t *testing.T) {
	// A read-only tool is registered anyway, so naming one in --allow means the
	// operator misunderstood something. It matches no *write* tool, and saying
	// so is more useful than silently accepting it.
	s := newAllowServer("haify_pool_list")
	_ = s.MCPServer()
	assert.Equal(t, []string{"haify_pool_list"}, s.UnmatchedAllowed())
}

func TestRunRefusesToStartOnAnUnmatchedAllowEntry(t *testing.T) {
	s := newAllowServer("haify_no_such_tool")
	err := s.Run(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "haify_no_such_tool")
}
