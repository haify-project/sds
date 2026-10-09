package deployment

import (
	"context"
	"fmt"
	"strconv"
	"strings"
)

// ============ LVM Operations ============

// PVCreate creates physical volumes
func (c *Client) PVCreate(ctx context.Context, hosts []string, device string, opts ...LVMOption) (*ExecResult, error) {
	// Idempotent: a device that is already a PV (e.g. from a partially
	// completed earlier pool creation) is left alone instead of failing
	// the whole retry with "Can't initialize ... without -ff".
	cmd := fmt.Sprintf("sudo pvs %s >/dev/null 2>&1 || sudo pvcreate -y %s", device, device)
	return c.Exec(ctx, hosts, cmd)
}

// VGCreate creates volume groups
func (c *Client) VGCreate(ctx context.Context, hosts []string, vgName string, devices []string) (*ExecResult, error) {
	// Idempotent for retries: an existing VG with the target name is the
	// successful outcome of a previous attempt, not an error.
	cmd := fmt.Sprintf("sudo vgs %s >/dev/null 2>&1 || sudo vgcreate %s %s", vgName, vgName, strings.Join(devices, " "))
	return c.Exec(ctx, hosts, cmd)
}

// LVCreate creates logical volumes
func (c *Client) LVCreate(ctx context.Context, hosts []string, vgName, lvName, size string) (*ExecResult, error) {
	cmd := fmt.Sprintf("sudo lvcreate -y -L %s -n %s %s", size, lvName, vgName)
	return c.Exec(ctx, hosts, cmd)
}

// LVCreateThinPool creates a thin pool logical volume
func (c *Client) LVCreateThinPool(ctx context.Context, hosts []string, vgName, poolName, size string) (*ExecResult, error) {
	// lvcreate only accepts absolute sizes with -L; percentage sizes like
	// "95%FREE" need the extents flag -l, otherwise creation fails with
	// "Invalid argument for --size".
	sizeFlag := "-L"
	if strings.Contains(size, "%") {
		sizeFlag = "-l"
	}
	cmd := fmt.Sprintf("sudo lvcreate -y %s %s -T %s/%s", sizeFlag, size, vgName, poolName)
	return c.Exec(ctx, hosts, cmd)
}

// LVCreateThinVolume creates a thin logical volume
func (c *Client) LVCreateThinVolume(ctx context.Context, hosts []string, vgName, poolName, lvName, size string) (*ExecResult, error) {
	// lvcreate -V <size> -T <vg>/<pool> -n <name>
	cmd := fmt.Sprintf("sudo lvcreate -y -V %s -T %s/%s -n %s", size, vgName, poolName, lvName)
	return c.Exec(ctx, hosts, cmd)
}

// LVRemove removes logical volumes
func (c *Client) LVRemove(ctx context.Context, hosts []string, lvPath string) (*ExecResult, error) {
	cmd := fmt.Sprintf("sudo lvremove -f %s", lvPath)
	return c.Exec(ctx, hosts, cmd)
}

// LVCreateThinPoolAllFree creates a thin pool spanning every free extent in the
// volume group, with an explicit metadata area.
//
// LVM's default metadata size is generous for a pool holding a couple of
// volumes and far too small for one holding a snapshot history: a freshly
// converted 8 GiB pool with a single volume already showed 30% of the default
// area used. Exhausting metadata takes the whole pool read-only, which is a
// much worse failure than running out of data space, so the size is stated
// rather than inherited.
//
// The *data* size is not stated, deliberately. Asking for an exact byte count
// means reproducing LVM's allocator: the metadata area rounds up to an extent,
// a spare copy of it is allocated alongside, and the data area rounds up too —
// so "everything minus one metadata area" overshoots the group and lvcreate
// exits 5 with "Insufficient free space". `-l 100%FREE` asks for exactly the
// intent, "as large as the free extents allow", and leaves that arithmetic
// where the knowledge is.
func (c *Client) LVCreateThinPoolAllFree(ctx context.Context, hosts []string, vgName, poolName string, metadataBytes uint64) (*ExecResult, error) {
	cmd := fmt.Sprintf("sudo lvcreate -y -l 100%%FREE --poolmetadatasize %dB -T %s/%s",
		metadataBytes, vgName, poolName)
	return c.Exec(ctx, hosts, cmd)
}

// LVExtendThinPoolMetadata raises a thin pool's metadata area.
//
// This must run BEFORE the data area is extended. Growing the data with
// `-l +100%FREE` takes every free extent, and the metadata extension — which
// needs extents of its own, and as many again for the spare copy LVM keeps
// alongside — then has nothing left to take.
func (c *Client) LVExtendThinPoolMetadata(ctx context.Context, hosts []string, vgName, poolName string, sizeBytes uint64) (*ExecResult, error) {
	return c.Exec(ctx, hosts, fmt.Sprintf("sudo lvextend -y --poolmetadatasize %dB %s/%s",
		sizeBytes, vgName, poolName))
}

