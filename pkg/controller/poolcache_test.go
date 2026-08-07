package controller

import (
	"context"
	"strings"
	"testing"

	"github.com/liliang-cn/sds/pkg/database"
	"github.com/liliang-cn/sds/pkg/deployment"
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

// ---- attach ----

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

func newAttachFake(reports ...string) *fakeDeploymentClient {
	return &fakeDeploymentClient{
		lvThinPoolInFunc: func(context.Context, string, string) (string, error) {
			return "sds_sdspool_thin", nil
		},
		probeBlockDeviceFunc: func(_ context.Context, host, _ string) (*deployment.ExecResult, error) {
			return freeDeviceProbe([]string{host}), nil
		},
		lvsCacheReportFunc: reportSequence(reports...),
	}
}

func TestAttachesAWritethroughCacheByDefault(t *testing.T) {
	dep := newAttachFake("", cachedThinPoolReport("writethrough", "Cwi-aoC---"))
	var mode, cacheVol, lv string
	dep.lvConvertToCacheFunc = func(_ context.Context, hosts []string, vg, l, cv, m string) (*deployment.ExecResult, error) {
		lv, cacheVol, mode = l, cv, m
		assert.Equal(t, "sds_sdspool", vg)
		return successExecResult(hosts, ""), nil
	}
	ctrl := cacheTestController(t, dep)

	info, err := ctrl.storage.AddPoolCache(context.Background(), "node-a", "sdspool", "/dev/nvme0n1", "")
	require.NoError(t, err)
	assert.Equal(t, "writethrough", mode, "an unstated mode must never become writeback")
	assert.Equal(t, cacheVolName, cacheVol)
	// The cache goes in front of the pool, not in front of one volume.
	assert.Equal(t, "sds_sdspool_thin", lv)
	assert.Equal(t, "writethrough", info.Mode)
	assert.Equal(t, "/dev/nvme0n1", info.Device)
}

func TestAttachesWritebackOnlyWhenAskedForByName(t *testing.T) {
	dep := newAttachFake("", cachedThinPoolReport("writeback", "Cwi-aoC---"))
	var mode string
	dep.lvConvertToCacheFunc = func(_ context.Context, hosts []string, _, _, _, m string) (*deployment.ExecResult, error) {
		mode = m
		return successExecResult(hosts, ""), nil
	}
	ctrl := cacheTestController(t, dep)

	info, err := ctrl.storage.AddPoolCache(context.Background(), "node-a", "sdspool", "/dev/nvme0n1", "writeback")
	require.NoError(t, err)
	assert.Equal(t, "writeback", mode)
	assert.Equal(t, "writeback", info.Mode)
}

func TestRefusesAnUnknownCacheMode(t *testing.T) {
	ctrl := cacheTestController(t, newAttachFake(""))
	_, err := ctrl.storage.AddPoolCache(context.Background(), "node-a", "sdspool", "/dev/nvme0n1", "wb")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unknown cache mode")
}

// A thick pool has no single LV that every volume passes through, so there is
// nothing a per-pool cache could be attached to.
func TestRefusesToCacheAThickPool(t *testing.T) {
	dep := newAttachFake("")
	dep.lvThinPoolInFunc = func(context.Context, string, string) (string, error) { return "", nil }
	ctrl := cacheTestController(t, dep)

	_, err := ctrl.storage.AddPoolCache(context.Background(), "node-a", "sdspool", "/dev/nvme0n1", "")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "convert-thin")
}

// ZFS caches through L2ARC and a separate log device; dm-cache under a vdev
// would hide the device from both layers.
func TestRefusesToCacheAZFSPool(t *testing.T) {
	ctrl := cacheTestController(t, newAttachFake(""))
	ctrl.db = newTestDB(t)
	ctx := context.Background()
	require.NoError(t, ctrl.db.SavePool(ctx, &database.Pool{Name: "sds_tank", Type: "zfs", Node: "node-a"}))

	_, err := ctrl.storage.AddPoolCache(ctx, "node-a", "tank", "/dev/nvme0n1", "")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "L2ARC")
}

// Reconfiguring a pool that already has a cache would mean detaching the old
// one — with whatever it still holds — as a side effect of an "add".
func TestRefusesToCacheAPoolThatAlreadyHasOne(t *testing.T) {
	dep := newAttachFake(cachedThinPoolReport("writeback", "Cwi-aoC---"))
	ctrl := cacheTestController(t, dep)

	_, err := ctrl.storage.AddPoolCache(context.Background(), "node-a", "sdspool", "/dev/nvme0n1", "")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "already cached")
}

