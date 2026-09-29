package controller

import (
	"context"
	"maps"
	"strings"
)

// rsDiscardGranularity is what a resource on thin storage resyncs with. A
// resync writes every block it covers, zeros included, so a full one — a new
// replica, an invalidate, a failback whose history no longer matches — turns a
// thin volume into a fully allocated one: 0.03% to 100% on a 1G volume on
// the Lima cluster, and the same thing is how a thin pool on openclaw filled
// up. With this set the sync source sends a run of zeros as a discard, and the
// target keeps it unallocated. DRBD rounds the value up to the backing
// device's discard granularity, and 64 KiB is the thin-pool chunk size LVM
// picks for all but very large pools.
const rsDiscardGranularity = "65536"

// withThinResyncDefaults returns options with disk/rs-discard-granularity
// added when any volume is backed by thin storage and the caller did not set
// it. The caller's map is not modified. Thick volumes are left alone: on them
// a discard can reach a device that does not read back zeros afterwards.
func (rm *ResourceManager) withThinResyncDefaults(ctx context.Context, options map[string]string, storageType string, volumes []resolvedVolume) map[string]string {
	for k := range options {
		if strings.TrimPrefix(strings.ToLower(k), "disk/") == "rs-discard-granularity" {
			return options
		}
	}
	thin := storageType == "lvm-thin" || storageType == "zfs-thin"
	for _, v := range volumes {
		if thin {
			break
		}
		thin = rm.poolRecordedThin(ctx, v.pool) || rm.poolRecordedThin(ctx, normalizeManagedName(v.pool))
	}
	if !thin {
		return options
	}
	out := make(map[string]string, len(options)+1)
	maps.Copy(out, options)
	out["disk/rs-discard-granularity"] = rsDiscardGranularity
	return out
}