// LVExtendThinPoolAllFree grows a thin pool's data area into every free extent
// left in the volume group.
func (c *Client) LVExtendThinPoolAllFree(ctx context.Context, hosts []string, vgName, poolName string) (*ExecResult, error) {
	return c.Exec(ctx, hosts, fmt.Sprintf("sudo lvextend -y -l +100%%FREE %s/%s", vgName, poolName))
}

// LVExtendThinPoolPercentFree grows a thin pool's data area by percent of the
// volume group's free extents, leaving the rest unallocated.
func (c *Client) LVExtendThinPoolPercentFree(ctx context.Context, hosts []string, vgName, poolName string, percent int) (*ExecResult, error) {
	return c.Exec(ctx, hosts, fmt.Sprintf("sudo lvextend -y -l +%d%%FREE %s/%s", percent, vgName, poolName))
}

// LVThinPoolIn returns the name of the thin pool in a volume group, or an
// empty string if the group has none.
//
// The name cannot be assumed: `pool create` builds "<pool>_thin", converting a
// thick pool in place builds something else, and a group adopted from
// elsewhere could use any name at all. Asking is one command and removes a
// whole class of "works on the nodes I tested" bug.
func (c *Client) LVThinPoolIn(ctx context.Context, host, vgName string) (string, error) {
	res, err := c.Exec(ctx, []string{host},
		fmt.Sprintf("sudo lvs --noheadings -o lv_name,segtype %s", vgName))
	if err != nil {
		return "", err
	}
	out, err := singleHostOutput(res, "list volumes in "+vgName)
	if err != nil {
		return "", err
	}
	for _, line := range strings.Split(out, "\n") {
		f := strings.Fields(line)
		if len(f) == 2 && f[1] == "thin-pool" {
			return f[0], nil
		}
	}
	return "", nil
}

// LVExists reports whether a logical volume is there at all.
//
// It distinguishes "no such volume" from "the query failed": lvs exits 5 both
// when the name is unknown and when LVM itself is unhappy, so the two are told
// apart by the message rather than the status. Only the volume being absent
// counts as absent — a missing *volume group* says `Volume group "x" not
// found`, and reporting that as a missing volume would turn a broken node into
// what looks like a half-finished conversion.
func (c *Client) LVExists(ctx context.Context, host, vgName, lvName string) (bool, error) {
	res, err := c.Exec(ctx, []string{host},
		fmt.Sprintf("sudo lvs --noheadings -o lv_name %s/%s", vgName, lvName))
	if err != nil {
		return false, err
	}
	for _, r := range res.Hosts {
		if r.Success {
			return true, nil
		}
		if strings.Contains(strings.ToLower(r.Output), "failed to find logical volume") {
			return false, nil
		}
		return false, fmt.Errorf("look for %s/%s: %s", vgName, lvName, strings.TrimSpace(r.Output))
	}
	return false, fmt.Errorf("look for %s/%s: no result", vgName, lvName)
}

// LVSizeBytes reports a logical volume's exact size.
//
// DRBD records the device size in its metadata, so a replica rebuilt from a
// rounded "6G" is a different device and refuses to attach. Every rebuild path
// has to carry bytes, never human sizes.
func (c *Client) LVSizeBytes(ctx context.Context, host, vgName, lvName string) (uint64, error) {
	res, err := c.Exec(ctx, []string{host},
		fmt.Sprintf("sudo lvs --noheadings --nosuffix --units b -o lv_size %s/%s", vgName, lvName))
	if err != nil {
		return 0, err
	}
	out, err := singleHostOutput(res, "read size of "+vgName+"/"+lvName)
	if err != nil {
		return 0, err
	}
	n, err := strconv.ParseUint(strings.TrimSpace(out), 10, 64)
	if err != nil {
		return 0, fmt.Errorf("unexpected lvs output %q for %s/%s", out, vgName, lvName)
	}
	return n, nil
}

// VGFreeBytes reports a volume group's unallocated space.
func (c *Client) VGFreeBytes(ctx context.Context, host, vgName string) (uint64, error) {
	res, err := c.Exec(ctx, []string{host},
		fmt.Sprintf("sudo vgs --noheadings --nosuffix --units b -o vg_free %s", vgName))
	if err != nil {
		return 0, err
	}
	out, err := singleHostOutput(res, "read free space of "+vgName)
	if err != nil {
		return 0, err
	}
	n, err := strconv.ParseUint(strings.TrimSpace(out), 10, 64)
	if err != nil {
		return 0, fmt.Errorf("unexpected vgs output %q for %s", out, vgName)
	}
	return n, nil
}

