package controller

import (
	"context"
	"errors"
	"fmt"
	"path"
	"sort"
	"strings"

	"go.uber.org/zap"

	"github.com/haify-project/sds/pkg/gateway"
)

// Where a resource's drbd-reactor promoters live.
//
// A promoter may run on a node that holds an UpToDate copy of the data and is
// meant to take over automatically: the resource's diskful replicas at the
// primary site. Not a tiebreaker or a diskless client, which hold no copy, and
// not the DR node of a WAN resource, which holds an asynchronous copy that may
// be behind — failing over to it is a manual, lossy decision
// (docs/design/wan-replication.md), and a promoter there turned it into an
// automatic one the first time the primary site demoted.
//
// Promoters were written to those nodes when an HA config or a gateway was
// created or edited, and at no other time. A replica added later had no
// promoter and could never take over; a replica removed kept one. SyncPromoters
// puts them right, and runs after every change to the replica set.

// failoverHosts returns the resolved addresses of the nodes that may run
// resource's promoters: its diskful replicas, without the DR node.
func (rm *ResourceManager) failoverHosts(ctx context.Context, resource string) ([]string, error) {
	hosts, err := rm.resourceHosts(ctx, resource)
	if err != nil {
		return nil, err
	}
	if dr := rm.drHost(ctx, resource); dr != "" {
		hosts = without(hosts, dr)
	}
	if len(hosts) == 0 {
		return nil, fmt.Errorf("resource %q has no primary-site replica to run a promoter on", resource)
	}
	return hosts, nil
}

// drHost is the resolved address of resource's DR node, or "" for a LAN
// resource.
func (rm *ResourceManager) drHost(ctx context.Context, resource string) string {
	if rm.controller.db == nil {
		return ""
	}
	dbRes, err := rm.controller.db.GetResource(ctx, resource)
	if err != nil || dbRes == nil || !dbRes.WANMode || dbRes.DRNode == "" {
		return ""
	}
	return rm.controller.ResolveHost(dbRes.DRNode)
}

