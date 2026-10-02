package inspect

import (
	"strings"
	"testing"
	"time"
)

func thin(vg, name string, data, meta float64) LV {
	return LV{VG: vg, Name: name, Segtype: "thin-pool", DataPercent: data, MetaPercent: meta, HasUsage: true}
}

func TestPoolThresholds(t *testing.T) {
	in := cluster()
	in.PoolNearFull, in.PoolFull = 80, 90
	in.Probes["n1"].LVs = []LV{thin("sds_pool0", "thinpool", 82, 10)}
	in.Probes["n2"].LVs = []LV{thin("sds_pool0", "thinpool", 50, 93), thin("othervg", "tp", 99, 99)}
	checks := checkPools(in)
	if c := only(t, checks, "pool.data"); c.Subject != "n1:sds_pool0/thinpool" || c.Status != StatusWarn {
		t.Errorf("got %+v", c)
	}
	c := only(t, checks, "pool.metadata")
	if c.Status != StatusFail || c.Fix != "sds pool add --pool pool0 --nodes n2 --devices <new-device>" {
		t.Errorf("got %+v", c)
	}
}

func TestPoolGrowthAgainstPreviousReport(t *testing.T) {
	in := cluster()
	in.Previous = &Report{ID: "7", FinishedAt: t0.Add(-24 * time.Hour),
		Pools: []PoolSample{{Node: "n1", Pool: "sds_pool0/thinpool", DataPercent: 50}}}
	in.Probes["n1"].LVs = []LV{thin("sds_pool0", "thinpool", 60, 5)} // +10%/day: full in 4 days
	c := only(t, checkPools(in), "pool.growth")
	if c.Status != StatusWarn || !strings.Contains(c.Message, "4.0 days") {
		t.Errorf("got %+v", c)
	}

	in.Probes["n1"].LVs = []LV{thin("sds_pool0", "thinpool", 49, 5)}
	allPass(t, checkPools(in))
}

func TestPoolSamplesOnlyManagedThinPools(t *testing.T) {
	in := cluster()
	in.Probes["n1"].LVs = []LV{thin("sds_pool0", "thinpool", 60, 5), thin("rootvg", "tp", 1, 1),
		{VG: "sds_pool0", Name: "data_data", Segtype: "thin"}}
	s := PoolSamples(in)
	if len(s) != 1 || s[0].Pool != "sds_pool0/thinpool" || s[0].Node != "n1" {
		t.Errorf("got %+v", s)
	}
}

func TestUnreadableLVsIsAnError(t *testing.T) {
	in := cluster()
	in.Probes["n2"].LVsOK = false
	if c := only(t, checkPools(in), "pool.unreadable"); c.Status != StatusError || c.Subject != "n2" {
		t.Errorf("got %+v", c)
	}
}
