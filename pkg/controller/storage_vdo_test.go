package controller

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"

	"github.com/haify-project/sds/pkg/database"
)

func TestNormalizeVDOPoolType(t *testing.T) {
	for _, in := range []string{"lvm-thin-vdo", "thin-vdo", "thin_vdo", " LVM-THIN-VDO "} {
		got, err := normalizeLVMPoolType(in)
		require.NoError(t, err, in)
		assert.Equal(t, vdoPoolType, got, in)
	}
	assert.True(t, isThinPoolType("thin_pool"))
	assert.True(t, isThinPoolType(vdoPoolType))
	assert.False(t, isThinPoolType("vg"))
	assert.False(t, isThinPoolType("zfs"))
}

func TestParseVDOReport(t *testing.T) {
	out := parseVDOReport(`  sds_a|  41.50|  63.20
  sds_b|12.00|5.00
  sds_b|77.00|1.00

  garbage
`)
	require.Len(t, out, 2)
	assert.InDelta(t, 41.5, out["sds_a"].PhysicalPercent, 0.001)
	assert.InDelta(t, 63.2, out["sds_a"].SavingPercent, 0.001)
	// Two VDO pools in one group: the fuller one is the one that matters.
	assert.InDelta(t, 77.0, out["sds_b"].PhysicalPercent, 0.001)
}

func TestVDOProbeScriptChecksEveryPrerequisite(t *testing.T) {
	for _, want := range []string{"dm_vdo", "kvdo", "vdoformat", "pooldatavdo", `"$missing`} {
		assert.Contains(t, vdoProbeScript, want)
	}
	// The probe uses $missing, so it must never be sent as a plain command.
	assert.True(t, strings.HasPrefix(vdoProbeScript, "missing="))
}

func TestAssertEncryptableVDOWithoutDB(t *testing.T) {
	rm := &ResourceManager{controller: &Controller{}}
	assert.NoError(t, rm.assertEncryptableVDO(context.Background(), "sds_pool"))
}

func TestAssertEncryptableVDORefusesVDOPool(t *testing.T) {
	db, err := database.Open(&database.Config{Path: filepath.Join(t.TempDir(), "sds.db")}, zap.NewNop())
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	ctx := context.Background()
	require.NoError(t, db.SavePool(ctx, &database.Pool{Name: "sds_dedup", Type: vdoPoolType, Node: "n1"}))
	require.NoError(t, db.SavePool(ctx, &database.Pool{Name: "sds_thin", Type: "thin_pool", Node: "n1"}))

	ctrl := &Controller{db: db}
	rm := &ResourceManager{controller: ctrl}
	assert.NoError(t, rm.assertEncryptableVDO(ctx, "thin"))
	err = rm.assertEncryptableVDO(ctx, "sds_thin", "dedup")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "VDO-backed")

	sm := &StorageManager{controller: ctrl}
	assert.Equal(t, map[string]bool{"sds_dedup": true}, sm.vdoPoolNames(ctx))
}
