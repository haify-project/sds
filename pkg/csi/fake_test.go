package csi

import (
	"context"
	"fmt"

	sdspb "github.com/liliang-cn/sds/api/proto/v1"
)

// fakeBackend is an in-memory SDSBackend for unit tests.
type fakeBackend struct {
	resources    map[string]*sdspb.ResourceInfo
	nodes        []*sdspb.NodeInfo
	pools        []*sdspb.PoolInfo // one entry per node hosting a pool
	primary      map[string]string // resource -> node
	createErr    error
	profiles     map[string]*sdspb.ResourceProfile
	listNodesErr error
	listPoolsErr error
	profileErr   error
	requestCalls []*sdspb.CreateResourceRequest

	// promoteErr, when set, makes PromoteForNode fail (e.g. simulating a
	// controller that refused to force-promote because the node lacks quorum).
	promoteErr    error
	promoteCalls  []string // resources passed to PromoteForNode
	setPrimaryErr error

	attachErr   error
	attachCalls []string // "resource/node" passed to AttachDisklessClient
	detachCalls []string // "resource/node" passed to DetachDisklessClient

	// snapshots maps "<volumePath>|<node>" to the snapshot names taken there,
	// mirroring the backend's per-node, per-backing-volume snapshot namespace.
	snapshots     map[string][]*sdspb.SnapshotInfo
	snapCreateErr error
	snapDeleteErr error
	snapListErr   error
	snapCreated   []string // "<volume>/<name>@<node>" passed to CreateSnapshot
	snapDeleted   []string // "<volume>/<name>@<node>" passed to DeleteSnapshot
	populateErr   error
	populated     []string // "<res>/<vol><-<device>@<node>" passed to PopulateVolume

	createCalls []createCall
}

// snapKey namespaces snapshots the way the backend does: per backing volume,
// per node.
func snapKey(volume, node string) string { return volume + "|" + node }

func (f *fakeBackend) CreateSnapshot(_ context.Context, volume, snapshotName, node string) error {
	if f.snapCreateErr != nil {
		return f.snapCreateErr
	}
	f.snapCreated = append(f.snapCreated, volume+"/"+snapshotName+"@"+node)
	if f.snapshots == nil {
		f.snapshots = map[string][]*sdspb.SnapshotInfo{}
	}
	k := snapKey(volume, node)
	f.snapshots[k] = append(f.snapshots[k], &sdspb.SnapshotInfo{Name: snapshotName, Volume: volume})
	return nil
}

func (f *fakeBackend) DeleteSnapshot(_ context.Context, volume, snapshotName, node string) error {
	if f.snapDeleteErr != nil {
		return f.snapDeleteErr
	}
	f.snapDeleted = append(f.snapDeleted, volume+"/"+snapshotName+"@"+node)
	k := snapKey(volume, node)
	var kept []*sdspb.SnapshotInfo
	for _, s := range f.snapshots[k] {
		if s.GetName() != snapshotName {
			kept = append(kept, s)
		}
	}
	f.snapshots[k] = kept
	return nil
}

func (f *fakeBackend) PopulateVolume(_ context.Context, resource string, volumeID uint32, sourceDevice, node string) (uint64, error) {
	if f.populateErr != nil {
		return 0, f.populateErr
	}
	f.populated = append(f.populated, fmt.Sprintf("%s/%d<-%s@%s", resource, volumeID, sourceDevice, node))
	return 1 << 20, nil
}

func (f *fakeBackend) ListSnapshots(_ context.Context, volume, node string) ([]*sdspb.SnapshotInfo, error) {
	if f.snapListErr != nil {
		return nil, f.snapListErr
	}
	return f.snapshots[snapKey(volume, node)], nil
}

type createCall struct {
	name        string
	nodes       []string
	pool        string
	storageType string
	sizeGB      uint32
	port        uint32
}

func newFakeBackend(nodeNames ...string) *fakeBackend {
	f := &fakeBackend{resources: map[string]*sdspb.ResourceInfo{}, primary: map[string]string{}, profiles: map[string]*sdspb.ResourceProfile{}}
	for i, n := range nodeNames {
		addr := fmt.Sprintf("10.0.0.%d", i+1)
		f.nodes = append(f.nodes, &sdspb.NodeInfo{Name: n, Address: addr, State: "online"})
		// By default every node hosts the pool used in tests ("vg0" -> "sds_vg0"),
		// so pool-aware placement sees all nodes as candidates. Tests that need a
		// node without the pool trim f.pools directly.
		f.pools = append(f.pools, &sdspb.PoolInfo{Name: "sds_vg0", Node: addr})
	}
	return f
}

