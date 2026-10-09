package controller

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/haify-project/haify/pkg/database"
	"go.uber.org/zap"
)

// A resource profile is a group, not only a creation template: a resource
// created from it — or attached to it later — stays its member, and what is
// set on the profile is carried to every member. Without that a profile named
// what a new resource would look like and nothing more; changing one DRBD
// option on thirty database volumes meant thirty set-options runs, and the
// ones someone missed drifted silently.

// ProfileMemberResult is what one profile operation did to one member.
type ProfileMemberResult struct {
	Resource string
	OK       bool
	Message  string
}

// profileMembers lists the resources that belong to a profile, by name.
func (rm *ResourceManager) profileMembers(ctx context.Context, profile string) ([]*database.Resource, error) {
	all, err := rm.controller.db.ListResources(ctx)
	if err != nil {
		return nil, fmt.Errorf("list resources: %w", err)
	}
	var members []*database.Resource
	for _, r := range all {
		if r.Profile == profile {
			members = append(members, r)
		}
	}
	sort.Slice(members, func(i, j int) bool { return members[i].Name < members[j].Name })
	return members, nil
}

func (rm *ResourceManager) requireProfile(ctx context.Context, name string) (*database.ResourceProfile, error) {
	if rm.controller.db == nil {
		return nil, fmt.Errorf("database not available")
	}
	p, err := rm.controller.db.GetResourceProfile(ctx, name)
	if err != nil || p == nil {
		return nil, fmt.Errorf("resource profile %q not found", name)
	}
	return p, nil
}

// SetProfileOptions records DRBD options on a profile and applies them to
// every member. The profile is saved first, so a member that fails keeps its
// old value while every later AdjustProfile tries again.
func (rm *ResourceManager) SetProfileOptions(ctx context.Context, name string, options map[string]string) (*database.ResourceProfile, []ProfileMemberResult, error) {
	if len(options) == 0 {
		return nil, nil, fmt.Errorf("no options provided")
	}
	p, err := rm.requireProfile(ctx, name)
	if err != nil {
		return nil, nil, err
	}
	if p.DRBDOptions == nil {
		p.DRBDOptions = map[string]string{}
	}
	for k, v := range options {
		p.DRBDOptions[k] = v
	}
	if err := rm.controller.db.SaveResourceProfile(ctx, p); err != nil {
		return nil, nil, fmt.Errorf("save profile: %w", err)
	}
	members, err := rm.profileMembers(ctx, name)
	if err != nil {
		return p, nil, err
	}
	results := make([]ProfileMemberResult, 0, len(members))
	for _, m := range members {
		res := ProfileMemberResult{Resource: m.Name, OK: true, Message: "options applied"}
		if err := rm.SetOptions(ctx, m.Name, options); err != nil {
			res.OK, res.Message = false, err.Error()
		}
		results = append(results, res)
	}
	rm.controller.logger.Info("Profile options set",
		zap.String("profile", name), zap.Any("options", options), zap.Int("members", len(members)))
	return p, results, nil
}

// AssignProfile makes resource a member of profile, applying the profile's
// DRBD options to it, or — with an empty profile — takes it out of the one it
// is in. Replica count and placement are left to AdjustProfile, which says
// what it would change before it changes it.
func (rm *ResourceManager) AssignProfile(ctx context.Context, resource, profile string) error {
	if rm.controller.db == nil {
		return fmt.Errorf("database not available")
	}
	r, err := rm.controller.db.GetResource(ctx, resource)
	if err != nil || r == nil {
		return fmt.Errorf("resource %q not found", resource)
	}
	if profile != "" {
		p, err := rm.requireProfile(ctx, profile)
		if err != nil {
			return err
		}
		if len(p.DRBDOptions) > 0 {
			if err := rm.SetOptions(ctx, resource, p.DRBDOptions); err != nil {
				return fmt.Errorf("apply the options of profile %q: %w", profile, err)
			}
		}
	}
	r.Profile = profile
	return rm.controller.db.SaveResource(ctx, r)
}

// AdjustProfile brings every member into line with its profile: the profile's
// DRBD options are applied, and a member with fewer diskful replicas than the
// profile asks for gets new ones, placed by the profile's pool and label
// constraints. A member with more replicas is reported and left alone —
// removing a copy of someone's data is not something to do as a side effect.
// With dryRun nothing is changed and each result says what would be.
func (rm *ResourceManager) AdjustProfile(ctx context.Context, name string, dryRun bool) ([]ProfileMemberResult, error) {
	p, err := rm.requireProfile(ctx, name)
	if err != nil {
		return nil, err
	}
	members, err := rm.profileMembers(ctx, name)
	if err != nil {
		return nil, err
	}
	results := make([]ProfileMemberResult, 0, len(members))
	for _, m := range members {
		results = append(results, rm.adjustMember(ctx, p, m, dryRun))
	}
	return results, nil
}

