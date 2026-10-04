package controller

import (
	"context"
	"fmt"
	"strings"

	"github.com/haify-project/sds/pkg/database"
)

// Adding a replica while a member is away.
//
// Adding a replica rewrites the config on every member and adjusts them all,
// so one member that does not answer used to block it — and that is exactly
// when a replica is most wanted: a node is down and the resource is short a
// copy. With allow_unreachable the add goes ahead on the members that answer,
// provided they are the majority of the resource's members (so the add cannot
// be what a minority partition does on its own). The members that were away
// keep their old config, which simply does not know the newcomer; they are
// recorded and repaired when they answer again (self_heal_return.go).
//
// A WAN resource's DR node is not skipped: its tunnel has to be rewritten
// for the newcomer, and that cannot wait.

// unreachableMembers returns the members (diskful and tiebreakers) that do
// not answer over SSH, or an error when that blocks the add.
func (rm *ResourceManager) unreachableMembers(ctx context.Context, dbRes *database.Resource, primaries, tiebreakers []string, allow bool) ([]string, error) {
	members := append(append([]string(nil), primaries...), tiebreakers...)
	var away []string
	for _, n := range members {
		if !rm.answers(ctx, rm.controller.ResolveHost(n)) {
			away = append(away, n)
		}
	}
	if dbRes.WANMode && dbRes.DRNode != "" && !rm.answers(ctx, rm.controller.ResolveHost(dbRes.DRNode)) {
		return nil, fmt.Errorf("the DR node %s does not answer; its WAN tunnel has to be rewritten for a new replica", dbRes.DRNode)
	}
	if len(away) == 0 {
		return nil, nil
	}
	if !allow {
		return nil, fmt.Errorf("member(s) %s of %s do not answer; add the replica anyway with allow_unreachable "+
			"(--allow-unreachable), and they get the new config when they are back",
			strings.Join(away, ", "), dbRes.Name)
	}
	if 2*len(away) >= len(members) {
		return nil, fmt.Errorf("%d of the %d members of %s do not answer; adding a replica needs the majority",
			len(away), len(members), dbRes.Name)
	}
	if len(withoutAll(primaries, away)) == 0 {
		return nil, fmt.Errorf("no replica of %s answers to copy from", dbRes.Name)
	}
	return away, nil
}

// withoutAll is list without any of drop.
func withoutAll(list, drop []string) []string {
	out := make([]string, 0, len(list))
	for _, v := range list {
		if !contains(drop, v) {
			out = append(out, v)
		}
	}
	return out
}
