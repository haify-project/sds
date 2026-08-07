package controller

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"go.uber.org/zap"
)

// Reading the state of a pool's fast tier: what lvs says, and what an operator
// needs from it. Kept apart from the code that attaches and detaches a cache so
// that the parsing — which is where a wrong answer turns into a wrong decision
// — can be read and tested on its own.

// PoolCacheInfo is what an operator needs in order to tell a tiered pool from a
// plain one, and to judge the exposure a writeback cache is carrying.
type PoolCacheInfo struct {
	Mode      string `json:"mode"`
	SizeBytes uint64 `json:"size_bytes"`
	Device    string `json:"device,omitempty"`
	// OriginLV is the LV the cache sits in front of, as LVM names it — for a
	// cached thin pool that is the pool's internal "_tdata" sub-LV.
	OriginLV     string `json:"origin_lv,omitempty"`
	UsedPercent  uint32 `json:"used_percent"`
	HitPercent   uint32 `json:"hit_percent"`
	DirtyPercent uint32 `json:"dirty_percent"`
	// Degraded means LVM reports the cache as missing a device. Such a cache
	// cannot be flushed, so it cannot be detached without discarding whatever
	// it still holds.
	Degraded bool `json:"degraded,omitempty"`
}

// lvsCacheRow is one line of `lvs -a -o <LVMCacheFields>`.
type lvsCacheRow struct {
	VG          string
	Name        string
	Attr        string
	SegType     string
	SizeBytes   uint64
	Mode        string
	TotalBlocks uint64
	UsedBlocks  uint64
	DirtyBlocks uint64
	ReadHits    uint64
	ReadMisses  uint64
	WriteHits   uint64
	WriteMisses uint64
	Device      string
}

// parseCacheReport groups an lvs report by volume group.
//
// Rows for internal LVs arrive in brackets — "[sds_pool_thin_tdata]" — and the
// devices column carries an extent offset, "/dev/nvme0n1(0)". Both are stripped
// here so that nothing downstream has to know about lvs presentation.
func parseCacheReport(output string) map[string][]lvsCacheRow {
	byVG := make(map[string][]lvsCacheRow)
	for _, line := range strings.Split(output, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		f := strings.Split(line, "|")
		if len(f) < 14 {
			continue // not a report line: a warning from lvm, or a truncated read
		}
		vg := strings.TrimSpace(f[0])
		if vg == "" {
			continue
		}
		row := lvsCacheRow{
			VG:          vg,
			Name:        strings.Trim(strings.TrimSpace(f[1]), "[]"),
			Attr:        strings.TrimSpace(f[2]),
			SegType:     strings.TrimSpace(f[3]),
			SizeBytes:   parseCacheUint(f[4]),
			Mode:        strings.TrimSpace(f[5]),
			TotalBlocks: parseCacheUint(f[6]),
			UsedBlocks:  parseCacheUint(f[7]),
			DirtyBlocks: parseCacheUint(f[8]),
			ReadHits:    parseCacheUint(f[9]),
			ReadMisses:  parseCacheUint(f[10]),
			WriteHits:   parseCacheUint(f[11]),
			WriteMisses: parseCacheUint(f[12]),
			Device:      parseCacheDevice(f[13]),
		}
		byVG[vg] = append(byVG[vg], row)
	}
	return byVG
}

// parseCacheUint reads one numeric lvs column. The cache counters are blank for
// an LV that is not currently active, which is absence of information rather
// than a parse failure, so it reads as zero.
func parseCacheUint(field string) uint64 {
	v := strings.TrimSpace(field)
	v = strings.TrimSuffix(v, "B")
	if v == "" {
		return 0
	}
	n, err := strconv.ParseUint(v, 10, 64)
	if err != nil {
		return 0
	}
	return n
}

func parseCacheDevice(field string) string {
	v := strings.TrimSpace(field)
	if i := strings.Index(v, "("); i >= 0 {
		v = v[:i]
	}
	// A multi-segment LV lists several devices; a cache volume is created on
	// exactly one PV, so the first is the answer and the rest are noise.
	if i := strings.Index(v, ","); i >= 0 {
		v = v[:i]
	}
	return strings.TrimSpace(v)
}

