package controller

import (
	"context"
	"strings"
	"testing"

	"github.com/haify-project/sds/pkg/deployment"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A cache is the one part of a pool that can sit in the write path without
// being replicated anywhere, so the refusals matter more than the happy path
// and are asserted one at a time.

// lvsLine builds one row of the report LVSCacheReport asks for. lvs indents its
// output by two spaces under --noheadings, which is included here because the
// parser has to survive it.
func lvsLine(fields ...string) string {
	if len(fields) != 14 {
		panic("an lvs cache row has 14 columns")
	}
	return "  " + strings.Join(fields, "|")
}

// cachedThinPoolReport is what a node running a writethrough cache in front of
// a thin pool reports: the cache lives on the pool's internal _tdata sub-LV and
// the fast device on an internal _cvol, neither of which lvs shows without -a.
func cachedThinPoolReport(mode, attr string) string {
	return strings.Join([]string{
		lvsLine("sds_sdspool", "openclaw_data", "Vwi-aotz--", "thin", "6442450944",
			"", "", "", "", "", "", "", "", "sds_sdspool_thin(0)"),
		lvsLine("sds_sdspool", "sds_sdspool_thin", "twi-aotz--", "thin-pool", "107374182400",
			"", "", "", "", "", "", "", "", "sds_sdspool_thin_tdata(0)"),
		lvsLine("sds_sdspool", "[sds_sdspool_thin_tdata]", attr, "cache", "107374182400",
			mode, "1638400", "409600", "16384", "900000", "60000", "30000", "10000",
			"sds_sdspool_thin_tdata_corig(0)"),
		lvsLine("sds_sdspool", "[sds_sdspool_thin_tmeta]", "ewi-ao----", "linear", "134217728",
			"", "", "", "", "", "", "", "", "/dev/sdb(0)"),
		lvsLine("sds_sdspool", "[sdscache_cvol]", "Cwi-aoC---", "linear", "107374182400",
			"", "", "", "", "", "", "", "", "/dev/nvme0n1(0)"),
	}, "\n")
}

func TestParsesACachedThinPoolReport(t *testing.T) {
	info := summarizeCache(parseCacheReport(cachedThinPoolReport("writethrough", "Cwi-aoC---"))["sds_sdspool"])
	require.NotNil(t, info)
	assert.Equal(t, "writethrough", info.Mode)
	// The cache is bolted to the thin pool's data sub-LV, and its brackets are
	// lvs presentation, not part of the name.
	assert.Equal(t, "sds_sdspool_thin_tdata", info.OriginLV)
	assert.Equal(t, "/dev/nvme0n1", info.Device, "the extent offset is not part of the device path")
	assert.Equal(t, uint64(107374182400), info.SizeBytes, "the size is the fast volume's, not the origin's")
	assert.Equal(t, uint32(25), info.UsedPercent)
	assert.Equal(t, uint32(1), info.DirtyPercent)
	// (900000 + 30000) hits out of (900000 + 30000 + 60000 + 10000) accesses.
	assert.Equal(t, uint32(93), info.HitPercent)
	assert.False(t, info.Degraded)
}

func TestReportsNoCacheForAPlainPool(t *testing.T) {
	plain := strings.Join([]string{
		lvsLine("sds_sdspool", "openclaw_data", "Vwi-aotz--", "thin", "6442450944",
			"", "", "", "", "", "", "", "", "sds_sdspool_thin(0)"),
		lvsLine("sds_sdspool", "sds_sdspool_thin", "twi-aotz--", "thin-pool", "107374182400",
			"", "", "", "", "", "", "", "", "/dev/sdb(0)"),
	}, "\n")
	assert.Nil(t, summarizeCache(parseCacheReport(plain)["sds_sdspool"]))
}

// The kernel counters are blank for an LV that is not currently active. That is
// missing information, not a broken cache, and it must not divide by zero.
func TestParsesACacheWhoseCountersAreBlank(t *testing.T) {
	inactive := lvsLine("sds_sdspool", "[sds_sdspool_thin_tdata]", "Cwi---C---", "cache", "107374182400",
		"writeback", "", "", "", "", "", "", "", "sds_sdspool_thin_tdata_corig(0)")
	info := summarizeCache(parseCacheReport(inactive)["sds_sdspool"])
	require.NotNil(t, info)
	assert.Equal(t, "writeback", info.Mode)
	assert.Zero(t, info.UsedPercent)
	assert.Zero(t, info.HitPercent)
	assert.Zero(t, info.DirtyPercent)
}

// lvm writes warnings to the same stream, and Exec hands back stdout and stderr
// combined. A "WARNING: Device /dev/sdb not initialized" line must not become a
// volume group.
func TestSkipsLinesThatAreNotReportRows(t *testing.T) {
	noise := strings.Join([]string{
		"  WARNING: PV /dev/sdb in VG sds_sdspool is missing.",
		"",
		"  sds_sdspool|truncated|Cwi-aoC---|cache",
		cachedThinPoolReport("writethrough", "Cwi-aoC---"),
	}, "\n")
	byVG := parseCacheReport(noise)
	assert.Len(t, byVG, 1)
	require.NotNil(t, summarizeCache(byVG["sds_sdspool"]))
}

func TestGroupsRowsFromEveryVolumeGroupOnAHost(t *testing.T) {
	both := cachedThinPoolReport("writeback", "Cwi-aoC---") + "\n" +
		lvsLine("sds_other", "sds_other_thin", "twi-aotz--", "thin-pool", "1073741824",
			"", "", "", "", "", "", "", "", "/dev/sdc(0)")
	byVG := parseCacheReport(both)
	require.Len(t, byVG, 2)
	assert.NotNil(t, summarizeCache(byVG["sds_sdspool"]))
	assert.Nil(t, summarizeCache(byVG["sds_other"]))
}

// The health character is what says a cache cannot be flushed, and getting it
// wrong is expensive in both directions: too strict and a healthy cache can
// never be detached, too loose and a broken one is detached with --uncache,
// which fails, or worse appears to work.
func TestReadsTheCacheHealthCharacter(t *testing.T) {
	assert.False(t, cacheIsDegraded("Cwi-aoC---"), "a healthy cache")
	assert.True(t, cacheIsDegraded("Cwi-aoC-p-"), "partial: a physical volume is gone")
	assert.True(t, cacheIsDegraded("Cwi-aoC-X-"), "unknown health")
	assert.False(t, cacheIsDegraded("Cwi-aoC-m-"), "mismatches say nothing about flushing")
	assert.False(t, cacheIsDegraded("Cwi-aoC-w-"), "writemostly is a raid property")
	assert.False(t, cacheIsDegraded("short"), "a truncated attr string is not evidence of damage")
}

func TestParsesABlockDeviceProbe(t *testing.T) {
	p := parseBlockDeviceProbe(strings.Join([]string{
		"block=yes",
		"size=1000204886016",
		"fstype=",
		"mount=",
		"holders=",
		"pvvg=",
	}, "\n"))
	assert.True(t, p.IsBlock)
	assert.Equal(t, uint64(1000204886016), p.SizeBytes)
	assert.Empty(t, p.FSType)
	assert.Empty(t, p.Holders)

	busy := parseBlockDeviceProbe(strings.Join([]string{
		"block=yes",
		"size=1000204886016",
		"fstype=LVM2_member",
		"mount=",
		"holders=nvme0n1p1 nvme0n1p2",
		"pvvg=vg_root",
	}, "\n"))
	assert.Equal(t, "LVM2_member", busy.FSType)
	assert.Equal(t, []string{"nvme0n1p1", "nvme0n1p2"}, busy.Holders)
	assert.Equal(t, "vg_root", busy.VG)
}

// ---- mode ----

// The default is the whole safety argument, so it is asserted rather than
// assumed: an empty mode must never reach lvconvert as writeback.
func TestCacheModeDefaultsToWritethrough(t *testing.T) {
	mode, err := normalizeCacheMode("")
	require.NoError(t, err)
	assert.Equal(t, "writethrough", mode)

	mode, err = normalizeCacheMode("WriteBack")
	require.NoError(t, err)
	assert.Equal(t, "writeback", mode)

	_, err = normalizeCacheMode("write-back")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "writethrough")
}

