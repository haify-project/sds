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
	data, meta := extendPlan(10*gib, 128<<20, 5*gib, true, true, 20)
	assert.Equal(t, uint64(256<<20), meta)
	assert.Equal(t, uint64(2*gib), data)

	data, _ = extendPlan(10*gib, 128<<20, 1*gib, true, false, 20)
	assert.Equal(t, uint64(1*gib), data, "capped by what is free")

	data, meta = extendPlan(10*gib, 128<<20, 10<<20, true, true, 20)
	assert.Zero(t, data, "too little to bother: the caller warns instead")
	assert.Zero(t, meta)

	// Metadata growth comes out of the free space before data; data is
	// rounded down to whole extents.
	data, meta = extendPlan(10*gib, 128<<20, 1*gib+(1<<20), true, true, 20)
	assert.Equal(t, uint64(256<<20), meta)
	assert.Equal(t, uint64(1*gib-(128<<20)), data)
	assert.Zero(t, data%(4<<20), "whole extents: lvextend refuses anything else")
}

func TestCheckRemovable(t *testing.T) {
	pvs := parsePVs("/dev/vdb|10737418240|8589934592\n/dev/vdc|10737418240|0\n")
	require.Len(t, pvs, 2)
	assert.Empty(t, checkRemovable(pvs, "/dev/vdc"))
	assert.Empty(t, checkRemovable(pvs, "/dev/vdb"), "10G free on vdc holds vdb's 8G")
	assert.Contains(t, checkRemovable(pvs, "/dev/vdd"), "not a disk of this pool")
	assert.Contains(t, checkRemovable(pvs[:1], "/dev/vdb"), "only disk")
	full := parsePVs("/dev/vdb|10|9\n/dev/vdc|10|8\n")
	assert.Contains(t, checkRemovable(full, "/dev/vdb"), "add a disk first")
}

func TestDiskJobScript(t *testing.T) {
	rm := diskJobScript(&database.StorageJob{ID: "j1", Kind: database.JobRemoveDisk, Pool: "sds_tp", Disk: "/dev/vdc"})
	assert.Contains(t, rm, "pvmove -i 15 /dev/vdc ||")
	assert.Contains(t, rm, "vgreduce sds_tp /dev/vdc")
	assert.Contains(t, rm, "echo done > /var/lib/sds-jobs/j1.status")

	rep := diskJobScript(&database.StorageJob{ID: "j2", Kind: database.JobReplaceDisk, Pool: "sds_r", Disk: "/dev/vdc", NewDisk: "/dev/vdd"})
	assert.Contains(t, rep, "vgextend sds_r /dev/vdd")
	assert.Contains(t, rep, "pvmove -i 15 /dev/vdc /dev/vdd")
	assert.Less(t, strings.Index(rep, "vgextend"), strings.Index(rep, "pvmove"), "the new disk joins before anything moves to it")
	assert.Less(t, strings.Index(rep, "pvmove"), strings.Index(rep, "vgreduce"), "the old disk leaves only once empty")
}
