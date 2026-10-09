package mcpserver

import (
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"go.uber.org/zap"
)

// The remote endpoint is capped at operate, so a tool that interrupts service
// or ends future data protection must be admin-only, and a tool that only
// reads gateway configuration must be reachable by a read token.
func TestRoleTiers(t *testing.T) {
	operate := toolNames(t, New(fullClient{&mockClient{}}, zap.NewNop(), Options{NoDestructive: true}))
	for _, name := range []string{
		"haify_resource_set_role", "haify_resource_unmount",
		"haify_snapshot_schedule_delete", "haify_backup_schedule_delete",
	} {
		if operate[name] {
			t.Errorf("%s must not be available to operate", name)
		}
	}
	for _, name := range []string{"haify_resource_mount", "haify_iscsi_chap", "haify_snapshot_schedule_create", "haify_backup_schedule_create"} {
		if !operate[name] {
			t.Errorf("%s must be available to operate", name)
		}
	}

	reads := []string{
		"haify_nfs_export_list", "haify_iscsi_lun_list", "haify_iscsi_initiator_list",
		"haify_iscsi_chap_get", "haify_nvme_namespace_list", "haify_nvme_host_list",
	}
	read := toolNames(t, New(&mockExtraClient{}, zap.NewNop(), Options{ReadOnly: true}))
	for _, name := range reads {
		if !read[name] {
			t.Errorf("%s must be available to read", name)
		}
	}

	session := connect(t, &mockExtraClient{}, true)
	for _, name := range reads {
		res, err := session.CallTool(t.Context(), &mcp.CallToolParams{Name: name, Arguments: map[string]any{"resource": "gw1"}})
		if err != nil || res.IsError {
			t.Errorf("%s: %v %+v", name, err, res)
		}
	}
}