// ---- device guards ----

func TestRefusesADeviceThatIsNotFree(t *testing.T) {
	free := blockDeviceProbe{IsBlock: true, SizeBytes: 512 * gib}

	cases := []struct {
		name  string
		probe blockDeviceProbe
		want  string
	}{
		{"not a block device", blockDeviceProbe{}, "not a block device"},
		{"already a physical volume", blockDeviceProbe{IsBlock: true, SizeBytes: 512 * gib, VG: "vg_root"}, "vg_root"},
		{"has partitions", blockDeviceProbe{IsBlock: true, SizeBytes: 512 * gib, Holders: []string{"nvme0n1p1"}}, "nvme0n1p1"},
		{"mounted", blockDeviceProbe{IsBlock: true, SizeBytes: 512 * gib, Mountpoint: "/srv"}, "/srv"},
		{"carries a filesystem", blockDeviceProbe{IsBlock: true, SizeBytes: 512 * gib, FSType: "xfs"}, "wipefs"},
		{"too small", blockDeviceProbe{IsBlock: true, SizeBytes: 2 * gib}, "working set"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := checkCacheDevice("/dev/nvme0n1", tc.probe, "node-a")
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.want)
		})
	}

	require.NoError(t, checkCacheDevice("/dev/nvme0n1", free, "node-a"))
}