func (f *fakeBackend) CreateResourceRequest(ctx context.Context, req *sdspb.CreateResourceRequest) error {
	f.requestCalls = append(f.requestCalls, req)
	return f.CreateResourceWithPoolAndType(ctx, req.Name, req.Port, req.Nodes, req.Protocol, req.SizeGb, req.Pool, req.StorageType, req.DrbdOptions)
}

func (f *fakeBackend) GetResourceProfile(_ context.Context, name string) (*sdspb.ResourceProfile, error) {
	if f.profileErr != nil {
		return nil, f.profileErr
	}
	profile, ok := f.profiles[name]
	if !ok {
		return nil, fmt.Errorf("profile %q not found", name)
	}
	return profile, nil
}

// onlyPoolOnNodes restricts the fake's pool so it exists only on the named
// nodes, letting a test exercise pool-aware replica placement.
func (f *fakeBackend) onlyPoolOnNodes(names ...string) {
	keep := map[string]bool{}
	for _, n := range names {
		keep[n] = true
	}
	var addrs map[string]bool = map[string]bool{}
	for _, n := range f.nodes {
		if keep[n.GetName()] {
			addrs[n.GetAddress()] = true
		}
	}
	var pruned []*sdspb.PoolInfo
	for _, p := range f.pools {
		if addrs[p.GetNode()] {
			pruned = append(pruned, p)
		}
	}
	f.pools = pruned
}

func (f *fakeBackend) CreateResourceWithPoolAndType(_ context.Context, name string, port uint32, nodes []string, _ string, sizeGB uint32, pool, storageType string, _ map[string]string) error {
	if f.createErr != nil {
		return f.createErr
	}
	f.createCalls = append(f.createCalls, createCall{name, nodes, pool, storageType, sizeGB, port})
	vols := []*sdspb.VolumeInfo{{VolumeId: 0, Device: "/dev/drbd100", SizeGb: uint64(sizeGB), Pool: pool}}
	f.resources[name] = &sdspb.ResourceInfo{Name: name, Nodes: nodes, Volumes: vols}
	return nil
}

func (f *fakeBackend) GetResource(_ context.Context, name string) (*sdspb.ResourceInfo, error) {
	r, ok := f.resources[name]
	if !ok {
		return nil, fmt.Errorf("resource %q not found", name)
	}
	return r, nil
}

func (f *fakeBackend) DeleteResource(_ context.Context, name string) error {
	delete(f.resources, name)
	return nil
}

func (f *fakeBackend) ListNodes(context.Context) ([]*sdspb.NodeInfo, error) {
	if f.listNodesErr != nil {
		return nil, f.listNodesErr
	}
	return f.nodes, nil
}

func (f *fakeBackend) ListPools(context.Context) ([]*sdspb.PoolInfo, error) {
	if f.listPoolsErr != nil {
		return nil, f.listPoolsErr
	}
	return f.pools, nil
}

func (f *fakeBackend) RegisterNode(_ context.Context, name, address string) (*sdspb.NodeInfo, error) {
	n := &sdspb.NodeInfo{Name: name, Address: address, State: "online"}
	f.nodes = append(f.nodes, n)
	return n, nil
}

func (f *fakeBackend) SetPrimary(_ context.Context, resource, node string, _ bool) error {
	if f.setPrimaryErr != nil {
		return f.setPrimaryErr
	}
	f.primary[resource] = node
	return nil
}

func (f *fakeBackend) PromoteForNode(_ context.Context, resource, node string) error {
	f.promoteCalls = append(f.promoteCalls, resource)
	if f.promoteErr != nil {
		return f.promoteErr
	}
	f.primary[resource] = node
	return nil
}

func (f *fakeBackend) SetSecondary(_ context.Context, resource, _ string) error {
	delete(f.primary, resource)
	return nil
}

func (f *fakeBackend) AttachDisklessClient(_ context.Context, resource, node string) error {
	if f.attachErr != nil {
		return f.attachErr
	}
	f.attachCalls = append(f.attachCalls, resource+"/"+node)
	// A diskless client deliberately does NOT join the diskful Nodes set, mirroring
	// the real controller (GetResource.Nodes is diskful-only), so a later replica
	// check still treats this node as remote.
	return nil
}

func (f *fakeBackend) DetachDisklessClient(_ context.Context, resource, node string) error {
	f.detachCalls = append(f.detachCalls, resource+"/"+node)
	return nil
}

func (f *fakeBackend) ResizeVolume(_ context.Context, resource string, volumeID uint32, sizeGB uint32) error {
	return nil
}

var _ SDSBackend = (*fakeBackend)(nil)
