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
		"sds_resource_set_role", "sds_resource_unmount",
		"sds_snapshot_schedule_delete", "sds_backup_schedule_delete",
	} {
		if operate[name] {
			t.Errorf("%s must not be available to operate", name)
		}
	}
	for _, name := range []string{"sds_resource_mount", "sds_iscsi_chap", "sds_snapshot_schedule_create", "sds_backup_schedule_create"} {
		if !operate[name] {
			t.Errorf("%s must be available to operate", name)
		}
	}

	reads := []string{
		"sds_nfs_export_list", "sds_iscsi_lun_list", "sds_iscsi_initiator_list",
		"sds_iscsi_chap_get", "sds_nvme_namespace_list", "sds_nvme_host_list",
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