// cacheTestController wires a fake whose lvs report can change between calls,
// which is what the read-back-and-verify steps need.
func cacheTestController(t *testing.T, dep *fakeDeploymentClient) *Controller {
	t.Helper()
	ctrl := newBasicTestController(dep)
	ctrl.hosts = []string{"10.0.0.1"}
	ctrl.hostsMap["node-a"] = "10.0.0.1"
	return ctrl
}

// reportSequence serves a different lvs report on each call, so a test can say
// "no cache before, a cache after".
func reportSequence(reports ...string) func(context.Context, []string, string) (*deployment.ExecResult, error) {
	i := 0
	return func(_ context.Context, hosts []string, _ string) (*deployment.ExecResult, error) {
		out := reports[len(reports)-1]
		if i < len(reports) {
			out = reports[i]
		}
		i++
		return successExecResult(hosts, out), nil
	}
}

func freeDeviceProbe(hosts []string) *deployment.ExecResult {
	return successExecResult(hosts, "block=yes\nsize=549755813888\nfstype=\nmount=\nholders=\npvvg=")
}

// ---- listing ----

// An operator has to be able to tell a tiered pool from a plain one without
// asking a second question per pool.
func TestPoolListingCarriesCacheState(t *testing.T) {
	dep := &fakeDeploymentClient{
		execFunc: func(_ context.Context, hosts []string, cmd string, _ ...deployment.ExecOption) (*deployment.ExecResult, error) {
			if strings.Contains(cmd, "vgs") {
				return successExecResult(hosts, "sds_sdspool|107374182400|53687091200|/dev/sdb"), nil
			}
			return successExecResult(hosts, ""), nil
		},
		lvsCacheReportFunc: func(_ context.Context, hosts []string, vg string) (*deployment.ExecResult, error) {
			assert.Empty(t, vg, "the listing path asks once for every group on the host")
			return successExecResult(hosts, cachedThinPoolReport("writeback", "Cwi-aoC---")), nil
		},
	}
	ctrl := cacheTestController(t, dep)

	pools, err := ctrl.storage.ListPools(context.Background())
	require.NoError(t, err)
	require.Len(t, pools, 1)
	require.NotNil(t, pools[0].Cache)
	assert.Equal(t, "writeback", pools[0].Cache.Mode)
	assert.Equal(t, uint32(1), pools[0].Cache.DirtyPercent,
		"the dirty share is the size of the window a lost SSD would take with it")
}

// A node that cannot answer the cache query must not fail the pool listing:
// the capacity figures are why the call was made.
func TestPoolListingSurvivesAnUnreadableCacheQuery(t *testing.T) {
	dep := &fakeDeploymentClient{
		execFunc: func(_ context.Context, hosts []string, cmd string, _ ...deployment.ExecOption) (*deployment.ExecResult, error) {
			if strings.Contains(cmd, "vgs") {
				return successExecResult(hosts, "sds_sdspool|107374182400|53687091200|/dev/sdb"), nil
			}
			return successExecResult(hosts, ""), nil
		},
		lvsCacheReportFunc: func(_ context.Context, hosts []string, _ string) (*deployment.ExecResult, error) {
			return failedResult(hosts, "  Volume group sds_sdspool not found"), nil
		},
	}
	ctrl := cacheTestController(t, dep)

	pools, err := ctrl.storage.ListPools(context.Background())
	require.NoError(t, err)
	require.Len(t, pools, 1)
	assert.Nil(t, pools[0].Cache)
}

