package controller

import (
	"context"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"go.uber.org/zap"

	"github.com/haify-project/haify/pkg/deployment"
	"github.com/haify-project/haify/pkg/event"
)

// Trimming the filesystems on DRBD devices ([storage.thin] trim_schedule,
// `haify pool trim`).
//
// A filesystem that frees a block tells nobody: the thin pool under every
// replica keeps it allocated. Worse, a full resync writes the zeros of every
// block the filesystem does not use, so after one a 1G volume holding 50M of
// files holds 1G of every replica's pool. fstrim on the node serving the
// filesystem discards the free blocks; DRBD passes the discards to every
// peer, and each peer's thin pool lets the blocks go.
//
// Only a mounted filesystem can be trimmed, and only the Primary mounts one,
// so the sweep simply trims every DRBD-backed mount on every node. A raw block
// device (an iSCSI LUN, a CSI block volume) is the initiator's to trim.

const trimTimeout = 15 * time.Minute

// trimScript prints one "mount|exit|fstrim output" line per DRBD-backed
// ext4/xfs mount. A suspended device would block fstrim forever, hence the
// timeout.
const trimScript = `for t in $(findmnt -rn -t ext4,xfs -o TARGET,SOURCE | awk '$2 ~ "^/dev/drbd" {print $1}'); do
  out=$(timeout 900 fstrim -v "$t" 2>&1); rc=$?
  echo "$t|$rc|$(echo "$out" | tr '\n' ' ')"
done`

// TrimResult is one filesystem's trim.
type TrimResult struct {
	Node  string
	Mount string
	Bytes uint64
	Err   string
}

var trimBytesRE = regexp.MustCompile(`\((\d+) bytes\) trimmed`)

// parseTrimOutput reads trimScript's output from one node.
func parseTrimOutput(node, out string) []TrimResult {
	var res []TrimResult
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		parts := strings.SplitN(strings.TrimSpace(line), "|", 3)
		if len(parts) != 3 || parts[0] == "" {
			continue
		}
		r := TrimResult{Node: node, Mount: strings.ReplaceAll(parts[0], `\x20`, " ")}
		if m := trimBytesRE.FindStringSubmatch(parts[2]); m != nil {
			r.Bytes, _ = strconv.ParseUint(m[1], 10, 64)
		}
		if parts[1] != "0" {
			r.Err = strings.TrimSpace(parts[2])
			switch {
			case parts[1] == "124":
				r.Err = "timed out (is the device suspended?)"
			case r.Err == "":
				r.Err = "fstrim exited " + parts[1]
			}
		}
		res = append(res, r)
	}
	return res
}

// Trim trims every DRBD-backed filesystem on node, or on every online node
// when node is empty.
func (sm *StorageManager) Trim(ctx context.Context, node string) ([]TrimResult, error) {
	c := sm.controller
	nodes, err := c.nodes.ListNodes(ctx)
	if err != nil {
		return nil, err
	}
	var hosts []string
	nameOf := map[string]string{}
	for _, n := range nodes {
		if node != "" && n.Name != node && n.Address != node {
			continue
		}
		if node == "" && n.State != NodeStateOnline {
			continue
		}
		hosts = append(hosts, n.Address)
		nameOf[n.Address] = n.Name
	}
	if len(hosts) == 0 {
		if node != "" {
			return nil, fmt.Errorf("node %q is not registered", node)
		}
		return nil, nil
	}
	cmd := "echo " + base64Std(trimScript) + " | base64 -d | sudo /bin/bash"
	res, err := c.deployment.Exec(ctx, hosts, cmd, deployment.WithExecTimeout(trimTimeout))
	if err != nil {
		return nil, err
	}
	var out []TrimResult
	for _, h := range hosts {
		hr := res.Hosts[h]
		if hr == nil {
			out = append(out, TrimResult{Node: nameOf[h], Err: "no answer"})
			continue
		}
		if !hr.Success && strings.TrimSpace(hr.Output) == "" {
			out = append(out, TrimResult{Node: nameOf[h], Err: "trim failed"})
			continue
		}
		out = append(out, parseTrimOutput(nameOf[h], hr.Output)...)
	}
	return out, nil
}

// trimSummary is one line about a sweep, for the log and the event.
func trimSummary(results []TrimResult) (string, bool) {
	var total uint64
	var failed []string
	mounts := 0
	for _, r := range results {
		if r.Err != "" {
			failed = append(failed, fmt.Sprintf("%s:%s (%s)", r.Node, r.Mount, r.Err))
			continue
		}
		if r.Mount != "" {
			mounts++
			total += r.Bytes
		}
	}
	msg := fmt.Sprintf("trimmed %d filesystem(s), %s of free space discarded", mounts, formatBytes(total))
	if len(failed) > 0 {
		msg += "; failed: " + strings.Join(failed, ", ")
	}
	return msg, len(failed) > 0
}

var trimming atomic.Bool

// runTrimSweep is the scheduled trim.
func (sm *ScheduleManager) runTrimSweep() {
	if !trimming.CompareAndSwap(false, true) {
		return
	}
	defer trimming.Store(false)
	c := sm.controller
	results, err := c.storage.Trim(context.Background(), "")
	if err != nil {
		c.logger.Warn("Trim sweep failed", zap.Error(err))
		return
	}
	msg, failed := trimSummary(results)
	c.logger.Info("Trim sweep", zap.String("result", msg))
	if c.events != nil {
		sev := event.SeverityInfo
		if failed {
			sev = event.SeverityWarning
		}
		c.events.Publish(event.Event{Type: event.TypePoolTrimmed, Severity: sev, Status: event.StatusInfo, Message: msg})
	}
}

func (sm *ScheduleManager) trimSchedule() string {
	if sm.controller.config == nil {
		return ""
	}
	return strings.TrimSpace(sm.controller.config.Storage.Thin.TrimSchedule)
}
