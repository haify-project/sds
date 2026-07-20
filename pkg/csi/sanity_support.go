package csi

import (
	"context"
	"fmt"
	"os"
	"sync"

	sdspb "github.com/liliang-cn/sds/api/proto/v1"
)

// sanityBackend is a concurrency-safe in-memory SDSBackend for the CSI sanity
// suite. It is exported via NewSanityFakeBackend.
type sanityBackend struct {
	mu        sync.Mutex
	resources map[string]*sdspb.ResourceInfo
	nodes     []*sdspb.NodeInfo
	pools     []*sdspb.PoolInfo
}

// NewSanityFakeBackend builds a fake backend seeded with the given node names.
// Every node hosts the "vg0" pool the sanity suite provisions against.
func NewSanityFakeBackend(nodeNames ...string) SDSBackend {
	b := &sanityBackend{resources: map[string]*sdspb.ResourceInfo{}}
	for i, n := range nodeNames {
		addr := fmt.Sprintf("10.0.0.%d", i+1)
		b.nodes = append(b.nodes, &sdspb.NodeInfo{
			Name:    n,
			Address: addr,
			State:   "online",
		})
		b.pools = append(b.pools, &sdspb.PoolInfo{Name: "sds_vg0", Node: addr})
	}
	return b
}

func (b *sanityBackend) CreateResourceWithPoolAndType(_ context.Context, name string, port uint32, nodes []string, _ string, sizeGB uint32, pool, _ string, _ map[string]string) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.resources[name] = &sdspb.ResourceInfo{
		Name:  name,
		Nodes: nodes,
		Port:  port,
		Volumes: []*sdspb.VolumeInfo{{
			VolumeId: 0,
			Device:   "/dev/drbd100",
			SizeGb:   uint64(sizeGB),
			Pool:     pool,
		}},
	}
	return nil
}

func (b *sanityBackend) GetResource(_ context.Context, name string) (*sdspb.ResourceInfo, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if r, ok := b.resources[name]; ok {
		return r, nil
	}
	return nil, fmt.Errorf("resource %q not found", name)
}

func (b *sanityBackend) DeleteResource(_ context.Context, name string) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	delete(b.resources, name)
	return nil
}

func (b *sanityBackend) ListNodes(_ context.Context) ([]*sdspb.NodeInfo, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	out := make([]*sdspb.NodeInfo, len(b.nodes))
	copy(out, b.nodes)
	return out, nil
}

func (b *sanityBackend) ListPools(_ context.Context) ([]*sdspb.PoolInfo, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	out := make([]*sdspb.PoolInfo, len(b.pools))
	copy(out, b.pools)
	return out, nil
}

func (b *sanityBackend) RegisterNode(_ context.Context, name, address string) (*sdspb.NodeInfo, error) {
	return &sdspb.NodeInfo{Name: name, Address: address}, nil
}

func (b *sanityBackend) SetPrimary(_ context.Context, _, _ string, _ bool) error   { return nil }
func (b *sanityBackend) PromoteForNode(_ context.Context, _, _ string) error       { return nil }
func (b *sanityBackend) SetSecondary(_ context.Context, _, _ string) error         { return nil }
func (b *sanityBackend) AttachDisklessClient(_ context.Context, _, _ string) error { return nil }
func (b *sanityBackend) DetachDisklessClient(_ context.Context, _, _ string) error { return nil }
func (b *sanityBackend) ResizeVolume(_ context.Context, _ string, _ uint32, _ uint32) error { return nil }

var _ SDSBackend = (*sanityBackend)(nil)

// nopMounter is a no-op Mounter used in the sanity suite to satisfy the node
// server without performing real filesystem operations.
type nopMounter struct {
	mu     sync.Mutex
	mounts map[string]bool
}

// NewNopMounter returns a no-op Mounter for tests (no real filesystem ops).
func NewNopMounter() Mounter {
	return &nopMounter{mounts: map[string]bool{}}
}

func (m *nopMounter) FormatAndMount(_, target, _ string, _ []string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.mounts[target] = true
	return nil
}

func (m *nopMounter) Mount(_, target, _ string, _ []string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.mounts[target] = true
	return nil
}

func (m *nopMounter) Unmount(target string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.mounts, target)
	return nil
}

func (m *nopMounter) IsMountPoint(target string) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.mounts[target], nil
}

func (m *nopMounter) EnsureDir(target string) error {
	return os.MkdirAll(target, 0o750)
}

func (m *nopMounter) ResizeFS(_, _ string) error { return nil }

var _ Mounter = (*nopMounter)(nil)
