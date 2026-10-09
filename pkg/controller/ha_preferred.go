package controller

import (
	"context"
	"fmt"
	"regexp"
	"strings"

	"go.uber.org/zap"

	"github.com/haify-project/haify/pkg/gateway"
)

// Preferred nodes for an HA resource (`haify ha set-preferred`).
//
// drbd-reactor's promoter can be told which nodes to prefer: `preferred-nodes`
// lists them in order, and each node waits a moment longer the further down
// the list it is before it tries to promote, so the first one that can wins.
// `preferred-nodes-policy = "start-only"` (drbd-reactor 1.9+) uses the order
// only to pick where the resource starts; "always" also moves it back to a
// more preferred node when that one returns, which is a failover of its own.
//
// It is a preference, not a fence: it does not stop any node from promoting
// and plays no part in split-brain avoidance, which is DRBD quorum's job.
// drbd-reactor compares the entries with each node's `uname -n`, so haify node
// names are written as the hostnames recorded at registration.
//
// The new config is written on every node that runs the promoter, and
// drbd-reactor is reloaded on all of them except the one where the resource is
// Primary: the order only matters to the nodes that would take over, and
// reloading a changed promoter where it runs can restart what it started. The
// Primary's drbd-reactor picks the file up at its next reload.

var preferredLineRE = regexp.MustCompile(`(?m)^preferred-nodes(-policy)?\s*=.*\n`)

// withPreferredNodes sets (or, with no nodes, removes) the preferred-nodes
// lines of a promoter config.
func withPreferredNodes(content string, hostnames []string, policy string) string {
	out := preferredLineRE.ReplaceAllString(content, "")
	if len(hostnames) == 0 {
		return out
	}
	quoted := make([]string, len(hostnames))
	for i, h := range hostnames {
		quoted[i] = fmt.Sprintf("%q", h)
	}
	lines := "preferred-nodes = [" + strings.Join(quoted, ", ") + "]\n"
	if policy != "" {
		lines += fmt.Sprintf("preferred-nodes-policy = %q\n", policy)
	}
	header := regexp.MustCompile(`(?m)^\[promoter\.resources\.[^\]]+\]\n`)
	if loc := header.FindStringIndex(out); loc != nil {
		return out[:loc[1]] + lines + out[loc[1]:]
	}
	return out + lines
}

// SetHaPreferredNodes records and applies the preferred nodes of resource's
// HA config. An empty list removes the preference.
func (rm *ResourceManager) SetHaPreferredNodes(ctx context.Context, resource string, nodes []string, policy string) error {
	switch policy {
	case "", "start-only", "always":
	default:
		return fmt.Errorf("the policy is start-only or always, not %q", policy)
	}
	ha, err := rm.controller.db.GetHaConfig(ctx, resource)
	if err != nil || ha == nil {
		return fmt.Errorf("%s has no HA config; create it with `haify ha create` first", resource)
	}
	dbRes, err := rm.controller.db.GetResource(ctx, resource)
	if err != nil || dbRes == nil {
		return fmt.Errorf("resource %q not found", resource)
	}
	members := splitCSV(dbRes.Nodes)
	hostnames := make([]string, 0, len(nodes))
	for _, n := range nodes {
		if !contains(members, n) || (dbRes.WANMode && n == dbRes.DRNode) {
			return fmt.Errorf("%s holds no primary-site replica of %s, so it cannot be preferred", n, resource)
		}
		hostnames = append(hostnames, rm.uname(n))
	}
	hosts, err := rm.failoverHosts(ctx, resource)
	if err != nil {
		return err
	}
	cfg := haTomlPath(resource)
	dump, err := rm.deployment.Exec(ctx, hosts, gateway.ConfigDumpCommand(cfg, cfg+".disabled"))
	if err != nil {
		return err
	}
	primary := ""
	if info, err := rm.GetResource(ctx, resource); err == nil {
		for node, st := range info.NodeStates {
			if strings.EqualFold(st.Role, "Primary") {
				primary = rm.controller.ResolveHost(node)
			}
		}
	}
	var reload []string
	for _, h := range hosts {
		hr := dump.Hosts[h]
		if hr == nil || !hr.Success {
			return fmt.Errorf("%s did not answer; nothing was changed there", rm.nodeLabel(h))
		}
		files, err := gateway.ParseConfigDump(hr.Output)
		if err != nil {
			return err
		}
		for path, content := range files {
			if _, err := rm.deployment.DistributeConfig(ctx, []string{h}, withPreferredNodes(content, hostnames, policy), path); err != nil {
				return fmt.Errorf("write %s on %s: %w", path, rm.nodeLabel(h), err)
			}
		}
		if h != primary {
			reload = append(reload, h)
		}
	}
	if len(reload) > 0 {
		if _, err := rm.deployment.ReactorReload(ctx, reload); err != nil {
			rm.controller.logger.Warn("Reloading drbd-reactor failed", zap.String("resource", resource), zap.Error(err))
		}
	}
	ha.PreferredNodes, ha.PreferredNodesPolicy = nodes, policy
	return rm.controller.db.SaveHaConfig(ctx, ha)
}

// uname is the hostname drbd-reactor knows node by.
func (rm *ResourceManager) uname(node string) string {
	if nodes, err := rm.controller.nodes.ListNodes(context.Background()); err == nil {
		for _, n := range nodes {
			if n.Name == node && n.Hostname != "" {
				return n.Hostname
			}
		}
	}
	return node
}
