package inspect

import (
	"strings"
	"testing"
)

func TestHygieneListsLeftoversOfDeletedResources(t *testing.T) {
	in := cluster()
	in.Resources = []Resource{{Name: "vm"}, {Name: "my_data"}}
	in.Probes["n1"].ResFiles = []string{"vm", "old"}
	in.Probes["n2"].LVs = []LV{
		{VG: "sds_pool0", Name: "vm_data"},
		{VG: "sds_pool0", Name: "my_data_data"},
		{VG: "sds_pool0", Name: "my_data_data_sched_20260901T020000Z"},
		{VG: "sds_pool0", Name: "old_data"},
		{VG: "sds_pool0", Name: "old_state1"},
		{VG: "sds_pool0", Name: "old_vol2_sched_20260901T020000Z"},
		{VG: "sds_pool0", Name: "old_data_bk_20260901T020000Z"}, // the backups area judges these
		{VG: "sds_pool0", Name: "thinpool", Segtype: "thin-pool"},
		{VG: "rootvg", Name: "x_data"},
	}
	checks := checkHygiene(in)
	if c := only(t, checks, "hygiene.stale_res_file"); c.Subject != "n1" || strings.Join(c.Evidence, ",") != "old" {
		t.Errorf("got %+v", c)
	}
	c := only(t, checks, "hygiene.orphan_lv")
	want := "sds_pool0/old_data,sds_pool0/old_state1,sds_pool0/old_vol2_sched_20260901T020000Z"
	if strings.Join(c.Evidence, ",") != want {
		t.Errorf("evidence = %v", c.Evidence)
	}
	if c.Status != StatusWarn {
		t.Errorf("hygiene only warns: %+v", c)
	}
}

func TestHygieneCleanIsAPass(t *testing.T) {
	allPass(t, checkHygiene(cluster()))
}
