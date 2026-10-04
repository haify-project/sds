package controller

import (
	"context"
	"encoding/base64"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	sdspb "github.com/haify-project/sds/api/proto/v1"
	"github.com/haify-project/sds/pkg/database"
)

// The VIP is the last thing up and the first thing down, as in every gateway:
// before, failover sent clients to a node whose service was still starting.
func TestLegacyPromoterOrderStartsTheVIPLast(t *testing.T) {
	rm := NewResourceManager(newBasicTestController(&fakeDeploymentClient{}))
	cfg := rm.generatePromoterConfig("app", []string{"app.service"}, "/srv/app", "10.0.0.50/24",
		[]OcfAgentSpec{{Provider: "heartbeat", Name: "Dummy", Instance: "d"}})

	mount := strings.Index(cfg, "srv-app.mount")
	svc := strings.Index(cfg, "app.service")
	ocf := strings.Index(cfg, "ocf:heartbeat:Dummy")
	vip := strings.Index(cfg, "service-ip@")
	require.True(t, mount >= 0 && svc >= 0 && ocf >= 0 && vip >= 0, cfg)
	assert.Less(t, mount, svc)
	assert.Less(t, svc, ocf)
	assert.Less(t, ocf, vip, "the VIP comes after everything it fronts")
}

// A service bound to the VIP must still start before the VIP is up.
func TestNonlocalBindIsPersisted(t *testing.T) {
	dep := &fakeDeploymentClient{}
	ctrl := newBasicTestController(dep)
	require.NoError(t, ctrl.resources.ensureNonlocalBind(context.Background(), []string{"10.0.0.1"}))
	calls := execCmdsMatching(dep, "base64 -d")
	require.Len(t, calls, 1)
	script := decodeB64Script(t, calls[0].cmd)
	assert.Contains(t, script, "/etc/sysctl.d/90-sds-ha-vip.conf")
	assert.Contains(t, script, "net.ipv4.ip_nonlocal_bind = 1")
}

// The record kept everything but the OCF agents and the explicit order, so a
// config could be neither shown nor made again from it.
func TestHaConfigRecordKeepsAgentsAndOrder(t *testing.T) {
	ctrl := newBasicTestController(&fakeDeploymentClient{})
	ctrl.db = newTestDB(t)
	ctx := context.Background()
	agents := []OcfAgentSpec{{Provider: "heartbeat", Name: "IPaddr2", Instance: "vip", Params: map[string]string{"ip": "10.0.0.5"}}}
	items := []HaStartItem{{SystemdUnit: "a.service"}, {Ocf: &agents[0]}}
	require.NoError(t, ctrl.db.SaveHaConfig(ctx, &database.HaConfig{
		Resource: "r", OcfAgents: ocfAgentRecords(agents), StartItems: startItemRecords(items),
	}))

	got, err := ctrl.db.GetHaConfig(ctx, "r")
	require.NoError(t, err)
	info := haConfigInfo(got)
	require.Len(t, info.OcfAgents, 1)
	assert.Equal(t, "10.0.0.5", info.OcfAgents[0].Params["ip"])
	require.Len(t, info.StartItems, 2)
	assert.Equal(t, "a.service", info.StartItems[0].GetSystemdUnit())
	assert.Equal(t, "IPaddr2", info.StartItems[1].GetOcf().GetName())
	assert.IsType(t, &sdspb.HaStartItem_Ocf{}, info.StartItems[1].Item)
}

// decodeB64Script returns the script an "echo <b64> | base64 -d | ..." command runs.
func decodeB64Script(t *testing.T, cmd string) string {
	t.Helper()
	b64, _, ok := strings.Cut(strings.TrimPrefix(cmd, "echo "), " | base64 -d")
	require.True(t, ok, cmd)
	raw, err := base64.StdEncoding.DecodeString(b64)
	require.NoError(t, err)
	return string(raw)
}
