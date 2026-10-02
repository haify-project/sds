package inspect

import (
	"fmt"
	"strings"
)

// Growth thresholds: how soon a pool that keeps filling at its recent rate
// runs out.
const (
	growthWarnDays = 14.0
	growthFailDays = 3.0
	// minTrendHours keeps two reports minutes apart from producing a rate.
	minTrendHours = 1.0
)

// checkPools judges thin pool data and metadata use against the alert
// thresholds, and extrapolates the growth since the previous report.
func checkPools(in *Input) []Check {
	near, full := in.PoolNearFull, in.PoolFull
	if near <= 0 || near >= 100 {
		near = 85
	}
	if full <= 0 || full > 100 || full <= near {
		full = 95
	}
	prev := map[string]PoolSample{}
	if in.Previous != nil {
		for _, s := range in.Previous.Pools {
			prev[s.Node+"/"+s.Pool] = s
		}
	}
	var out []Check
	var unreadable []string
	pools := 0
	for _, node := range sortedKeys(in.Probes) {
		p := in.Probes[node]
		if !p.LVsOK {
			unreadable = append(unreadable, node)
			continue
		}
		for _, lv := range p.LVs {
			if lv.Segtype != "thin-pool" || !managedVG(lv.VG) || !lv.HasUsage {
				continue
			}
			pools++
			key := lv.VG + "/" + lv.Name
			subject := node + ":" + key
			ev := []string{fmt.Sprintf("data %.1f%%, metadata %.1f%% (warn %.0f%%, fail %.0f%%)", lv.DataPercent, lv.MetaPercent, near, full)}
			trend, days, growing := poolTrend(in, prev[node+"/"+key], lv.DataPercent)
			if trend != "" {
				ev = append(ev, trend)
			}
			fix := fmt.Sprintf("sds pool add --pool %s --nodes %s --devices <new-device>", strings.TrimPrefix(lv.VG, "sds_"), node)
			for _, dim := range []struct {
				id, what string
				pct      float64
			}{{"pool.data", "data", lv.DataPercent}, {"pool.metadata", "metadata", lv.MetaPercent}} {
				st := StatusPass
				switch {
				case dim.pct >= full:
					st = StatusFail
				case dim.pct >= near:
					st = StatusWarn
				}
				if st == StatusPass {
					continue
				}
				msg := fmt.Sprintf("thin pool %s is %.1f%% full", dim.what, dim.pct)
				if dim.what == "data" {
					msg += "; when it reaches 100% writes fail and every replica on it goes Diskless"
				} else {
					msg += "; metadata does not grow with the pool and fails writes just as hard"
				}
				out = append(out, Check{ID: dim.id, Area: AreaPools, Subject: subject, Status: st, Message: msg, Evidence: ev, Fix: fix})
			}
			if growing && days < growthWarnDays && lv.DataPercent < near {
				st := StatusWarn
				if days < growthFailDays {
					st = StatusFail
				}
				out = append(out, Check{ID: "pool.growth", Area: AreaPools, Subject: subject, Status: st,
					Message: fmt.Sprintf("thin pool data is growing fast enough to be full in about %.1f days", days), Evidence: ev, Fix: fix})
			}
		}
	}
	if len(unreadable) > 0 {
		out = append(out, Check{ID: "pool.unreadable", Area: AreaPools, Subject: strings.Join(unreadable, ","), Status: StatusError,
			Message: "lvs gave no readable answer, so thin pool use there is unknown"})
	}
	if len(out) == 0 {
		out = append(out, pass("pool.usage", AreaPools, "%s below %.0f%% data and metadata, none filling within %.0f days",
			plural(pools, "thin pool", "thin pools"), near, growthWarnDays))
	}
	return out
}

// poolTrend describes data growth since the previous sample and, when the
// pool is filling, the days until it is full at that rate.
func poolTrend(in *Input, prev PoolSample, now float64) (string, float64, bool) {
	if in.Previous == nil || prev.Pool == "" {
		return "", 0, false
	}
	hours := in.Now.Sub(in.Previous.FinishedAt).Hours()
	if hours < minTrendHours {
		return "", 0, false
	}
	perDay := (now - prev.DataPercent) / hours * 24
	desc := fmt.Sprintf("since report %s (%.0fh ago): %.1f%% -> %.1f%%, %+.2f%%/day", in.Previous.ID, hours, prev.DataPercent, now, perDay)
	if perDay <= 0 {
		return desc, 0, false
	}
	days := (100 - now) / perDay
	return desc + fmt.Sprintf(", full in ~%.1f days", days), days, true
}
