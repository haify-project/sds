package controller

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/haify-project/sds/pkg/deployment"
)

// RAID inside a node's pool (`sds pool create --raid`).
//
// DRBD keeps copies on other nodes; RAID keeps a node's own copy alive when
// one of its disks dies, so losing a disk costs a rebuild on that node instead
// of a full resync from a peer — which on a big pool is hours of network and
// of running one copy short. LVM's own RAID is used rather than md: its
// metadata lives in the volume group, so there is no mdadm.conf to keep in
// step and nothing for the initramfs to assemble.
//
// A thin pool's data area is created at the chosen level and its metadata
// area as a mirror; a thick pool records the level and gives it to every
// volume created in it.

// raidMinDisks is the fewest disks each level can be built from.
var raidMinDisks = map[string]int{"raid1": 2, "raid10": 4, "raid5": 3, "raid6": 5}

// validateRaid checks a requested level against the pool type and disks.
func validateRaid(level, poolType string, disks int) error {
	if level == "" {
		return nil
	}
	min, ok := raidMinDisks[level]
	if !ok {
		return fmt.Errorf("unknown raid level %q: use raid1, raid10, raid5 or raid6", level)
	}
	if poolType == vdoPoolType {
		return fmt.Errorf("a VDO pool cannot also be RAID")
	}
	if disks < min {
		return fmt.Errorf("%s needs at least %d disks, %d given", level, min, disks)
	}
	if level == "raid10" && disks%2 != 0 {
		return fmt.Errorf("raid10 pairs disks into mirrors: give an even number, not %d", disks)
	}
	return nil
}

// raidLVArgs are the lvcreate arguments for level over disks.
func raidLVArgs(level string, disks int) string {
	switch level {
	case "raid1":
		return "--type raid1 -m 1"
	case "raid10":
		return fmt.Sprintf("--type raid10 -m 1 -i %d", disks/2)
	case "raid5":
		return fmt.Sprintf("--type raid5 -i %d", disks-1)
	case "raid6":
		return fmt.Sprintf("--type raid6 -i %d", disks-2)
	}
	return ""
}

// raidDataShare is the fraction of raw space a level leaves for data.
func raidDataShare(level string, disks int) (num, den uint64) {
	switch level {
	case "raid1", "raid10":
		return 1, 2
	case "raid5":
		return uint64(disks - 1), uint64(disks)
	case "raid6":
		return uint64(disks - 2), uint64(disks)
	}
	return 1, 1
}

// raidSymmetricFree is how much of a group's free space a RAID LV can grow
// into. Each image of the LV lives on its own disk, so the room is set by the
// disk with the least free space among the ones it needs, not by the total:
// a mirror cannot use space that only one disk has.
func raidSymmetricFree(level string, frees []uint64, disks int) uint64 {
	images := disks
	if level == "raid1" {
		images = 2
	}
	if images <= 0 || len(frees) < images {
		return 0
	}
	sorted := append([]uint64(nil), frees...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i] > sorted[j] })
	return sorted[images-1] * uint64(images)
}

// raidThinSizes splits a group's free space between a thin pool's data and
// its mirrored metadata, keeping pct percent of free for the pool and a little
// slack for the RAID metadata sub-volumes and extent rounding.
func raidThinSizes(free, pct uint64, level string, disks int) (data, meta uint64) {
	num, den := raidDataShare(level, disks)
	budget := free * pct / 100 * 98 / 100
	meta = thinMetadataBytes(budget * num / den)
	if 2*meta >= budget {
		return 0, 0
	}
	data = (budget - 2*meta) * num / den
	const mib4 = 4 << 20
	return data / mib4 * mib4, (meta + mib4 - 1) / mib4 * mib4
}

// createRaidThinPool builds vg's thin pool on RAID.
func (sm *StorageManager) createRaidThinPool(ctx context.Context, host, vg, thinLV, level string, disks int) error {
	dep := sm.controller.deployment
	free, err := dep.VGFreeBytes(ctx, host, vg)
	if err != nil {
		return err
	}
	data, meta := raidThinSizes(free, sm.controller.thinPoolPercentFree(), level, disks)
	if data == 0 {
		return fmt.Errorf("the disks are too small for a %s thin pool", level)
	}
	steps := []string{
		fmt.Sprintf("sudo lvcreate -y %s -L %dB -n %s %s", raidLVArgs(level, disks), data, thinLV, vg),
		fmt.Sprintf("sudo lvcreate -y --type raid1 -m 1 -L %dB -n %s_meta %s", meta, thinLV, vg),
		fmt.Sprintf("sudo lvconvert -y --type thin-pool --poolmetadata %s/%s_meta %s/%s", vg, thinLV, vg, thinLV),
	}
	for _, cmd := range steps {
		if err := execFailure(dep.Exec(ctx, []string{host}, cmd, deployment.WithExecTimeout(raidCreateTimeout))); err != nil {
			return fmt.Errorf("create the %s thin pool: %w", level, err)
		}
	}
	return nil
}

// raidCreateTimeout bounds one lvcreate; the initial RAID sync runs on in the
// background after it returns.
const raidCreateTimeout = 10 * time.Minute

// poolRaid is the RAID level recorded for pool, "" when none.
func (rm *ResourceManager) poolRaid(ctx context.Context, pool string) string {
	if rm.controller.db == nil {
		return ""
	}
	p, err := rm.controller.db.GetPool(ctx, normalizeManagedName(pool))
	if err != nil || p == nil {
		return ""
	}
	return p.Raid
}

// createThickLV creates a thick volume, at the pool's RAID level if it has
// one.
func (rm *ResourceManager) createThickLV(ctx context.Context, hosts []string, pool, volume, size string) (*deployment.ExecResult, error) {
	level := rm.poolRaid(ctx, pool)
	if level == "" {
		return rm.deployment.LVCreate(ctx, hosts, pool, volume, size)
	}
	// The level's arguments depend on how many disks the group has, which can
	// differ by node, so each node gets its own command.
	out := &deployment.ExecResult{Hosts: map[string]*deployment.HostResult{}}
	for _, h := range hosts {
		n := rm.vgDiskCount(ctx, h, pool)
		flag := "-L"
		if strings.HasSuffix(size, "%FREE") || strings.HasSuffix(size, "%VG") {
			flag = "-l"
		}
		cmd := fmt.Sprintf("sudo lvcreate -y %s %s %s -n %s %s", raidLVArgs(level, n), flag, size, volume, pool)
		res, err := rm.deployment.Exec(ctx, []string{h}, cmd, deployment.WithExecTimeout(raidCreateTimeout))
		if err != nil {
			return nil, err
		}
		for k, v := range res.Hosts {
			out.Hosts[k] = v
		}
	}
	return out, nil
}

func (rm *ResourceManager) vgDiskCount(ctx context.Context, host, vg string) int {
	res, err := rm.deployment.Exec(ctx, []string{host}, fmt.Sprintf("sudo vgs --noheadings -o pv_count %s", vg))
	if err != nil || res.Hosts[host] == nil {
		return 0
	}
	n, _ := strconv.Atoi(strings.TrimSpace(res.Hosts[host].Output))
	return n
}