// The report below is not synthetic. It was captured from LVM 2.03.16 on a real
// node by creating a thin pool, attaching a cache volume to it with the exact
// command this package issues, and running the exact query LVMCacheFields
// defines. Everything about how LVM presents a cached thin pool was a guess
// until then, and the guesses that mattered were:
//
//   - `lvconvert --type cache --cachevol X vg/<thinpool>` is accepted with the
//     THIN POOL's name; LVM redirects the cache onto its _tdata sub-LV itself
//     and reports "Logical volume vg/<pool>_tdata is now cached". Passing
//     _tdata explicitly is not required. `--uncache vg/<thinpool>` is likewise
//     accepted, and removes the cache volume as part of the detach.
//   - cache_mode appears on the _tdata row, NOT on the thin pool's own row,
//     which is blank. Reading the mode back from the pool row would compare
//     "" against "writethrough" and fail every successful attach.
//   - The fast volume becomes "[<name>_cvol]" with segtype `linear`, not
//     `cache-pool`, so only the name suffix identifies it — and it is the only
//     row carrying the real block device.
//   - A third internal row, "[<pool>_tdata_corig]", appears and must be
//     ignored; its devices column holds the slow device.
//   - The origin row's own devices column names an LV, not a device, so the
//     device has to come from the _cvol row.
func TestParsesRealLVMCachedThinPoolReport(t *testing.T) {
	const real = `  sds_vg0|[tiertest_cache_cvol]|Cwi-aoC---|linear|1073741824|||||||||/dev/sdd(512)
  sds_vg0|tiertest_thin|twi-a-tz--|thin-pool|2147483648|||||||||tiertest_thin_tdata(0)
  sds_vg0|[tiertest_thin_tdata]|Cwi-aoC---|cache|2147483648|writethrough|16256|0|0|0|0|0|0|tiertest_thin_tdata_corig(0)
  sds_vg0|[tiertest_thin_tdata_corig]|owi-aoC---|linear|2147483648|||||||||/dev/sdd(0)
  sds_vg0|[tiertest_thin_tmeta]|ewi-ao----|linear|4194304|||||||||/dev/sdc(6145)`

	byVG := parseCacheReport(real)
	rows, found := byVG["sds_vg0"]
	require.True(t, found, "the volume group must be recognised")
	require.Len(t, rows, 5, "every internal row is reported and must survive parsing")

	info := summarizeCache(rows)
	require.NotNil(t, info, "a cached thin pool must not read as uncached")

	assert.Equal(t, "writethrough", info.Mode,
		"the mode lives on the _tdata row; reading the pool row yields empty and breaks the read-back check")
	assert.Equal(t, "tiertest_thin_tdata", info.OriginLV)
	assert.Equal(t, "/dev/sdd", info.Device,
		"the device comes from the _cvol row — the origin row names an LV")
	assert.Equal(t, uint64(1073741824), info.SizeBytes)
	assert.False(t, info.Degraded, "Cwi-aoC--- is a healthy cache")

	// A freshly attached writethrough cache has taken no writes yet. Dirty is
	// the number an operator watches, so it must read as 0 rather than as
	// unknown-therefore-alarming.
	assert.Zero(t, info.DirtyPercent)
	assert.Zero(t, info.UsedPercent)
}

// The same host before any cache existed: the plain thin pool must not be
// mistaken for a cached one just because it has internal sub-LVs.
func TestParsesRealLVMUncachedThinPoolReport(t *testing.T) {
	const real = `  sds_vg0|tiertest_thin|twi-a-tz--|thin-pool|2147483648|||||||||tiertest_thin_tdata(0)
  sds_vg0|[tiertest_thin_tdata]|Twi-ao----|linear|2147483648|||||||||/dev/sdd(0)
  sds_vg0|[tiertest_thin_tmeta]|ewi-ao----|linear|4194304|||||||||/dev/sdc(6145)`

	info := summarizeCache(parseCacheReport(real)["sds_vg0"])
	assert.Nil(t, info, "a thin pool with no cache segment has no cache")
}
