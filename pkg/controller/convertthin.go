package controller

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"go.uber.org/zap"

	"github.com/liliang-cn/sds/pkg/deployment"
)

// Converting a thick LVM pool to a thin one, in place, one node at a time.
//
// A thick pool cannot really do snapshots: LVM makes the caller reserve a COW
// area per snapshot (SDS reserves 20% of the origin), so a 6 GiB volume costs
// 1.2 GiB per snapshot whether anything changes or not. On a 10 GiB pool that
// is two snapshots, which is not a retention policy. A thin snapshot costs only
// the blocks that diverge — measured on 2026-08-02, three snapshots of a 6 GiB
// origin moved the pool's usage by exactly nothing.
//
// The conversion destroys this node's backing volumes and rebuilds them from
// the peers, so the interesting part is not the LVM commands: it is refusing to
// start when the cluster cannot afford to lose this copy.

const gib = uint64(1) << 30

// thinConversionInput is everything the planner needs, gathered by the caller.
// Keeping it a plain struct is what makes the refusals testable without a
// cluster.
type thinConversionInput struct {
	Node string
	Pool string
	// VGFreeBytes is unallocated space in the volume group right now, before
	// the thick volumes are removed.
	VGFreeBytes uint64
	// Resources is every resource known to the controller; the planner selects
	// the ones with a volume in this pool.
	Resources []*ResourceInfo
	// BackingBytes maps a backing LV name to its exact size. DRBD records the
	// device size in its metadata, so a volume rebuilt from a rounded "6G"
	// is a different device and will not attach.
	BackingBytes map[string]uint64
	// AlreadyThin marks volumes that need no work.
	AlreadyThin map[string]bool
}

type thinVolumePlan struct {
	Resource  string
	LV        string
	SizeBytes uint64
}

type thinConversionPlan struct {
	Node          string
	Pool          string
	ThinPoolName  string
	PoolBytes     uint64
	MetadataBytes uint64
	Volumes       []thinVolumePlan
}

// thinPoolName is the LV the converted volumes live inside.
const thinPoolName = "sdsthin"

// planThinConversion decides whether this node's pool can be rebuilt as thin
// right now, and how big to make the pool.
func planThinConversion(in *thinConversionInput) (*thinConversionPlan, error) {
	if in.Node == "" || in.Pool == "" {
		return nil, fmt.Errorf("node and pool are both required")
	}

	var vols []thinVolumePlan
	var originTotal uint64

	for _, res := range in.Resources {
		for _, v := range res.Volumes {
			if v.Pool != in.Pool {
				continue // a different pool on the same node is not our business
			}
			if !hasNode(res, in.Node) {
				continue
			}
			if in.AlreadyThin[v.BackingVolume] {
				return nil, fmt.Errorf("%s/%s is already thin; nothing to convert",
					in.Pool, v.BackingVolume)
			}
			if err := checkSafeToRebuild(res, in.Node); err != nil {
				return nil, err
			}
			size, ok := in.BackingBytes[v.BackingVolume]
			if !ok || size == 0 {
				return nil, fmt.Errorf("could not read the exact size of %s/%s; refusing to guess",
					in.Pool, v.BackingVolume)
			}
			vols = append(vols, thinVolumePlan{Resource: res.Name, LV: v.BackingVolume, SizeBytes: size})
			originTotal += size
		}
	}
	if len(vols) == 0 {
		return nil, fmt.Errorf("no thick volumes of a known resource found in %s on %s", in.Pool, in.Node)
	}
	sort.Slice(vols, func(i, j int) bool { return vols[i].LV < vols[j].LV })

	// Removing the thick volumes returns their extents, so the pool can be as
	// large as those plus whatever was already free — minus a margin for thin
	// metadata and LVM's own rounding.
	usable := originTotal + in.VGFreeBytes
	metadata := thinMetadataBytes(usable)
	if usable <= originTotal+metadata {
		return nil, fmt.Errorf(
			"converting %s on %s would leave no headroom for snapshots (%d MiB free beyond the volumes); "+
				"grow the volume group first", in.Pool, in.Node, (usable-originTotal)/(1<<20))
	}
	poolBytes := usable - metadata

	return &thinConversionPlan{
		Node:          in.Node,
		Pool:          in.Pool,
		ThinPoolName:  thinPoolName,
		PoolBytes:     poolBytes,
		MetadataBytes: metadata,
		Volumes:       vols,
	}, nil
}