func (rm *ResourceManager) adjustMember(ctx context.Context, p *database.ResourceProfile, m *database.Resource, dryRun bool) ProfileMemberResult {
	res := ProfileMemberResult{Resource: m.Name, OK: true}
	var did []string

	if len(p.DRBDOptions) > 0 {
		if dryRun {
			did = append(did, "would apply the profile's DRBD options")
		} else if err := rm.SetOptions(ctx, m.Name, p.DRBDOptions); err != nil {
			res.OK, res.Message = false, "apply options: "+err.Error()
			return res
		} else {
			did = append(did, "DRBD options applied")
		}
	}

	nodes := splitCSV(m.Nodes)
	if m.WANMode && m.DRNode != "" {
		nodes = without(nodes, m.DRNode) // the DR copy is not a local replica
	}
	switch {
	case p.Replicas <= 0 || len(nodes) == p.Replicas:
		did = append(did, fmt.Sprintf("%d replica(s)", len(nodes)))
	case len(nodes) > p.Replicas:
		did = append(did, fmt.Sprintf("%d replicas, more than the profile's %d; none removed", len(nodes), p.Replicas))
	case m.WANMode:
		did = append(did, fmt.Sprintf("%d of %d replicas; WAN resources are not adjusted — add replicas with resource add-replica", len(nodes), p.Replicas))
	default:
		missing := p.Replicas - len(nodes)
		pool, sizeGB := rm.memberPoolAndSize(ctx, m.Name, p.Pool)
		picked, err := rm.selectAdditionalReplicas(ctx, pool, sizeGB, missing, nodes, nil, p.OnDifferent, p.OnSame)
		if err != nil {
			res.OK, res.Message = false, strings.Join(append(did, "no room for more replicas: "+err.Error()), "; ")
			return res
		}
		tiebreakers := splitCSV(m.DisklessNodes)
		for _, n := range picked {
			isTiebreaker := containsString(tiebreakers, n)
			if dryRun {
				if isTiebreaker {
					did = append(did, "would turn tiebreaker "+n+" into a replica")
				} else {
					did = append(did, "would add a replica on "+n)
				}
				continue
			}
			var err error
			if isTiebreaker {
				err = rm.tiebreakerToReplica(ctx, m.Name, n)
			} else {
				err = rm.AddReplica(ctx, m.Name, n)
			}
			if err != nil {
				res.OK = false
				did = append(did, fmt.Sprintf("add a replica on %s: %v", n, err))
				break
			}
			if isTiebreaker {
				did = append(did, "turned tiebreaker "+n+" into a replica")
			} else {
				did = append(did, "added a replica on "+n)
			}
		}
	}
	res.Message = strings.Join(did, "; ")
	return res
}

// tiebreakerToReplica gives a resource's diskless tiebreaker a replica of its
// own. On three nodes the only place for a third copy of a two-replica
// resource is its tiebreaker, and AddReplica refuses a tiebreaker: it would be
// a data copy and a quorum-only member at once. The tiebreaker is taken out
// first, then the node is added as a replica; with three copies the resource
// no longer needs one. If the replica cannot be added, the tiebreaker is put
// back, so a failed adjust does not leave two copies with no third vote.
func (rm *ResourceManager) tiebreakerToReplica(ctx context.Context, resource, node string) error {
	if err := rm.SetTiebreaker(ctx, resource, ""); err != nil {
		return fmt.Errorf("take tiebreaker %s out: %w", node, err)
	}
	if err := rm.AddReplica(ctx, resource, node); err != nil {
		if rerr := rm.SetTiebreaker(ctx, resource, node); rerr != nil {
			return fmt.Errorf("%w; putting the tiebreaker back also failed: %v", err, rerr)
		}
		return err
	}
	return nil
}

// memberPoolAndSize is the pool a member's new replicas go into — its own
// volumes', else the profile's — and the size one must hold.
func (rm *ResourceManager) memberPoolAndSize(ctx context.Context, resource, profilePool string) (string, uint64) {
	vols, err := rm.controller.db.ListVolumes(ctx, resource)
	var pool string
	var size uint64
	if err == nil {
		for _, v := range vols {
			if pool == "" {
				pool = v.Pool
			}
			size += uint64(v.SizeGB)
		}
	}
	if pool == "" {
		pool = profilePool
	}
	return pool, size
}

// ProfileMaxSize is the largest volume a new member of the profile could get
// now: the biggest size for which the profile's pool still has room on enough
// nodes to satisfy its replica count and label constraints. On a thin pool the
// figure is what fits without overcommitting; thin reports that.
func (rm *ResourceManager) ProfileMaxSize(ctx context.Context, name string) (sizeGB uint64, nodes []string, thin bool, err error) {
	p, err := rm.requireProfile(ctx, name)
	if err != nil {
		return 0, nil, false, err
	}
	if p.Pool == "" {
		return 0, nil, false, fmt.Errorf("profile %q names no pool", name)
	}
	replicas := p.Replicas
	if replicas <= 0 {
		replicas = 2
	}
	cands, err := rm.placementCandidates(ctx, normalizeManagedName(p.Pool), 0, nil)
	if err != nil {
		return 0, nil, false, err
	}
	size, picked := maxPlaceableSize(cands, replicas, placementConstraints{onDifferent: p.OnDifferent, onSame: p.OnSame})
	if picked == nil {
		return 0, nil, false, fmt.Errorf("pool %q cannot place %d replica(s) under the profile's constraints", p.Pool, replicas)
	}
	for _, c := range cands {
		for _, n := range picked {
			if c.node == n && c.thin {
				thin = true
			}
		}
	}
	return size, picked, thin, nil
}

// maxPlaceableSize tries every candidate's free space as the size, largest
// first, and returns the first that still leaves enough nodes to place on, in
// whole GiB rounded down: a size that does not fit is worse than no answer.
func maxPlaceableSize(cands []placementNode, replicas int, c placementConstraints) (uint64, []string) {
	sizes := make([]uint64, 0, len(cands))
	for _, n := range cands {
		sizes = append(sizes, n.freeBytes)
	}
	sort.Slice(sizes, func(i, j int) bool { return sizes[i] > sizes[j] })
	for _, s := range sizes {
		var fit []placementNode
		for _, n := range cands {
			if n.freeBytes >= s {
				fit = append(fit, n)
			}
		}
		if picked, err := selectConstrained(fit, replicas, c); err == nil {
			return s / (1 << 30), picked
		}
	}
	return 0, nil
}
