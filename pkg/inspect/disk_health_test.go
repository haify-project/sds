package inspect

import (
	"encoding/base64"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseSmart(t *testing.T) {
	st, detail, model, _ := ParseSmart([]byte(`{"model_name":"Samsung 980","smart_status":{"passed":true},
		"nvme_smart_health_information_log":{"critical_warning":0,"percentage_used":93,"media_errors":0}}`))
	assert.Equal(t, DiskWarn, st)
	assert.Contains(t, detail, "93% of rated endurance")
	assert.Equal(t, "Samsung 980", model)

	st, detail, _, _ = ParseSmart([]byte(`{"smart_status":{"passed":true},"ata_smart_attributes":{"table":[
		{"id":5,"raw":{"value":8}},{"id":198,"raw":{"value":2}}]}}`))
	assert.Equal(t, DiskFail, st)
	assert.Contains(t, detail, "2 uncorrectable sectors")
	assert.Contains(t, detail, "8 reallocated sectors")

	st, _, _, _ = ParseSmart([]byte(`{"smart_status":{"passed":false}}`))
	assert.Equal(t, DiskFail, st)

	st, detail, _, _ = ParseSmart([]byte(`{"smartctl":{"messages":[{"string":"/dev/vdb: Unable to detect device type"}]}}`))
	assert.Equal(t, DiskUnknown, st, "a virtual disk has no SMART: unknown, not failing")
	assert.Contains(t, detail, "Unable to detect")

	st, _, _, _ = ParseSmart([]byte(`{"smart_status":{"passed":true},"ata_smart_attributes":{"table":[{"id":5,"raw":{"value":0}}]}}`))
	assert.Equal(t, DiskOK, st)
}

func TestParseDiskProbeAndCheck(t *testing.T) {
	bad := base64.StdEncoding.EncodeToString([]byte(`{"model_name":"WD","smart_status":{"passed":false}}`))
	out := "/dev/vdb|haify_tp|10737418240|5368709120|/dev/vdb|NOSMARTCTL\n/dev/sdb1|haify_hdd|100|50|/dev/sdb|" + bad + "\n" +
		"/dev/sda3|ubuntu-vg|100|100|/dev/sda|NOSMARTCTL\n" // the node's own VG: not a pool
	disks := ParseDiskProbe("n1", out)
	require.Len(t, disks, 2)
	assert.Equal(t, DiskUnknown, disks[0].Status)
	assert.Equal(t, uint64(5368709120), disks[0].UsedBytes)
	assert.Equal(t, DiskFail, disks[1].Status)
	assert.Equal(t, "/dev/sdb", disks[1].Device)

	checks := checkDisks(&Input{Disks: disks, DisksProbed: true})
	require.Len(t, checks, 1, "unknown is not a finding")
	assert.Equal(t, StatusFail, checks[0].Status)
	assert.Contains(t, checks[0].Fix, "replace-disk --pool haify_hdd --node n1 --disk /dev/sdb1")
	assert.Nil(t, checkDisks(&Input{}), "nothing read, nothing said")
}