func TestRefusesADeviceTheNodeSaysIsInUse(t *testing.T) {
	dep := newAttachFake("")
	dep.probeBlockDeviceFunc = func(_ context.Context, host, _ string) (*deployment.ExecResult, error) {
		return successExecResult([]string{host},
			"block=yes\nsize=549755813888\nfstype=LVM2_member\nmount=\nholders=\npvvg=vg_root"), nil
	}
	ctrl := cacheTestController(t, dep)

	_, err := ctrl.storage.AddPoolCache(context.Background(), "node-a", "sdspool", "/dev/nvme0n1", "")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "vg_root")
}

// lvconvert exits non-zero through the result, never through the returned
// error; a failed attach must not leave the cache volume behind, because the
// obvious retry would then fail on the name rather than on the real problem.
func TestUndoesAPartialAttach(t *testing.T) {
	dep := newAttachFake("")
	var removed, released string
	dep.lvConvertToCacheFunc = func(_ context.Context, hosts []string, _, _, _, _ string) (*deployment.ExecResult, error) {
		return failedResult(hosts, "  Insufficient free space: 8 extents needed, but only 0 available"), nil
	}
	dep.lvRemoveFunc = func(_ context.Context, hosts []string, path string) (*deployment.ExecResult, error) {
		removed = path
		return successExecResult(hosts, ""), nil
	}
	dep.vgReduceAndRemovePVFunc = func(_ context.Context, hosts []string, _, device string) (*deployment.ExecResult, error) {
		released = device
		return successExecResult(hosts, ""), nil
	}
	ctrl := cacheTestController(t, dep)

	_, err := ctrl.storage.AddPoolCache(context.Background(), "node-a", "sdspool", "/dev/nvme0n1", "")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "Insufficient free space")
	assert.Equal(t, "sds_sdspool/"+cacheVolName, removed)
	assert.Equal(t, "/dev/nvme0n1", released)
}

func TestReportsAnAttachThatLeftNoCacheBehind(t *testing.T) {
	// lvconvert exited zero, but the pool still has no cache.
	ctrl := cacheTestController(t, newAttachFake("", ""))
	_, err := ctrl.storage.AddPoolCache(context.Background(), "node-a", "sdspool", "/dev/nvme0n1", "")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no cache")
}

// The mode is read back rather than reported from the request: an lvconvert
// that succeeded while applying a different mode would leave the operator
// believing writes are durable when they are not.
func TestReportsACacheAttachedInTheWrongMode(t *testing.T) {
	ctrl := cacheTestController(t, newAttachFake("", cachedThinPoolReport("writeback", "Cwi-aoC---")))
	_, err := ctrl.storage.AddPoolCache(context.Background(), "node-a", "sdspool", "/dev/nvme0n1", "")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "writeback")
	assert.Contains(t, err.Error(), "writethrough")
}

// A pool whose current cache state cannot be read is a pool this cannot reason
// about, and attaching a second cache to one that already has one is not a
// recoverable mistake.
func TestRefusesToAttachWhenTheCacheStateCannotBeRead(t *testing.T) {
	dep := newAttachFake("")
	dep.lvsCacheReportFunc = func(_ context.Context, hosts []string, _ string) (*deployment.ExecResult, error) {
		return failedResult(hosts, "  Volume group sds_sdspool not found"), nil
	}
	ctrl := cacheTestController(t, dep)

	_, err := ctrl.storage.AddPoolCache(context.Background(), "node-a", "sdspool", "/dev/nvme0n1", "")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not found")
}

func TestRefusesToAttachWhenTheDeviceCannotBeProbed(t *testing.T) {
	dep := newAttachFake("")
	dep.probeBlockDeviceFunc = func(_ context.Context, host, _ string) (*deployment.ExecResult, error) {
		return failedResult([]string{host}, "  sh: lsblk: not found"), nil
	}
	ctrl := cacheTestController(t, dep)

	_, err := ctrl.storage.AddPoolCache(context.Background(), "node-a", "sdspool", "/dev/nvme0n1", "")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "lsblk")
}

func TestRefusesToAttachWithoutADevice(t *testing.T) {
	ctrl := cacheTestController(t, newAttachFake(""))
	_, err := ctrl.storage.AddPoolCache(context.Background(), "node-a", "sdspool", "  ", "")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "required")
}

// ---- detach ----

func newDetachFake(reports ...string) *fakeDeploymentClient {
	return &fakeDeploymentClient{lvsCacheReportFunc: reportSequence(reports...)}
}

func TestRemovesACacheAndReleasesItsDevice(t *testing.T) {
	dep := newDetachFake(cachedThinPoolReport("writeback", "Cwi-aoC---"), "")
	var uncachedLV, releasedDevice string
	dep.lvUncacheFunc = func(_ context.Context, hosts []string, _, lv string) (*deployment.ExecResult, error) {
		uncachedLV = lv
		return successExecResult(hosts, ""), nil
	}
	dep.vgReduceAndRemovePVFunc = func(_ context.Context, hosts []string, _, device string) (*deployment.ExecResult, error) {
		releasedDevice = device
		return successExecResult(hosts, ""), nil
	}
	ctrl := cacheTestController(t, dep)

	require.NoError(t, ctrl.storage.RemovePoolCache(context.Background(), "node-a", "sdspool"))
	// lvconvert takes the pool, not the internal sub-LV the cache is bolted to.
	assert.Equal(t, "sds_sdspool_thin", uncachedLV)
	// Leaving the SSD in the group would make it free space that the next
	// thin-pool extension would allocate pool data onto.
	assert.Equal(t, "/dev/nvme0n1", releasedDevice)
}

