package csi

import (
	"context"
	"fmt"

	sdspb "github.com/liliang-cn/sds/api/proto/v1"
)

// fakeBackend is an in-memory SDSBackend for unit tests.
type fakeBackend struct {
	resources map[string]*sdspb.ResourceInfo
	nodes     []*sdspb.NodeInfo
	primary   map[string]string // resource -> node
	createErr error

	createCalls []createCall
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
	f := &fakeBackend{resources: map[string]*sdspb.ResourceInfo{}, primary: map[string]string{}}
	for i, n := range nodeNames {
		f.nodes = append(f.nodes, &sdspb.NodeInfo{Name: n, Address: fmt.Sprintf("10.0.0.%d", i+1), State: "online"})
	}
	return f
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

func (f *fakeBackend) ListNodes(context.Context) ([]*sdspb.NodeInfo, error) { return f.nodes, nil }

func (f *fakeBackend) RegisterNode(_ context.Context, name, address string) (*sdspb.NodeInfo, error) {
	n := &sdspb.NodeInfo{Name: name, Address: address, State: "online"}
	f.nodes = append(f.nodes, n)
	return n, nil
}

func (f *fakeBackend) SetPrimary(_ context.Context, resource, node string, _ bool) error {
	f.primary[resource] = node
	return nil
}

func (f *fakeBackend) SetSecondary(_ context.Context, resource, _ string) error {
	delete(f.primary, resource)
	return nil
}

var _ SDSBackend = (*fakeBackend)(nil)
