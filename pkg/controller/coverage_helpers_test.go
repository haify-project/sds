package controller

import (
	"context"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/liliang-cn/sds/pkg/database"
	sdsui "github.com/liliang-cn/sds/ui"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

func TestGatewayServiceHost(t *testing.T) {
	assert.Equal(t, "", gatewayServiceHost(""))
	assert.Equal(t, "", gatewayServiceHost("   "))
	assert.Equal(t, "192.168.1.10", gatewayServiceHost("192.168.1.10/24"))
	assert.Equal(t, "10.0.0.5", gatewayServiceHost("10.0.0.5"))
	// Not a valid CIDR but has a slash -> cut before slash.
	assert.Equal(t, "host.example", gatewayServiceHost("host.example/thing"))
}

func TestGatewayExportDirectory(t *testing.T) {
	// Empty export path -> base/<resource>.
	assert.Equal(t, filepath.Join(gwBase(), "data"), gatewayExportDirectory("data", ""))
	// Already under the base path -> returned unchanged.
	under := filepath.Join(gwBase(), "data", "exp")
	assert.Equal(t, under, gatewayExportDirectory("data", under))
	// Relative/absolute path outside base -> joined under base/<resource>.
	assert.Equal(t, filepath.Join(gwBase(), "data", "sub"), gatewayExportDirectory("data", "/sub"))
}

func gwBase() string {
	// Mirror gatewayExportDirectory's base by round-tripping a known input.
	full := gatewayExportDirectory("res", "")
	return strings.TrimSuffix(full, string(filepath.Separator)+"res")
}

type gwStringer struct{}

func (gwStringer) String() string { return "stringer-val" }

func TestStringifyGatewayValue(t *testing.T) {
	cases := []struct {
		in   any
		want string
	}{
		{nil, ""},
		{"plain", "plain"},
		{gwStringer{}, "stringer-val"},
		{[]string{"a", "b"}, "a,b"},
		{[]any{"a", "", "c"}, "a,c"},
		{map[string]string{"b": "2", "a": "1"}, "a=1,b=2"},
		{map[string]any{"z": "9", "a": ""}, "z=9"},
		{true, "true"},
		{false, "false"},
		{int(7), "7"},
		{int32(8), "8"},
		{int64(9), "9"},
		{uint(1), "1"},
		{uint32(2), "2"},
		{uint64(3), "3"},
		{float32(1.5), "1.5"},
		{float64(2.25), "2.25"},
	}
	for _, c := range cases {
		assert.Equalf(t, c.want, stringifyGatewayValue(c.in), "input %v", c.in)
	}
}

func TestFormatGatewayNodeStates(t *testing.T) {
	assert.Equal(t, "", formatGatewayNodeStates(nil))
	assert.Equal(t, "", formatGatewayNodeStates(map[string]*ResourceNodeState{}))

	states := map[string]*ResourceNodeState{
		"n2": {Role: "Secondary", DiskState: "UpToDate", Replication: "Established"},
		"n1": {Role: "Primary", DiskState: "UpToDate", Replication: ""},
		"n3": nil, // nil state is skipped
	}
	got := formatGatewayNodeStates(states)
	assert.Equal(t, "n1:Primary/UpToDate/,n2:Secondary/UpToDate/Established", got)
}

func TestGatewayInfoFromDBRecord(t *testing.T) {
	assert.Nil(t, gatewayInfoFromDBRecord(nil))

	rec := &database.Gateway{Name: "gw1", Resource: "res1", Type: database.GatewayType("nfs")}
	info := gatewayInfoFromDBRecord(rec)
	require.NotNil(t, info)
	assert.Equal(t, "res1", info.ID)
	assert.Equal(t, "gw1", info.Name)
	assert.Equal(t, "nfs", info.Type)
	assert.Equal(t, "res1", info.Resource)
}

func TestServerGatewayConfigOptions(t *testing.T) {
	dep := &fakeDeploymentClient{}
	ctrl := newBasicTestController(dep)
	srv := NewServer(ctrl)
	ctx := context.Background()

	// No DB -> empty options.
	assert.Empty(t, srv.gatewayConfigOptions(ctx, "res1"))

	ctrl.db = newTestDB(t)
	require.NoError(t, ctrl.db.SaveGateway(ctx, &database.Gateway{
		Name:     "gw1",
		Resource: "res1",
		Type:     database.GatewayType("nfs"),
		Config: map[string]any{
			"export_directory": "/exports/res1",
			"nested":           map[string]any{"sub": "v", "blank": ""},
			"blankTop":         "",
		},
		Status:     "started",
		ActiveNode: "n1",
	}))

	opts := srv.gatewayConfigOptions(ctx, "res1")
	assert.Equal(t, "/exports/res1", opts["export_directory"])
	assert.Equal(t, "v", opts["nested.sub"])
	assert.NotContains(t, opts, "nested.blank")
	assert.NotContains(t, opts, "blankTop")
	assert.Equal(t, "started", opts["gateway_status"])
	assert.Equal(t, "n1", opts["active_node"])

	// Missing gateway -> empty.
	assert.Empty(t, srv.gatewayConfigOptions(ctx, "does-not-exist"))
}

func TestServerGatewayInfoLookups(t *testing.T) {
	dep := &fakeDeploymentClient{}
	ctrl := newBasicTestController(dep)
	ctrl.db = newTestDB(t)
	srv := NewServer(ctrl)
	ctx := context.Background()

	// Not found before any record exists.
	_, err := srv.getGatewayInfo(ctx, "res1")
	assert.Error(t, err)

	// Empty list.
	list, err := srv.listGatewayInfos(ctx)
	require.NoError(t, err)
	assert.Empty(t, list)

	require.NoError(t, ctrl.db.SaveGateway(ctx, &database.Gateway{
		Name: "gw1", Resource: "res1", Type: database.GatewayType("iscsi"),
	}))

	info, err := srv.getGatewayInfo(ctx, "res1")
	require.NoError(t, err)
	assert.Equal(t, "res1", info.Resource)
	assert.Equal(t, "iscsi", info.Type)

	list, err = srv.listGatewayInfos(ctx)
	require.NoError(t, err)
	assert.Len(t, list, 1)
}

func TestServerGatewayRuntimeInfoNoResource(t *testing.T) {
	dep := &fakeDeploymentClient{}
	ctrl := newBasicTestController(dep)
	ctrl.db = newTestDB(t)
	srv := NewServer(ctrl)

	// Resource does not exist -> defaults, "configured".
	state, node, opts := srv.gatewayRuntimeInfo(context.Background(), "missing")
	assert.Equal(t, "configured", state)
	assert.Equal(t, "", node)
	assert.Empty(t, opts)
}

func TestServerEnrichGatewayInfoFallsBackToDBStatus(t *testing.T) {
	dep := &fakeDeploymentClient{}
	ctrl := newBasicTestController(dep)
	ctrl.db = newTestDB(t)
	srv := NewServer(ctrl)
	ctx := context.Background()

	require.NoError(t, ctrl.db.SaveGateway(ctx, &database.Gateway{
		Name:     "gw1",
		Resource: "res1",
		Type:     database.GatewayType("nfs"),
		Config:   map[string]any{"export_directory": "/exports/res1"},
		Status:   "stopped",
	}))

	gw, err := srv.getGatewayInfo(ctx, "res1")
	require.NoError(t, err)

	pb := srv.enrichGatewayInfo(ctx, gw)
	assert.Equal(t, "res1", pb.Resource)
	// No live/Primary resource -> state falls back to DB gateway_status.
	assert.Equal(t, "stopped", pb.State)
	// NFS path is populated from export_directory.
	assert.Equal(t, "/exports/res1", pb.Path)
}

func TestUIServerServeHTTP(t *testing.T) {
	ui, err := NewUIServer(zap.NewNop(), "127.0.0.1", 0)
	require.NoError(t, err)

	// Root -> index.html.
	rr := httptest.NewRecorder()
	ui.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/", nil))
	assert.Equal(t, http.StatusOK, rr.Code)
	assert.Contains(t, rr.Header().Get("Content-Type"), "text/html")
	assert.Equal(t, "*", rr.Header().Get("Access-Control-Allow-Origin"))

	// Unknown path -> SPA fallback to index.html.
	rr = httptest.NewRecorder()
	ui.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/some/spa/route", nil))
	assert.Equal(t, http.StatusOK, rr.Code)

	// OPTIONS -> 204 no content.
	rr = httptest.NewRecorder()
	ui.ServeHTTP(rr, httptest.NewRequest(http.MethodOptions, "/", nil))
	assert.Equal(t, http.StatusNoContent, rr.Code)

	// Serve real static assets to exercise the content-type branches.
	for ext, wantCT := range map[string]string{
		".js": "javascript", ".css": "css", ".svg": "svg", ".html": "text/html",
	} {
		p := findEmbeddedAsset(t, ext)
		if p == "" {
			continue
		}
		rr = httptest.NewRecorder()
		ui.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/"+p, nil))
		assert.Equalf(t, http.StatusOK, rr.Code, "asset %s", p)
		assert.Containsf(t, rr.Header().Get("Content-Type"), wantCT, "asset %s", p)
	}
}

// findEmbeddedAsset walks the embedded UI dist for a file with the given
// extension and returns its path relative to dist ("" if none exists).
func findEmbeddedAsset(t *testing.T, ext string) string {
	t.Helper()
	distFS, err := fs.Sub(sdsui.FS, "dist")
	require.NoError(t, err)
	var found string
	_ = fs.WalkDir(distFS, ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || found != "" {
			return nil
		}
		if strings.HasSuffix(p, ext) {
			found = p
		}
		return nil
	})
	return found
}
