package controller

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"go.uber.org/zap"
)

// Turning TLS on or off for a resource. DRBD refuses to change a connection's
// transport while it is up (net_conf_change: -EINVAL), so every connection has
// to go down and come back with the new options. Taking them all down at once
// would drop the Primary below quorum; instead each link is cycled on its own —
// disconnect both ends, load the new net options, connect — while the others
// keep the votes. A link whose handshake fails is left StandAlone by DRBD and
// never retried, so the switch stops there and names it.

// tlsLinkTimeout is how long one link may take to come back.
const tlsLinkTimeout = 60 * time.Second

// SetResourceTLS switches encrypted replication on or off for every connection
// of resource, and returns how many links it changed.
func (rm *ResourceManager) SetResourceTLS(ctx context.Context, resource string, on bool) (int, error) {
	info, err := rm.GetResource(ctx, resource)
	if err != nil {
		return 0, err
	}
	if info.WANMode {
		return 0, fmt.Errorf("%s replicates off-site through haify-proxy, which already runs mutual TLS; DRBD TLS is for the local links", resource)
	}
	members := tlsMembers(info)
	if on {
		if err := rm.assertTLSReady(ctx, members); err != nil {
			return 0, err
		}
	}
	value := "no"
	if on {
		value = "yes"
	}
	if _, _, err := rm.stageResourceConfig(ctx, resource, func(current string) (string, error) {
		return applyDrbdOptions(current, map[string]string{"net/tls": value})
	}); err != nil {
		return 0, err
	}

	changed := 0
	for i, a := range members {
		for _, b := range members[i+1:] {
			if rm.linkTLS(ctx, resource, a, b) == on {
				continue
			}
			if err := rm.cycleLink(ctx, resource, a, b, on); err != nil {
				return changed, err
			}
			changed++
		}
	}
	rm.controller.logger.Info("Switched replication TLS",
		zap.String("resource", resource), zap.Bool("tls", on), zap.Int("links", changed))
	return changed, nil
}

// tlsMembers is every node in the resource's connection mesh.
func tlsMembers(info *ResourceInfo) []string {
	var out []string
	seen := map[string]bool{}
	for _, list := range [][]string{info.Nodes, info.DisklessNodes, info.DisklessClients} {
		for _, n := range list {
			if n != "" && !seen[n] {
				seen[n] = true
				out = append(out, n)
			}
		}
	}
	return out
}

// assertTLSReady refuses when any of nodes cannot complete a handshake: DRBD
// would leave that link StandAlone.
func (rm *ResourceManager) assertTLSReady(ctx context.Context, nodes []string) error {
	states, err := rm.ReplicationTLSStatus(ctx, nodes)
	if err != nil {
		return err
	}
	var bad []string
	for _, s := range states {
		if !s.Ready {
			bad = append(bad, fmt.Sprintf("%s (%s)", s.Node, s.Problem))
		}
	}
	if len(bad) > 0 {
		return fmt.Errorf("not every node can carry an encrypted connection: %s", strings.Join(bad, "; "))
	}
	return nil
}

// cycleLink takes the a–b connection down at both ends and brings it back with
// the options now in the config.
func (rm *ResourceManager) cycleLink(ctx context.Context, resource, a, b string, on bool) error {
	steps := []struct{ node, peer, cmd string }{
		{a, b, "disconnect"}, {b, a, "disconnect"},
		{a, b, "net-options"}, {b, a, "net-options"},
		{a, b, "connect"}, {b, a, "connect"},
	}
	for _, s := range steps {
		cmd := fmt.Sprintf("sudo drbdadm %s %s:%s", s.cmd, resource, s.peer)
		if s.cmd == "disconnect" {
			cmd += " 2>/dev/null || true"
		}
		if err := rm.execAllSuccess(ctx, []string{rm.controller.ResolveHost(s.node)}, cmd,
			fmt.Sprintf("%s %s on %s", s.cmd, resource, s.node)); err != nil {
			return err
		}
	}
	deadline := time.Now().Add(tlsLinkTimeout)
	for {
		state, tls := rm.linkState(ctx, resource, a, b)
		if state == "Connected" && tls == on {
			return nil
		}
		if state == "StandAlone" || time.Now().After(deadline) {
			return fmt.Errorf("the %s link of %s did not come back with tls=%v (it is %s); see `journalctl -u tlshd` on both nodes, then `haify resource repair %s`",
				a+"–"+b, resource, on, orUnknown(state), resource)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(2 * time.Second):
		}
	}
}

// linkTLS reports whether the a–b link is currently up with TLS.
func (rm *ResourceManager) linkTLS(ctx context.Context, resource, a, b string) bool {
	state, tls := rm.linkState(ctx, resource, a, b)
	return state == "Connected" && tls
}

// linkState reads the a–b connection as a sees it.
func (rm *ResourceManager) linkState(ctx context.Context, resource, a, b string) (string, bool) {
	host := rm.controller.ResolveHost(a)
	res, err := rm.deployment.Exec(ctx, []string{host}, "sudo drbdsetup status "+resource+" --json")
	if err != nil || res == nil || !res.AllSuccess() {
		return "", false
	}
	var status []struct {
		Connections []struct {
			Name  string `json:"name"`
			State string `json:"connection-state"`
			TLS   bool   `json:"tls"`
		} `json:"connections"`
	}
	if json.Unmarshal([]byte(hostOutput(res, host)), &status) != nil {
		return "", false
	}
	for _, r := range status {
		for _, c := range r.Connections {
			if c.Name == b {
				return c.State, c.TLS
			}
		}
	}
	return "", false
}

// assertNewMemberTLS refuses to add node to a resource whose connections are
// encrypted when node cannot complete the handshake.
func (rm *ResourceManager) assertNewMemberTLS(ctx context.Context, resource, node string) error {
	hosts, err := rm.resourceHosts(ctx, resource)
	if err != nil || len(hosts) == 0 {
		return nil
	}
	res, err := rm.deployment.Exec(ctx, hosts[:1],
		fmt.Sprintf("grep -Eq '^[[:space:]]*tls[[:space:]]+yes;' /etc/drbd.d/%s.res && echo HAIFY_TLS=yes; true", resource))
	if err != nil || res == nil {
		return nil
	}
	if _, ok := tlsField(hostOutput(res, hosts[0]), "HAIFY_TLS"); !ok {
		return nil
	}
	if err := rm.assertTLSReady(ctx, []string{node}); err != nil {
		return fmt.Errorf("%s replicates over TLS: %w", resource, err)
	}
	return nil
}