// checkSafeToRebuild refuses the cases where losing this node's copy — for the
// length of a full resync — is not something the resource can absorb.
func checkSafeToRebuild(res *ResourceInfo, node string) error {
	st, ok := res.NodeStates[node]
	if !ok {
		return fmt.Errorf("no live DRBD state for %s on %s; refusing to convert blind", res.Name, node)
	}
	if strings.EqualFold(st.Role, "Primary") {
		return fmt.Errorf("%s is Primary on %s; move it first (sds ha evict, or resource secondary)",
			res.Name, node)
	}

	// Count what would still hold data, and make sure none of it is busy.
	diskful := 0
	for peer, ps := range res.NodeStates {
		if peer == node {
			continue
		}
		if isResyncing(ps.Replication) {
			return fmt.Errorf("%s is mid-resync on %s (%s, %.0f%%); wait for it to finish",
				res.Name, peer, ps.Replication, ps.SyncPercent)
		}
		if ps.DiskState == "Diskless" {
			continue // a tiebreaker votes but holds nothing
		}
		if ps.DiskState != "UpToDate" {
			return fmt.Errorf("%s is %s on %s; every other replica must be UpToDate first",
				res.Name, ps.DiskState, peer)
		}
		diskful++
	}
	if diskful < 2 {
		return fmt.Errorf(
			"converting %s on %s would leave only one diskful copy while it resyncs; "+
				"add a replica first", res.Name, node)
	}
	return nil
}

func isResyncing(replication string) bool {
	switch replication {
	case "", "Established", "Off", "WFReportParams":
		return false
	}
	return strings.Contains(replication, "Sync") || strings.Contains(replication, "Ahead") ||
		strings.Contains(replication, "Behind")
}

func hasNode(res *ResourceInfo, node string) bool {
	for _, n := range res.Nodes {
		if strings.TrimSpace(n) == node {
			return true
		}
	}
	return false
}

