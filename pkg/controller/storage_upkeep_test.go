package controller

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/haify-project/sds/pkg/database"
)

func TestParseTrimOutput(t *testing.T) {
	out := "/var/lib/sds-app/pg1|0|/var/lib/sds-app/pg1: 812.3 MiB (851771392 bytes) trimmed\n" +
		"/data\\x20dir|0|/data dir: 0 B (0 bytes) trimmed\n" +
		"/mnt/stuck|124|\n"
	res := parseTrimOutput("sdt1", out)
	require.Len(t, res, 3)
	assert.Equal(t, uint64(851771392), res[0].Bytes)
	assert.Equal(t, "/data dir", res[1].Mount, "findmnt escapes spaces")
	assert.NotEmpty(t, res[2].Err, "a timed-out trim is a failure")
	msg, failed := trimSummary(res)
	assert.True(t, failed)
	assert.Contains(t, msg, "trimmed 2 filesystem(s), 812.3 MiB of free space discarded")
	assert.Contains(t, msg, "sdt1:/mnt/stuck")
}

// Metadata grows first and doubles; data grows by the configured share, both
// only as far as the group's free space reaches.
func TestExtendPlan(t *testing.T) {
	const gib = 1 << 30
	data, meta := extendPlan(10*gib, 128<<20, 5*gib, true, true, 20, 1, 1, false)
	assert.Equal(t, uint64(256<<20), meta)
	assert.Equal(t, uint64(2*gib), data)

	data, _ = extendPlan(10*gib, 128<<20, 1*gib, true, false, 20, 1, 1, false)
	assert.Equal(t, uint64(1*gib), data, "capped by what is free")

	data, meta = extendPlan(10*gib, 128<<20, 10<<20, true, true, 20, 1, 1, false)
	assert.Zero(t, data, "too little to bother: the caller warns instead")
	assert.Zero(t, meta)

	// RAID1: 1 GiB free holds under half a GiB of data, after the mirrored
	// metadata growth has taken its 2x share.
	data, meta = extendPlan(10*gib, 128<<20, 1*gib, true, true, 20, 1, 2, true)
	assert.Equal(t, uint64(256<<20), meta)
	assert.LessOrEqual(t, 2*data+2*(meta-(128<<20)), uint64(1*gib))
	assert.Positive(t, data)
	assert.Zero(t, data%(4<<20), "whole extents: lvextend refuses anything else")
}

func TestCheckRemovable(t *testing.T) {
	pvs := parsePVs("/dev/vdb|10737418240|8589934592\n/dev/vdc|10737418240|0\n")
	require.Len(t, pvs, 2)
	assert.Empty(t, checkRemovable(pvs, "/dev/vdc", false))
	assert.Empty(t, checkRemovable(pvs, "/dev/vdb", false), "10G free on vdc holds vdb's 8G")
	assert.Contains(t, checkRemovable(pvs, "/dev/vdd", false), "not a disk of this pool")
	assert.Contains(t, checkRemovable(pvs, "/dev/vdc", true), "use replace-disk")
	assert.Contains(t, checkRemovable(pvs[:1], "/dev/vdb", false), "only disk")
	full := parsePVs("/dev/vdb|10|9\n/dev/vdc|10|8\n")
	assert.Contains(t, checkRemovable(full, "/dev/vdb", false), "add a disk first")
}

func TestDiskJobScript(t *testing.T) {
	rm := diskJobScript(&database.StorageJob{ID: "j1", Kind: database.JobRemoveDisk, Pool: "sds_tp", Disk: "/dev/vdc"}, false)
	assert.Contains(t, rm, "pvmove -i 15 /dev/vdc ||")
	assert.Contains(t, rm, "vgreduce sds_tp /dev/vdc")
	assert.Contains(t, rm, "echo done > /var/lib/sds-jobs/j1.status")
	assert.NotContains(t, rm, "lvconvert")

	rep := diskJobScript(&database.StorageJob{ID: "j2", Kind: database.JobReplaceDisk, Pool: "sds_r", Disk: "/dev/vdc", NewDisk: "/dev/vdd"}, true)
	assert.Contains(t, rep, "vgextend sds_r /dev/vdd")
	assert.Contains(t, rep, "lvconvert -y --replace /dev/vdc sds_r/$lv /dev/vdd")
	assert.Less(t, strings.Index(rep, "vgextend"), strings.Index(rep, "lvconvert"), "the new disk joins before anything moves to it")
	assert.Less(t, strings.Index(rep, "pvmove"), strings.Index(rep, "vgreduce"), "the old disk leaves only once empty")
}

func TestRaid(t *testing.T) {
	assert.NoError(t, validateRaid("", "thin_pool", 1))
	assert.NoError(t, validateRaid("raid1", "thin_pool", 2))
	assert.ErrorContains(t, validateRaid("raid5", "vg", 2), "at least 3")
	assert.ErrorContains(t, validateRaid("raid1", vdoPoolType, 2), "VDO")
	assert.ErrorContains(t, validateRaid("raid0", "vg", 4), "unknown")
	assert.ErrorContains(t, validateRaid("raid10", "vg", 5), "even")

	assert.Equal(t, "--type raid10 -m 1 -i 2", raidLVArgs("raid10", 4))
	assert.Equal(t, "--type raid5 -i 3", raidLVArgs("raid5", 4))
	assert.Equal(t, "--type raid6 -i 3", raidLVArgs("raid6", 5))

	const gib = 1 << 30
	data, meta := raidThinSizes(20*gib, 85, "raid1", 2)
	assert.Positive(t, data)
	assert.LessOrEqual(t, 2*data+2*meta, uint64(20*gib)*85/100, "mirrored data and metadata fit in the share of free space")
	assert.Zero(t, data%(4<<20))

	// A mirror is held back by its emptier disk; raid5 by its third-emptiest.
	const mib = 1 << 20
	assert.Equal(t, uint64(2*4*mib), raidSymmetricFree("raid1", []uint64{4 * mib, 252 * mib}, 2))
	assert.Equal(t, uint64(3*100*mib), raidSymmetricFree("raid5", []uint64{500 * mib, 100 * mib, 300 * mib}, 3))
	assert.Zero(t, raidSymmetricFree("raid10", []uint64{mib, mib}, 4))
}