// SyncPromoters makes every SDS promoter of resource — its `ha create` config
// and its gateway — present on the nodes failoverHosts names and absent from
// every other node. former names nodes that held a replica until just now,
// which are retired even when they are not registered nodes.
//
// The controller's own resource is left alone: Self-HA places its promoter
// together with the controller binary and unit it starts (selfha.go).
func (rm *ResourceManager) SyncPromoters(ctx context.Context, resource string, former ...string) error {
	if resource == SelfHaResource {
		return nil
	}
	run, err := rm.failoverHosts(ctx, resource)
	if err != nil {
		return err
	}
	var errs []error
	if err := rm.syncHaPlacement(ctx, resource, run, former); err != nil {
		errs = append(errs, err)
	}
	if rm.controller.gateway != nil {
		if err := rm.controller.gateway.SyncPlacement(ctx, resource); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// syncHaPlacement installs resource's HA config on the run hosts that lack it
// and retires it from every other node.
//
// Hosts that already hold it are not written to: on the node running the
// service chain a rewrite plus reload is a restart of that chain.
func (rm *ResourceManager) syncHaPlacement(ctx context.Context, resource string, run, former []string) error {
	cfg := haTomlPath(resource)
	res, err := rm.deployment.Exec(ctx, run, gateway.ConfigDumpCommand(cfg, cfg+".disabled"))
	if err != nil {
		return fmt.Errorf("read the HA config of %s: %w", resource, err)
	}

	type copyOf struct {
		host, content string
		disabled      bool
	}
	var copies []copyOf
	var missing []string
	for _, h := range run {
		hr := res.Hosts[h]
		if hr == nil || !hr.Success {
			rm.controller.logger.Warn("Replica did not answer; its HA promoter is left as it is",
				zap.String("resource", resource), zap.String("host", h))
			continue
		}
		files, perr := gateway.ParseConfigDump(hr.Output)
		if perr != nil {
			return fmt.Errorf("read the HA config of %s on %s: %w", resource, rm.nodeLabel(h), perr)
		}
		if c, ok := files[cfg]; ok {
			copies = append(copies, copyOf{host: h, content: c})
		} else if c, ok := files[cfg+".disabled"]; ok {
			copies = append(copies, copyOf{host: h, content: c, disabled: true})
		} else {
			missing = append(missing, h)
		}
	}

	if len(copies) > 0 && len(missing) > 0 {
		// The live copy most replicas hold; the first host in order on a tie.
		sort.SliceStable(copies, func(i, j int) bool { return !copies[i].disabled && copies[j].disabled })
		count := map[string]int{}
		for _, c := range copies {
			count[c.content]++
		}
		chosen := copies[0]
		for _, c := range copies[1:] {
			if c.disabled == chosen.disabled && count[c.content] > count[chosen.content] {
				chosen = c
			}
		}
		if err := rm.installHaPromoter(ctx, resource, chosen.host, chosen.content, chosen.disabled, missing); err != nil {
			return err
		}
	}

	runSet := map[string]bool{}
	for _, h := range run {
		runSet[h] = true
	}
	var rest []string
	seen := map[string]bool{}
	for _, h := range append(rm.allNodeAddresses(), rm.resolveAll(former)...) {
		if h != "" && !runSet[h] && !seen[h] {
			seen[h] = true
			rest = append(rest, h)
		}
	}
	rm.retireHaPromoter(ctx, resource, rest)
	return nil
}

// installHaPromoter copies an HA config, and the mount units its chain
// starts, from src to hosts — after checking the hosts can run the chain.
func (rm *ResourceManager) installHaPromoter(ctx context.Context, resource, src, content string, disabled bool, hosts []string) error {
	chain, err := gateway.PromoterChain(content)
	if err != nil {
		return fmt.Errorf("HA config of %s: %w", resource, err)
	}
	if err := rm.checkHaChain(ctx, chain, hosts); err != nil {
		return fmt.Errorf("the HA config of %s cannot run on %s: %w", resource, rm.nodeLabels(hosts), err)
	}

	var mounts []string
	vip := false
	for _, e := range chain {
		switch {
		case strings.HasSuffix(e.Unit, ".mount"):
			mounts = append(mounts, path.Join("/etc/systemd/system", e.Unit))
		case strings.HasPrefix(e.Unit, "service-ip@"):
			vip = true
		}
	}
	if vip {
		if err := rm.ensureServiceIP(ctx, hosts); err != nil {
			return err
		}
	}
	if len(mounts) > 0 {
		res, err := rm.deployment.Exec(ctx, []string{src}, gateway.ConfigDumpCommand(mounts...))
		if err != nil || res == nil || res.Hosts[src] == nil || !res.Hosts[src].Success {
			return fmt.Errorf("read the mount units of %s from %s: %v", resource, rm.nodeLabel(src), err)
		}
		units, err := gateway.ParseConfigDump(res.Hosts[src].Output)
		if err != nil {
			return err
		}
		for _, p := range mounts {
			unit, ok := units[p]
			if !ok {
				return fmt.Errorf("the HA config of %s starts %s, which %s does not have", resource, path.Base(p), rm.nodeLabel(src))
			}
			if _, err := rm.deployment.DistributeConfig(ctx, hosts, unit, p); err != nil {
				return fmt.Errorf("install %s: %w", path.Base(p), err)
			}
		}
		if _, err := rm.deployment.Exec(ctx, hosts, "sudo systemctl daemon-reload"); err != nil {
			rm.controller.logger.Warn("daemon-reload failed after installing mount units", zap.Error(err))
		}
	}

	dest := haTomlPath(resource)
	if disabled {
		dest += ".disabled"
	}
	if _, err := rm.deployment.DistributeConfig(ctx, hosts, content, dest); err != nil {
		return fmt.Errorf("install the HA config of %s: %w", resource, err)
	}
	if !disabled {
		if _, err := rm.deployment.ReactorReload(ctx, hosts); err != nil {
			rm.controller.logger.Warn("drbd-reactor reload failed after placing an HA config",
				zap.String("resource", resource), zap.Error(err))
		}
	}
	rm.controller.logger.Info("HA promoter placed on new replicas",
		zap.String("resource", resource), zap.Strings("hosts", hosts), zap.String("copied_from", src))
	return nil
}

// checkHaChain verifies that every entry of an HA chain can start on hosts:
// the OCF agents exist and every plain unit is installed. Mount units and
// service-ip are not checked, because installHaPromoter provides them.
func (rm *ResourceManager) checkHaChain(ctx context.Context, chain []gateway.ChainEntry, hosts []string) error {
	var checks []string
	for _, e := range chain {
		switch {
		case e.AgentPath != "":
			checks = append(checks, fmt.Sprintf(`[ -x %s ] || echo "missing OCF agent %s"`,
				shellSingleQuote(e.AgentPath), e.AgentPath))
		case strings.HasSuffix(e.Unit, ".mount"), strings.HasPrefix(e.Unit, "service-ip@"):
		default:
			checks = append(checks, fmt.Sprintf(
				`[ "$(systemctl show -p LoadState --value %[1]s 2>/dev/null)" = loaded ] || echo "missing unit %[2]s"`,
				shellSingleQuote(e.Unit), e.Unit))
		}
	}
	if len(checks) == 0 {
		return nil
	}
	cmd := "echo " + base64Std(strings.Join(checks, "\n")+"\ntrue\n") + " | base64 -d | /bin/sh"
	res, err := rm.deployment.Exec(ctx, hosts, cmd)
	if err != nil {
		return err
	}
	var problems []string
	for _, h := range hosts {
		hr := res.Hosts[h]
		if hr == nil || !hr.Success {
			problems = append(problems, fmt.Sprintf("%s: could not be checked (%s)", rm.nodeLabel(h), hostFailure(hr)))
			continue
		}
		for _, line := range strings.Split(hr.Output, "\n") {
			if line = strings.TrimSpace(line); strings.HasPrefix(line, "missing ") {
				problems = append(problems, rm.nodeLabel(h)+": "+line)
			}
		}
	}
	if len(problems) > 0 {
		return fmt.Errorf("%s; install them there first", strings.Join(problems, "; "))
	}
	return nil
}

// retireHaPromoter removes resource's HA config from hosts and stops its
// service chain there — except on a node holding the resource Primary with
// UpToDate data, which is a DR node after a manual failover serving what is
// left: it loses only the promoter, never the service.
func (rm *ResourceManager) retireHaPromoter(ctx context.Context, resource string, hosts []string) {
	if len(hosts) == 0 {
		return
	}
	script := fmt.Sprintf(`found=
for f in %[1]s %[1]s.disabled; do
  [ -e "$f" ] && rm -f "$f" && found=1
done
[ -n "$found" ] || exit 0
systemctl reload drbd-reactor || systemctl restart drbd-reactor
st=$(drbdsetup status %[2]s 2>/dev/null)
if printf '%%s\n' "$st" | head -n1 | grep -q 'role:Primary' && printf '%%s\n' "$st" | grep -Eq '^ +(volume:[0-9]+ +)?disk:UpToDate'; then
  echo "kept the services of %[2]s running: this node is Primary with UpToDate data"
  exit 0
fi
systemctl stop %[3]s 2>/dev/null
true`, haTomlPath(resource), resource, shellSingleQuote(fmt.Sprintf("drbd-services@%s.target", strings.ReplaceAll(resource, "-", `\x2d`))))
	cmd := "echo " + base64Std(script) + " | base64 -d | sudo /bin/sh"
	res, err := rm.deployment.Exec(ctx, hosts, cmd)
	if err != nil {
		rm.controller.logger.Warn("Failed to retire the HA promoter from nodes that must not run it",
			zap.String("resource", resource), zap.Strings("hosts", hosts), zap.Error(err))
		return
	}
	for h, hr := range res.Hosts {
		if hr != nil && !hr.Success {
			rm.controller.logger.Warn("Could not retire the HA promoter; remove it there when the node is back",
				zap.String("resource", resource), zap.String("node", rm.nodeLabel(h)),
				zap.String("config", haTomlPath(resource)))
		}
	}
}

// checkPromoterPrereqs refuses, before anything is provisioned, a new replica
// on a node that could not run the resource's HA chain or gateway.
func (rm *ResourceManager) checkPromoterPrereqs(ctx context.Context, resource string, hosts []string) error {
	if resource == SelfHaResource {
		return nil
	}
	if rm.controller.db != nil && rm.controller.gateway != nil {
		if gw, err := rm.controller.db.GetGatewayByResource(ctx, resource); err == nil && gw != nil {
			if err := rm.controller.gateway.CheckPlacementPrereqs(ctx, string(gw.Type), hosts); err != nil {
				return fmt.Errorf("%s is exported by gateway %q, which %s could not run: %w",
					resource, gw.Name, rm.nodeLabels(hosts), err)
			}
		}
	}
	if _, content, err := rm.GetHaToml(ctx, resource); err == nil && strings.TrimSpace(content) != "" {
		chain, perr := gateway.PromoterChain(content)
		if perr != nil {
			return fmt.Errorf("HA config of %s: %w", resource, perr)
		}
		if err := rm.checkHaChain(ctx, chain, hosts); err != nil {
			return fmt.Errorf("%s has an HA config whose chain cannot start on %s: %w", resource, rm.nodeLabels(hosts), err)
		}
	}
	return nil
}

// resolveAll resolves node names to addresses.
func (rm *ResourceManager) resolveAll(nodes []string) []string {
	out := make([]string, 0, len(nodes))
	for _, n := range nodes {
		out = append(out, rm.controller.ResolveHost(n))
	}
	return out
}