// ConvertPoolToThin rebuilds one node's pool as an LVM thin pool.
//
// Only one node at a time, on purpose: the node's copy is destroyed and
// resynced in full from its peers, and a full 6 GiB resync is minutes on a LAN
// and around half an hour to an off-site replica. Doing two at once would
// stack those windows.
func (rm *ResourceManager) ConvertPoolToThin(ctx context.Context, nodeName, poolName string) error {
	if rm.deployment == nil {
		return fmt.Errorf("deployment client not set")
	}
	host := rm.controller.nodes.GetNodeAddressByName(nodeName)
	if host == "" {
		return fmt.Errorf("node %q is not registered", nodeName)
	}

	resources, err := rm.liveResourcesOn(ctx, nodeName)
	if err != nil {
		return err
	}

	in := &thinConversionInput{
		Node:         nodeName,
		Pool:         poolName,
		Resources:    resources,
		BackingBytes: map[string]uint64{},
		AlreadyThin:  map[string]bool{},
	}
	if in.VGFreeBytes, err = rm.deployment.VGFreeBytes(ctx, host, poolName); err != nil {
		return fmt.Errorf("inspect %s on %s: %w", poolName, nodeName, err)
	}
	for _, res := range resources {
		for _, v := range res.Volumes {
			if v.Pool != poolName || !hasNode(res, nodeName) {
				continue
			}
			thin, terr := rm.deployment.LVIsThin(ctx, host, poolName, v.BackingVolume)
			if terr != nil {
				return fmt.Errorf("check whether %s/%s is thin: %w", poolName, v.BackingVolume, terr)
			}
			in.AlreadyThin[v.BackingVolume] = thin
			size, serr := rm.deployment.LVSizeBytes(ctx, host, poolName, v.BackingVolume)
			if serr != nil {
				return fmt.Errorf("read size of %s/%s: %w", poolName, v.BackingVolume, serr)
			}
			in.BackingBytes[v.BackingVolume] = size
		}
	}

	plan, err := planThinConversion(in)
	if err != nil {
		return err
	}

	log := rm.controller.logger
	log.Info("Converting pool to thin",
		zap.String("node", nodeName), zap.String("pool", poolName),
		zap.Uint64("pool_bytes", plan.PoolBytes), zap.Int("volumes", len(plan.Volumes)))

	// Detach and remove every volume first, then build the pool from all the
	// freed extents. Interleaving would size the pool against space the other
	// volumes still hold.
	for _, v := range plan.Volumes {
		if _, derr := rm.deployment.DRBDDetach(ctx, host, v.Resource); derr != nil {
			return fmt.Errorf("detach %s on %s: %w", v.Resource, nodeName, derr)
		}
		if _, rerr := rm.deployment.LVRemove(ctx, []string{host},
			fmt.Sprintf("%s/%s", poolName, v.LV)); rerr != nil {
			return fmt.Errorf("remove %s/%s (the node is now diskless; rerun to finish): %w",
				poolName, v.LV, rerr)
		}
	}

	if _, err := rm.deployment.LVCreateThinPoolSized(ctx, []string{host},
		poolName, plan.ThinPoolName, plan.PoolBytes, plan.MetadataBytes); err != nil {
		return fmt.Errorf("create thin pool %s/%s (the node is diskless; rerun to finish): %w",
			poolName, plan.ThinPoolName, err)
	}

	for _, v := range plan.Volumes {
		if _, err := rm.deployment.LVCreateThinVolume(ctx, []string{host},
			poolName, plan.ThinPoolName, v.LV, fmt.Sprintf("%dB", v.SizeBytes)); err != nil {
			return fmt.Errorf("create thin volume %s/%s: %w", poolName, v.LV, err)
		}
		if _, err := rm.deployment.DRBDCreateMD(ctx, []string{host}, v.Resource,
			deployment.DefaultMaxPeers); err != nil {
			return fmt.Errorf("create metadata for %s: %w", v.Resource, err)
		}
		if _, err := rm.deployment.DRBDAttach(ctx, host, v.Resource); err != nil {
			return fmt.Errorf("attach %s: %w", v.Resource, err)
		}
		log.Info("Volume rebuilt as thin; full resync started",
			zap.String("node", nodeName), zap.String("volume", v.LV),
			zap.String("resource", v.Resource))
	}
	return nil
}

// liveResourcesOn returns live state for every resource with a replica on the
// node. Planning from the database alone would miss a resync in flight, which
// is exactly the state that must block a conversion.
func (rm *ResourceManager) liveResourcesOn(ctx context.Context, nodeName string) ([]*ResourceInfo, error) {
	if rm.controller.db == nil {
		return nil, fmt.Errorf("database not available")
	}
	dbResources, err := rm.controller.db.ListResources(ctx)
	if err != nil {
		return nil, fmt.Errorf("list resources: %w", err)
	}
	var out []*ResourceInfo
	for _, dbRes := range dbResources {
		info, gerr := rm.GetResource(ctx, dbRes.Name)
		if gerr != nil {
			// A resource whose state cannot be read might be the one that makes
			// this unsafe, so refuse rather than plan around it.
			return nil, fmt.Errorf("read live state of %s: %w", dbRes.Name, gerr)
		}
		if hasNode(info, nodeName) {
			out = append(out, info)
		}
	}
	return out, nil
}

// thinMetadataBytes sizes the pool's metadata area.
//
// LVM's default is small enough that a pool carrying many snapshots exhausts
// metadata long before it exhausts data — and a full metadata area takes the
// pool read-only, which is a far worse failure than simply running out of
// space. Measured on a freshly converted 8 GiB pool with a single volume:
// 30% of the default area was already gone. Reserve ~1% of the pool, floored
// at 128 MiB, which LVM accepts and which leaves room for hundreds of
// snapshots.
func thinMetadataBytes(poolBytes uint64) uint64 {
	const floor = 128 << 20
	m := poolBytes / 100
	if m < floor {
		return floor
	}
	// LVM caps thin metadata at 16 GiB.
	if m > 16*gib {
		return 16 * gib
	}
	return m
}
