package deployment

import (
	"fmt"
	"sort"
	"strings"
)

// ============ Result Types ============

// ConfigResult represents config distribution result
type ConfigResult struct {
	Path    string
	Success bool
	Hosts   map[string]*HostResult
}

// FailedHosts returns the hosts the config did not reach.
func (r *ConfigResult) FailedHosts() []string {
	return hostResultFailures(r.Hosts)
}

// FailureDetails renders failed hosts with the reason each one gave. See
// ExecResult.FailureDetails.
func (r *ConfigResult) FailureDetails() string {
	return hostResultDetails(r.Hosts)
}

// ExecResult represents command execution result
type ExecResult struct {
	Hosts map[string]*HostResult
}

// HostResult represents result for a single host
type HostResult struct {
	Host    string
	Output  string
	Success bool
	Error   error
}

// AllSuccess returns true if all operations succeeded
// combineStreams joins a command's stdout and stderr into the single Output
// field callers inspect, keeping stdout first and skipping empty streams so the
// common (successful, silent) case stays an empty string rather than a newline.
func combineStreams(stdout, stderr string) string {
	out := strings.TrimRight(stdout, "\n")
	errOut := strings.TrimRight(stderr, "\n")
	switch {
	case out == "":
		return errOut
	case errOut == "":
		return out
	default:
		return out + "\n" + errOut
	}
}

func (r *ExecResult) AllSuccess() bool {
	for _, h := range r.Hosts {
		if !h.Success {
			return false
		}
	}
	return true
}

// FailedHosts returns list of failed hosts
func (r *ExecResult) FailedHosts() []string {
	return hostResultFailures(r.Hosts)
}

func hostResultFailures(hosts map[string]*HostResult) []string {
	var failed []string
	for host, h := range hosts {
		if h != nil && !h.Success {
			failed = append(failed, host)
		}
	}
	return failed
}

func hostResultDetails(hosts map[string]*HostResult) string {
	failed := hostResultFailures(hosts)
	if len(failed) == 0 {
		return ""
	}
	sort.Strings(failed)

	parts := make([]string, 0, len(failed))
	for _, host := range failed {
		h := hosts[host]
		reason := ""
		if h != nil {
			reason = strings.TrimSpace(h.Output)
			if reason == "" && h.Error != nil {
				reason = strings.TrimSpace(h.Error.Error())
			}
		}
		if reason == "" {
			reason = "no output"
		}
		// Keep it to one line per host so the error stays greppable.
		reason = strings.Join(strings.Fields(reason), " ")
		parts = append(parts, fmt.Sprintf("%s: %s", host, reason))
	}
	return strings.Join(parts, "; ")
}

// FailureDetails renders the failed hosts *with what the command actually
// said*, e.g.
//
//	192.168.1.10: Device '/dev/sdb' not found; 192.168.1.11: already exists
//
// Error strings built from FailedHosts() alone ("failed on hosts: [10.0.0.1
// 10.0.0.2]") force whoever hit the failure to go SSH into the nodes and replay
// the command by hand, because the reason drbdadm/lvcreate/zfs printed is
// dropped on the floor. Prefer this in user-facing errors; hosts are sorted so
// the message is stable across runs.
func (r *ExecResult) FailureDetails() string {
	return hostResultDetails(r.Hosts)
}