// singleHostOutput unwraps a one-host ExecResult.
func singleHostOutput(res *ExecResult, what string) (string, error) {
	for _, hr := range res.Hosts {
		if !hr.Success {
			// A command that never ran — the SSH connection itself failed —
			// has no output; its reason is in Error.
			reason := strings.TrimSpace(hr.Output)
			if reason == "" && hr.Error != nil {
				reason = strings.TrimSpace(hr.Error.Error())
			}
			return "", fmt.Errorf("failed to %s: %s", what, reason)
		}
		return hr.Output, nil
	}
	return "", fmt.Errorf("failed to %s: no result", what)
}

// LVCreateSnapshot creates a snapshot of a logical volume
func (c *Client) LVCreateSnapshot(ctx context.Context, hosts []string, vgName, lvName, snapshotName, size string) (*ExecResult, error) {
	lvPath := fmt.Sprintf("%s/%s", vgName, lvName)
	// Create snapshot volume
	cmd := fmt.Sprintf("sudo lvcreate -y -L %s -s -n %s %s", size, snapshotName, lvPath)
	return c.Exec(ctx, hosts, cmd)
}

// LVCreateThinSnapshot creates a snapshot of a thin logical volume
func (c *Client) LVCreateThinSnapshot(ctx context.Context, hosts []string, vgName, lvName, snapshotName string) (*ExecResult, error) {
	// Thin snapshots don't need size, they use the thin pool
	cmd := fmt.Sprintf("sudo lvcreate -s -n %s %s/%s", snapshotName, vgName, lvName)
	return c.Exec(ctx, hosts, cmd)
}

// LVIsThin checks if a logical volume is thin provisioned
func (c *Client) LVIsThin(ctx context.Context, host, vgName, lvName string) (bool, error) {
	// lvs -o segtype --noheadings
	cmd := fmt.Sprintf("sudo lvs -o segtype --noheadings %s/%s", vgName, lvName)
	result, err := c.Exec(ctx, []string{host}, cmd)
	if err != nil {
		return false, err
	}

	for _, r := range result.Hosts {
		if r.Success {
			segType := strings.TrimSpace(r.Output)
			if segType == "thin" {
				return true, nil
			}
		}
	}
	return false, nil
}

// LVRemoveSnapshot removes a snapshot volume
func (c *Client) LVRemoveSnapshot(ctx context.Context, hosts []string, vgName, snapshotName string) (*ExecResult, error) {
	snapPath := fmt.Sprintf("%s/%s", vgName, snapshotName)
	cmd := fmt.Sprintf("sudo lvremove -f %s", snapPath)
	return c.Exec(ctx, hosts, cmd)
}

// LVListSnapshots lists snapshots for a volume group
// Fields are pipe-separated because lv_time contains spaces
// ("2026-08-08 06:00:03 +0000"); with a space separator a caller splitting on
// whitespace silently reads the date as three extra columns.
//
// The command used to carry a literal "VG/LV" — a placeholder copied from the
// lvs man page and never substituted. lvs still printed the snapshots it found,
// but exited 5 because no such volume existed, so any caller that checked the
// exit status threw away a perfectly good answer and reported no snapshots at
// all. The scheduler survived it only because it reads output regardless of
// exit status; `haify resource snapshot list` did not, and showed an empty list
// on a pool holding 27 snapshots.
//
// origin names the volume each snapshot was taken from, which is what lets a
// caller attribute snapshots to a resource rather than to a whole pool.
func (c *Client) LVListSnapshots(ctx context.Context, hosts []string, vgName string) (*ExecResult, error) {
	cmd := fmt.Sprintf("sudo lvs -S lv_role=snapshot -o lv_name,lv_size,lv_time,origin "+
		"--noheadings --nosuffix --units b --separator='|' %s", vgName)
	return c.Exec(ctx, hosts, cmd)
}

// LVMergeSnapshot merges a snapshot back into its origin volume
func (c *Client) LVMergeSnapshot(ctx context.Context, hosts []string, vgName, snapshotName string) (*ExecResult, error) {
	snapPath := fmt.Sprintf("%s/%s", vgName, snapshotName)
	cmd := fmt.Sprintf("sudo lvconvert --merge %s", snapPath)
	return c.Exec(ctx, hosts, cmd)
}

// LVMOption configures LVM operations
type LVMOption func(*lvmOptions)

type lvmOptions struct {
	force bool
}

// WithLVMForce enables force flag for LVM operations
func WithLVMForce(force bool) LVMOption {
	return func(o *lvmOptions) {
		o.force = force
	}
}
