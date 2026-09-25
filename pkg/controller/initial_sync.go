package controller

import (
	"context"
	"encoding/base64"
	"fmt"
	"strings"
	"time"

	"go.uber.org/zap"
)

// A brand-new resource on thin storage has nothing to synchronise: every
// replica is a freshly allocated thin LV or zvol, and an unwritten block of
// either reads as zeros. Running the initial sync anyway copies a volume's
// worth of zeros to every peer, and a thin target stores them — each secondary
// replica came out 100% allocated however little had been written, which is
// thin provisioning undone on every node but one, and a sync as long as the
// volume is big.
//
// So on thin storage the replicas are declared identical instead: a new
// current UUID with a cleared bitmap, the way AddVolume already brings up a
// new volume and the way LINSTOR does it. Thick LVM keeps the full sync: a
// freshly allocated LV holds whatever the extents held before, and replicas
// that differ in blocks nobody wrote would fail an online verify and could
// surface stale data.

// backedByZeroReadingStorage reports whether every diskful replica of a new
// resource sits on storage whose unwritten blocks read as zeros, so the
// replicas start identical.
//
// It asks what was actually built rather than trusting storageType: the CLI's
// --storage-type defaults to "lvm", and on a thin pool createBackingVolume
// builds a thin LV anyway, so the flag alone would send a thin resource
// through the full sync this exists to avoid.
//
// An encrypted volume never qualifies. Under LUKS an unwritten block reads as
// that node's decryption of zeros — noise, and different noise on every node,
// since each keeps its own key.
func (rm *ResourceManager) backedByZeroReadingStorage(ctx context.Context, storageType string, nodeIPs []string, vols []resolvedVolume) bool {
	for _, v := range vols {
		if v.encrypted {
			return false
		}
	}
	switch strings.ToLower(strings.TrimSpace(storageType)) {
	case "zfs", "zfs-thin":
		return true
	}
	for _, v := range vols {
		for _, ip := range nodeIPs {
			thin, err := rm.deployment.LVIsThin(ctx, ip, v.pool, v.volumeName)
			if err != nil || !thin {
				return false
			}
		}
	}
	return true
}

// peerConnectWait bounds how long a new resource may take to connect before
// the initial sync falls back to a full one.
const peerConnectWait = 30 * time.Second

// skipInitialSync marks a brand-new resource UpToDate on every replica without
// syncing, once all of its peers are connected. A peer that is not connected
// when the bitmap is cleared would still get a full sync when it does connect,
// so a resource that does not connect in time returns an error and the caller
// falls back to the ordinary initial sync.
func (rm *ResourceManager) skipInitialSync(ctx context.Context, resource, address string) error {
	deadline := time.Now().Add(peerConnectWait)
	for {
		res, err := rm.deployment.Exec(ctx, []string{address}, "sudo drbdsetup status "+resource)
		if err == nil && res.AllSuccess() {
			out := ""
			for _, h := range res.Hosts {
				out = h.Output
			}
			// Plain `drbdsetup status` names the connection state only when it
			// is not Connected.
			if strings.Contains(out, "role:") && !strings.Contains(out, "connection:") {
				break
			}
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("peers of %s did not connect within %s", resource, peerConnectWait)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(time.Second):
		}
	}

	rm.controller.logger.Info("Thin storage: skipping the initial sync (new current UUID, cleared bitmap)",
		zap.String("resource", resource), zap.String("address", address))
	// new-current-uuid takes one volume at a time. The verbose status lists
	// this node's volumes as two-space-indented "volume:N" lines; the
	// deeper-indented ones belong to peers.
	cmd := fmt.Sprintf(`set -e
vols=$(drbdsetup status %[1]s --verbose | grep -oE '^  volume:[0-9]+' | cut -d: -f2)
[ -n "$vols" ]
for v in $vols; do drbdadm new-current-uuid --clear-bitmap %[1]s/$v; done`, resource)
	return rm.execAllSuccess(ctx, []string{address},
		"echo "+base64Std(cmd)+" | base64 -d | sudo /bin/bash", "skip initial sync")
}

func base64Std(s string) string { return base64.StdEncoding.EncodeToString([]byte(s)) }