// summarizeCache turns one volume group's rows into the cache an operator sees,
// or nil when the group has none.
//
// The cache is found by segment type rather than by name. SDS names the volume
// it creates, but a group could have been cached by hand before SDS met it, and
// reporting "no cache" for a pool that plainly has one is the more expensive
// mistake — it is the answer that would let a second cache be attached.
func summarizeCache(rows []lvsCacheRow) *PoolCacheInfo {
	var origin, cvol *lvsCacheRow
	for i := range rows {
		switch {
		case rows[i].SegType == "cache":
			origin = &rows[i]
		case rows[i].SegType == "cache-pool" || strings.HasSuffix(rows[i].Name, "_cvol"):
			cvol = &rows[i]
		}
	}
	if origin == nil {
		return nil
	}

	info := &PoolCacheInfo{
		Mode:     origin.Mode,
		OriginLV: origin.Name,
		Degraded: cacheIsDegraded(origin.Attr),
	}
	if cvol != nil {
		info.SizeBytes = cvol.SizeBytes
		info.Device = cvol.Device
		if info.Mode == "" {
			// LVM reports the mode on whichever of the two rows it considers to
			// own the setting, and which one that is has moved between the
			// cachepool and cachevol styles.
			info.Mode = cvol.Mode
		}
		if cvol.Degraded() {
			info.Degraded = true
		}
	}
	info.UsedPercent = percentOf(origin.UsedBlocks, origin.TotalBlocks)
	info.DirtyPercent = percentOf(origin.DirtyBlocks, origin.TotalBlocks)
	hits := origin.ReadHits + origin.WriteHits
	info.HitPercent = percentOf(hits, hits+origin.ReadMisses+origin.WriteMisses)
	return info
}

// Degraded reports whether this row's LVM attributes say a device is missing.
func (r lvsCacheRow) Degraded() bool { return cacheIsDegraded(r.Attr) }

// cacheIsDegraded reads the volume-health character of an lv_attr string.
//
// Only 'p' (partial — a physical volume is missing) and 'X' (unknown) are
// treated as degraded, rather than "anything but healthy". The same position
// also carries conditions that say nothing about whether a cache can be
// flushed, and a check broad enough to include them would refuse to detach
// perfectly healthy caches — which is a refusal an operator cannot work around
// except by discarding data.
func cacheIsDegraded(attr string) bool {
	const healthIndex = 8
	if len(attr) <= healthIndex {
		return false
	}
	switch attr[healthIndex] {
	case 'p', 'X':
		return true
	}
	return false
}

func percentOf(part, whole uint64) uint32 {
	if whole == 0 {
		return 0
	}
	return uint32(part * 100 / whole)
}

// readPoolCache reads the cache state of one volume group on one host.
func (sm *StorageManager) readPoolCache(ctx context.Context, address, vgName string) (*PoolCacheInfo, error) {
	res, err := sm.controller.deployment.LVSCacheReport(ctx, []string{address}, vgName)
	if err != nil {
		return nil, fmt.Errorf("read cache state of %s: %w", vgName, err)
	}
	for _, r := range res.Hosts {
		if !r.Success {
			return nil, fmt.Errorf("read cache state of %s: %s", vgName, strings.TrimSpace(r.Output))
		}
		return summarizeCache(parseCacheReport(r.Output)[vgName]), nil
	}
	return nil, fmt.Errorf("read cache state of %s: no result", vgName)
}

// cacheByPool reports the cache of every volume group on every host, indexed by
// normalized node name and then by volume group.
//
// One lvs call for the whole cluster: this runs on the pool listing path, and a
// per-pool query would turn a cheap `pool list` into one SSH round per pool.
// A host that cannot answer is left out rather than failing the listing — a
// missing cache badge is a much smaller problem than a pool list that errors.
func (sm *StorageManager) cacheByPool(ctx context.Context, hosts []string) map[string]map[string]*PoolCacheInfo {
	out := make(map[string]map[string]*PoolCacheInfo)
	if len(hosts) == 0 {
		return out
	}
	res, err := sm.controller.deployment.LVSCacheReport(ctx, hosts, "")
	if err != nil {
		sm.controller.logger.Warn("Failed to read pool cache state", zap.Error(err))
		return out
	}
	for host, r := range res.Hosts {
		if !r.Success {
			continue
		}
		normalized := sm.controller.NormalizeHost(host)
		if normalized == "" {
			normalized = host
		}
		for vg, rows := range parseCacheReport(r.Output) {
			info := summarizeCache(rows)
			if info == nil {
				continue
			}
			if out[normalized] == nil {
				out[normalized] = make(map[string]*PoolCacheInfo)
			}
			out[normalized][vg] = info
		}
	}
	return out
}
