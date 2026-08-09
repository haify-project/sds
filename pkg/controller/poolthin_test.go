package controller

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Real output shapes, taken from a four-node cluster running LVM 2.03 with
// `lvs --noheadings --nosuffix --units b --separator '|' -o LVMThinFields`.

func TestParseThinReportPicksOutThePool(t *testing.T) {
	// One thin pool plus the volumes and snapshots living in it. Only the pool
	// carries utilisation; everything else must be dropped rather than parsed
	// as a 0% pool.
	out := `  sds_sdspool|openclaw_data|thin|6442450944|100.00||Vwi-a-tz--
  sds_sdspool|openclaw_data_sched_20260808T110000Z|thin|6442450944|||Vwi---tz-k
  sds_sdspool|sdsthin|thin-pool|20937965568|45.50|8.23|twi-aotz--`

	byVG := parseThinReport(out)
	require.Len(t, byVG, 1)

	info := byVG["sds_sdspool"]
	require.NotNil(t, info)
	assert.Equal(t, "sdsthin", info.PoolLV)
	assert.Equal(t, uint64(20937965568), info.SizeBytes)
	assert.InDelta(t, 45.50, info.DataPercent, 0.001)
	assert.InDelta(t, 8.23, info.MetaPercent, 0.001)
	assert.False(t, info.OutOfSpace)
}

func TestParseThinReportDetectsOutOfDataSpace(t *testing.T) {
	// The state that took node-a down on 2026-08-09. The 'D' in the health
	// field is LVM's own verdict and is what the kernel acted on; a caller
	// must not have to infer it from the percentage.
	out := `  sds_sdspool|sdsthin|thin-pool|10468982784|100.00|14.00|twi-aotzD-`

	info := parseThinReport(out)["sds_sdspool"]
	require.NotNil(t, info)
	assert.True(t, info.OutOfSpace)
	assert.InDelta(t, 100.0, info.DataPercent, 0.001)
}

func TestParseThinReportSkipsGroupsWithoutAThinPool(t *testing.T) {
	// A plain thick VG must be absent from the map entirely. Reporting it as a
	// pool at 0% would draw an empty bar for something that has no pool at all.
	out := `  vg0|data|linear|10737418240|||-wi-a-----`

	assert.Empty(t, parseThinReport(out))
}

func TestParseThinReportIgnoresNoise(t *testing.T) {
	// lvs prefixes warnings on stderr that dispatch folds into the same
	// stream, and a truncated read leaves a short final line. Neither may
	// abort the rows around them.
	out := `  WARNING: You have not turned on protection against thin pools running out of space.
  sds_sdspool|sdsthin|thin-pool|20937965568|45.50|8.23|twi-aotz--
  sds_sdspool|trunc`

	info := parseThinReport(out)["sds_sdspool"]
	require.NotNil(t, info)
	assert.InDelta(t, 45.50, info.DataPercent, 0.001)
}

func TestParseThinReportSeparatesVolumeGroups(t *testing.T) {
	out := `  sds_a|sdsthin|thin-pool|20937965568|30.78|6.91|twi-aotz--
  sds_b|sdsthin|thin-pool|20937965568|46.55|8.26|twi-aotz--`

	byVG := parseThinReport(out)
	require.Len(t, byVG, 2)
	assert.InDelta(t, 30.78, byVG["sds_a"].DataPercent, 0.001)
	assert.InDelta(t, 46.55, byVG["sds_b"].DataPercent, 0.001)
}

func TestParseThinReportKeepsTheFullestPoolInAGroup(t *testing.T) {
	// Adopted groups can hold more than one pool. The one closest to failing
	// is the one worth reporting, regardless of the order lvs emits them.
	out := `  vg|quiet|thin-pool|10737418240|12.00|3.00|twi-aotz--
  vg|busy|thin-pool|10737418240|91.00|9.00|twi-aotz--`

	info := parseThinReport(out)["vg"]
	require.NotNil(t, info)
	assert.Equal(t, "busy", info.PoolLV)

	// And with the order reversed, so the tiebreak is not an artefact of which
	// row happened to arrive first.
	reversed := `  vg|busy|thin-pool|10737418240|91.00|9.00|twi-aotz--
  vg|quiet|thin-pool|10737418240|12.00|3.00|twi-aotz--`
	assert.Equal(t, "busy", parseThinReport(reversed)["vg"].PoolLV)
}

func TestParseThinReportInactivePoolReportsNoPercentages(t *testing.T) {
	// An inactive pool leaves both percentage columns blank. That is absence
	// of information, and it must not be mistaken for an empty pool — the
	// caller distinguishes them by PoolThinInfo being present at all.
	out := `  vg|sdsthin|thin-pool|10737418240|||twi---tz--`

	info := parseThinReport(out)["vg"]
	require.NotNil(t, info)
	assert.Zero(t, info.DataPercent)
	assert.Zero(t, info.MetaPercent)
	assert.False(t, info.OutOfSpace)
}

func TestThinOutOfSpaceReadsTheHealthField(t *testing.T) {
	assert.True(t, thinOutOfSpace("twi-aotzD-"))
	assert.False(t, thinOutOfSpace("twi-aotz--"))
	// Short or empty attrs must not panic.
	assert.False(t, thinOutOfSpace(""))
	assert.False(t, thinOutOfSpace("twi-aotz"))
	// 'D' anywhere other than the health field is a different flag.
	assert.False(t, thinOutOfSpace("Dwi-aotz--"))
}

func TestParseThinUintToleratesSuffixAndFraction(t *testing.T) {
	assert.Equal(t, uint64(20937965568), parseThinUint("20937965568"))
	assert.Equal(t, uint64(20937965568), parseThinUint("20937965568B"))
	assert.Equal(t, uint64(20937965568), parseThinUint(" 20937965568.00 "))
	assert.Zero(t, parseThinUint(""))
	assert.Zero(t, parseThinUint("not-a-number"))
}