func TestRefusesToRemoveACacheThatIsNotThere(t *testing.T) {
	ctrl := cacheTestController(t, newDetachFake(""))
	err := ctrl.storage.RemovePoolCache(context.Background(), "node-a", "sdspool")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no cache")
}

// A cache whose device is gone cannot be flushed. Detaching it anyway needs
// --force, which discards the dirty blocks — a decision that belongs to whoever
// owns the data, not to this command.
func TestRefusesToRemoveACacheThatCannotBeFlushed(t *testing.T) {
	dep := newDetachFake(cachedThinPoolReport("writeback", "Cwi-aoC-p-"))
	var uncalled = true
	dep.lvUncacheFunc = func(_ context.Context, hosts []string, _, _ string) (*deployment.ExecResult, error) {
		uncalled = false
		return successExecResult(hosts, ""), nil
	}
	ctrl := cacheTestController(t, dep)

	err := ctrl.storage.RemovePoolCache(context.Background(), "node-a", "sdspool")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "--force")
	assert.True(t, uncalled, "nothing may be detached when the flush is known to be impossible")
}

// The flush is what makes the SSD safe to pull, and lvconvert reporting success
// is not the same fact as the cache being gone.
func TestDoesNotReportSuccessWhenTheCacheIsStillAttached(t *testing.T) {
	stillThere := cachedThinPoolReport("writeback", "Cwi-aoC---")
	dep := newDetachFake(stillThere, stillThere)
	var released bool
	dep.vgReduceAndRemovePVFunc = func(_ context.Context, hosts []string, _, _ string) (*deployment.ExecResult, error) {
		released = true
		return successExecResult(hosts, ""), nil
	}
	ctrl := cacheTestController(t, dep)

	err := ctrl.storage.RemovePoolCache(context.Background(), "node-a", "sdspool")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "still attached")
	assert.False(t, released, "the device must not be pulled out from under a cache that is still there")
}

func TestReportsAFailedFlush(t *testing.T) {
	dep := newDetachFake(cachedThinPoolReport("writeback", "Cwi-aoC---"), "")
	dep.lvUncacheFunc = func(_ context.Context, hosts []string, _, _ string) (*deployment.ExecResult, error) {
		return failedResult(hosts, "  Failed to flush cache, device is not writable"), nil
	}
	ctrl := cacheTestController(t, dep)

	err := ctrl.storage.RemovePoolCache(context.Background(), "node-a", "sdspool")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "Failed to flush cache")
}

// A device that could not be released is still a member PV of the pool, which
// the operator has to know about — but the flush did happen, and the message
// has to say so rather than implying the data is at risk.
func TestReportsADeviceThatCouldNotBeReleased(t *testing.T) {
	dep := newDetachFake(cachedThinPoolReport("writethrough", "Cwi-aoC---"), "")
	dep.vgReduceAndRemovePVFunc = func(_ context.Context, hosts []string, _, _ string) (*deployment.ExecResult, error) {
		return failedResult(hosts, "  Physical volume /dev/nvme0n1 still in use"), nil
	}
	ctrl := cacheTestController(t, dep)

	err := ctrl.storage.RemovePoolCache(context.Background(), "node-a", "sdspool")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "flushed and detached")
	assert.Contains(t, err.Error(), "still in use")
}

// If lvs never named the fast device, it is still a member PV of the pool after
// the detach — the same end state as a vgreduce that failed, and reported the
// same way rather than passed over.
func TestReportsACacheDeviceItCouldNotIdentify(t *testing.T) {
	anonymous := lvsLine("sds_sdspool", "[sds_sdspool_thin_tdata]", "Cwi-aoC---", "cache", "107374182400",
		"writethrough", "1638400", "409600", "0", "1", "1", "1", "1", "sds_sdspool_thin_tdata_corig(0)")
	dep := newDetachFake(anonymous, "")
	var released bool
	dep.vgReduceAndRemovePVFunc = func(_ context.Context, hosts []string, _, _ string) (*deployment.ExecResult, error) {
		released = true
		return successExecResult(hosts, ""), nil
	}
	ctrl := cacheTestController(t, dep)

	err := ctrl.storage.RemovePoolCache(context.Background(), "node-a", "sdspool")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "vgreduce")
	assert.False(t, released, "there is no device name to hand to vgreduce")
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
