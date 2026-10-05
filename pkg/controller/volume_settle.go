package controller

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"go.uber.org/zap"
)

// A volume added to a running resource is empty everywhere, so AddVolume skips
// its initial sync: a new current UUID with a cleared bitmap on one node marks
// every peer UpToDate. That only reaches peers whose disk has finished its
// handshake for the new volume. A peer still Negotiating when the bitmap is
// cleared is left Inconsistent on an Established connection, and DRBD starts no
// resync for it: the replica looks connected and stays useless. Nothing shows
// until a failover to that node, where the mount blocks on the volume forever
// (a gateway's state volume, seen on a test cluster).
//
// settleNewVolume therefore waits for the handshake first, and afterwards
// checks that every diskful peer is UpToDate, refusing to report success
// otherwise.

const (
	volumeHandshakeWait = 30 * time.Second
	volumeUpToDateWait  = 15 * time.Second
)

// volumePeerStates reads, on address, each peer's state for volume vol of
// resource, keyed by peer name.
func (rm *ResourceManager) volumePeerStates(ctx context.Context, address, resource string, vol int) (map[string]drbdPeerDevice, error) {
	res, err := rm.deployment.Exec(ctx, []string{address}, "sudo drbdsetup status "+resource+" --json")
	if err != nil {
		return nil, err
	}
	hr := res.Hosts[address]
	if hr == nil || !hr.Success {
		return nil, fmt.Errorf("drbdsetup status %s failed on %s", resource, address)
	}
	return parseVolumePeerStates(hr.Output, vol)
}

// parseVolumePeerStates extracts volume vol's peer devices from `drbdsetup
// status --json`. A peer whose connection is down carries no peer device; it
// is reported with its connection state as the replication state, so it reads
// as not settled.
func parseVolumePeerStates(output string, vol int) (map[string]drbdPeerDevice, error) {
	var resources []drbdsetupStatus
	if err := json.Unmarshal([]byte(strings.TrimSpace(output)), &resources); err != nil {
		return nil, fmt.Errorf("decode drbdsetup status json: %w", err)
	}
	if len(resources) == 0 {
		return nil, fmt.Errorf("drbdsetup status json contained no resources")
	}
	peers := map[string]drbdPeerDevice{}
	for _, c := range resources[0].Connections {
		pd := drbdPeerDevice{Volume: vol, ReplicationState: c.ConnectionState, PeerDiskState: "DUnknown"}
		for _, d := range c.PeerDevices {
			if d.Volume == vol {
				pd = d
			}
		}
		peers[c.Name] = pd
	}
	return peers, nil
}

// unsettledPeers names the peers whose disk has not finished its handshake for
// the volume: not Established, or a disk state DRBD has not determined yet.
func unsettledPeers(peers map[string]drbdPeerDevice) []string {
	var out []string
	for name, pd := range peers {
		switch {
		case pd.ReplicationState != "Established":
		case pd.PeerDiskState == "DUnknown", pd.PeerDiskState == "Negotiating", pd.PeerDiskState == "Attaching":
		default:
			continue
		}
		out = append(out, fmt.Sprintf("%s (%s/%s)", name, pd.ReplicationState, pd.PeerDiskState))
	}
	sort.Strings(out)
	return out
}

// peersNotUpToDate names the diskful peers that are not UpToDate.
func peersNotUpToDate(peers map[string]drbdPeerDevice) []string {
	var out []string
	for name, pd := range peers {
		if pd.PeerDiskState != "UpToDate" && pd.PeerDiskState != "Diskless" {
			out = append(out, fmt.Sprintf("%s (%s/%s)", name, pd.ReplicationState, pd.PeerDiskState))
		}
	}
	sort.Strings(out)
	return out
}

// waitVolumePeers polls volume vol's peers on address until done reports none
// left, or wait passes; it returns the last ones reported.
func (rm *ResourceManager) waitVolumePeers(ctx context.Context, address, resource string, vol int, wait time.Duration,
	done func(map[string]drbdPeerDevice) []string) ([]string, error) {
	deadline := time.Now().Add(wait)
	var left []string
	for {
		peers, err := rm.volumePeerStates(ctx, address, resource, vol)
		if err == nil {
			if left = done(peers); len(left) == 0 {
				return nil, nil
			}
		} else {
			left = []string{err.Error()}
		}
		if time.Now().After(deadline) {
			return left, nil
		}
		select {
		case <-ctx.Done():
			return left, ctx.Err()
		case <-time.After(time.Second):
		}
	}
}

// settleNewVolume declares a freshly added, empty volume UpToDate on every
// replica without a sync, as described above.
func (rm *ResourceManager) settleNewVolume(ctx context.Context, address, resource string, vol int) error {
	left, err := rm.waitVolumePeers(ctx, address, resource, vol, volumeHandshakeWait, unsettledPeers)
	if err != nil {
		return err
	}
	if len(left) > 0 {
		return fmt.Errorf("volume %d of %s did not attach on every peer within %s: %s", vol, resource,
			volumeHandshakeWait, strings.Join(left, ", "))
	}
	cmd := fmt.Sprintf("sudo drbdadm new-current-uuid --clear-bitmap %s/%d", resource, vol)
	if err := rm.execAllSuccess(ctx, []string{address}, cmd, "failed to initialize new volume sync state"); err != nil {
		return err
	}
	left, err = rm.waitVolumePeers(ctx, address, resource, vol, volumeUpToDateWait, peersNotUpToDate)
	if err != nil {
		return err
	}
	if len(left) > 0 {
		rm.controller.logger.Warn("New volume is not UpToDate on every peer",
			zap.String("resource", resource), zap.Int("volume", vol), zap.Strings("peers", left))
		return fmt.Errorf("volume %d of %s is not UpToDate on %s after its sync was skipped; it would block on a "+
			"failover there. Copy it over with `drbdadm invalidate-remote %s:<peer>/%d` on %s",
			vol, resource, strings.Join(left, ", "), resource, vol, address)
	}
	return nil
}
