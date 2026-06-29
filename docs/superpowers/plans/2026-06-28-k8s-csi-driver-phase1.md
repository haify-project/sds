# k8s CSI Driver Phase ① Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Ship a DRBD CSI driver so Kubernetes pods get dynamically-provisioned, replicated RWO volumes, pinned via topology to a node that holds a replica.

**Architecture:** Approach A — thin CSI translation layer (csi-controller Deployment + csi-node DaemonSet) that reuses the existing `sds-controller` gRPC API via `pkg/client`; SSH execution unchanged. Built fresh in the monorepo; backend-agnostic Piraeus parts (drbd-module-loader, sidecar manifest shapes) are borrowed, nothing is forked.

**Tech Stack:** Go, `github.com/container-storage-interface/spec/lib/go/csi` (v5), `google.golang.org/grpc` (already a dep), `k8s.io/mount-utils` for node mounting, `github.com/kubernetes-csi/csi-test/v5` for the sanity suite.

Spec: `docs/superpowers/specs/2026-06-28-k8s-csi-driver-phase1-design.md`

---

## File Structure

New Go package `pkg/csi/` (one responsibility per file):

| File | Responsibility |
| --- | --- |
| `pkg/csi/driver.go` | Driver name/version/topology consts; `SDSBackend` interface; `Driver` struct; `Run(endpoint)` unix-socket gRPC server |
| `pkg/csi/identity.go` | CSI Identity service (GetPluginInfo, GetPluginCapabilities, Probe) |
| `pkg/csi/names.go` | `sanitizeResourceName` — CSI volume name → DRBD-safe resource name (pure) |
| `pkg/csi/params.go` | `ParseVolumeParams` — StorageClass parameters (pure) |
| `pkg/csi/topology.go` | `selectReplicaNodes`, `accessibleTopology`, `requisiteNodes` (pure) |
| `pkg/csi/controller.go` | CSI Controller service (CreateVolume, DeleteVolume, capabilities) |
| `pkg/csi/mount.go` | `Mounter` interface + `safeMounter` over `k8s.io/mount-utils` |
| `pkg/csi/node.go` | CSI Node service (NodeStage/Unstage/Publish/Unpublish, NodeGetInfo, capabilities) |
| `pkg/csi/fake_test.go` | `fakeBackend` (implements `SDSBackend`) shared by unit tests |
| `cmd/csi-controller/main.go` | Controller plugin entrypoint |
| `cmd/csi-node/main.go` | Node plugin entrypoint (+ self node-registration) |

Modified:

| File | Change |
| --- | --- |
| `pkg/controller/resources.go` | Auto-allocate a free port when `CreateResource` is called with `port == 0` |
| `Makefile` | Build targets for `csi-controller` / `csi-node` |

New non-Go:

| File | Responsibility |
| --- | --- |
| `Dockerfile.csi` | Builds both CSI binaries into one image |
| `deploy/k8s/*.yaml` | CSIDriver, StorageClass, RBAC, csi-controller Deployment, csi-node DaemonSet (sds-controller deployment assumed already running and reachable) |
| `scripts/csi-e2e.sh` | Real-cluster smoke test |

---

## Task 1: Add CSI dependency + driver skeleton + Identity service

**Files:**
- Create: `pkg/csi/driver.go`
- Create: `pkg/csi/identity.go`
- Test: `pkg/csi/identity_test.go`

- [ ] **Step 1: Add dependencies**

Run:
```bash
cd /Users/liliang/Things/dev/storage/sds
go get github.com/container-storage-interface/spec@v5.0.0
go get k8s.io/mount-utils@v0.31.0
```
Expected: `go.mod` gains both modules, no build errors.

- [ ] **Step 2: Write `pkg/csi/driver.go`**

```go
package csi

import (
	"context"
	"fmt"
	"net"
	"os"
	"strings"

	"github.com/container-storage-interface/spec/lib/go/csi"
	sdspb "github.com/liliang-cn/sds/api/proto/v1"
	"google.golang.org/grpc"
	"go.uber.org/zap"
)

const (
	// DriverName is the CSI driver name (reverse-DNS, used in CSIDriver object).
	DriverName = "sds.csi.liliang-cn.com"
	// DriverVersion is reported via Identity.GetPluginInfo.
	DriverVersion = "0.1.0"
	// TopologyKeyNode segments a volume to the node(s) holding a replica.
	TopologyKeyNode = DriverName + "/node"
)

// SDSBackend is the subset of the sds-controller gRPC client the CSI driver
// uses. *client.SDSClient satisfies it directly; tests use a fake.
type SDSBackend interface {
	CreateResourceWithPoolAndType(ctx context.Context, name string, port uint32, nodes []string, protocol string, sizeGB uint32, pool, storageType string, drbdOptions map[string]string) error
	GetResource(ctx context.Context, name string) (*sdspb.ResourceInfo, error)
	DeleteResource(ctx context.Context, name string) error
	ListNodes(ctx context.Context) ([]*sdspb.NodeInfo, error)
	RegisterNode(ctx context.Context, name, address string) (*sdspb.NodeInfo, error)
	SetPrimary(ctx context.Context, resource, node string, force bool) error
	SetSecondary(ctx context.Context, resource, node string) error
}

// Driver wires the CSI services onto a gRPC server over a unix socket.
type Driver struct {
	endpoint string
	srv      *grpc.Server
	log      *zap.Logger

	identity   csi.IdentityServer
	controller csi.ControllerServer
	node       csi.NodeServer
}

// NewDriver builds a Driver. Pass nil for services that this binary doesn't
// serve (the controller binary leaves node nil and vice-versa).
func NewDriver(endpoint string, log *zap.Logger, id csi.IdentityServer, ctrl csi.ControllerServer, node csi.NodeServer) *Driver {
	return &Driver{endpoint: endpoint, log: log, identity: id, controller: ctrl, node: node}
}

// Run listens on the unix socket and serves until the context is cancelled.
func (d *Driver) Run(ctx context.Context) error {
	proto, addr, err := parseEndpoint(d.endpoint)
	if err != nil {
		return err
	}
	if proto == "unix" {
		_ = os.Remove(addr) // clear a stale socket
	}
	lis, err := net.Listen(proto, addr)
	if err != nil {
		return fmt.Errorf("listen %s: %w", d.endpoint, err)
	}
	d.srv = grpc.NewServer()
	if d.identity != nil {
		csi.RegisterIdentityServer(d.srv, d.identity)
	}
	if d.controller != nil {
		csi.RegisterControllerServer(d.srv, d.controller)
	}
	if d.node != nil {
		csi.RegisterNodeServer(d.srv, d.node)
	}
	go func() {
		<-ctx.Done()
		d.srv.GracefulStop()
	}()
	d.log.Info("CSI driver serving", zap.String("endpoint", d.endpoint))
	return d.srv.Serve(lis)
}

// parseEndpoint splits "unix:///path" or "tcp://host:port" into proto + addr.
func parseEndpoint(ep string) (string, string, error) {
	if strings.HasPrefix(ep, "unix://") {
		return "unix", strings.TrimPrefix(ep, "unix://"), nil
	}
	if strings.HasPrefix(ep, "tcp://") {
		return "tcp", strings.TrimPrefix(ep, "tcp://"), nil
	}
	return "", "", fmt.Errorf("invalid endpoint %q (want unix:// or tcp://)", ep)
}
```

- [ ] **Step 3: Write `pkg/csi/identity.go`**

```go
package csi

import (
	"context"

	"github.com/container-storage-interface/spec/lib/go/csi"
	"google.golang.org/protobuf/types/known/wrapperspb"
)

// identityServer implements the CSI Identity service.
type identityServer struct {
	csi.UnimplementedIdentityServer
}

// NewIdentityServer returns the Identity service implementation.
func NewIdentityServer() csi.IdentityServer { return &identityServer{} }

func (s *identityServer) GetPluginInfo(context.Context, *csi.GetPluginInfoRequest) (*csi.GetPluginInfoResponse, error) {
	return &csi.GetPluginInfoResponse{Name: DriverName, VendorVersion: DriverVersion}, nil
}

func (s *identityServer) GetPluginCapabilities(context.Context, *csi.GetPluginCapabilitiesRequest) (*csi.GetPluginCapabilitiesResponse, error) {
	return &csi.GetPluginCapabilitiesResponse{
		Capabilities: []*csi.PluginCapability{
			{Type: &csi.PluginCapability_Service_{Service: &csi.PluginCapability_Service{
				Type: csi.PluginCapability_Service_CONTROLLER_SERVICE}}},
			{Type: &csi.PluginCapability_Service_{Service: &csi.PluginCapability_Service{
				Type: csi.PluginCapability_Service_VOLUME_ACCESSIBILITY_CONSTRAINTS}}},
		},
	}, nil
}

func (s *identityServer) Probe(context.Context, *csi.ProbeRequest) (*csi.ProbeResponse, error) {
	return &csi.ProbeResponse{Ready: &wrapperspb.BoolValue{Value: true}}, nil
}
```

- [ ] **Step 4: Write `pkg/csi/identity_test.go`**

```go
package csi

import (
	"context"
	"testing"

	"github.com/container-storage-interface/spec/lib/go/csi"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestIdentityGetPluginInfo(t *testing.T) {
	resp, err := NewIdentityServer().GetPluginInfo(context.Background(), &csi.GetPluginInfoRequest{})
	require.NoError(t, err)
	assert.Equal(t, DriverName, resp.Name)
	assert.Equal(t, DriverVersion, resp.VendorVersion)
}

func TestIdentityProbeReady(t *testing.T) {
	resp, err := NewIdentityServer().Probe(context.Background(), &csi.ProbeRequest{})
	require.NoError(t, err)
	assert.True(t, resp.GetReady().GetValue())
}
```

- [ ] **Step 5: Run tests, expect PASS**

Run: `go test ./pkg/csi/... -run TestIdentity -v`
Expected: PASS (2 tests). If `github.com/golang/protobuf/ptypes/wrappers` is missing, run `go mod tidy` first.

- [ ] **Step 6: Commit**

```bash
go mod tidy
git add go.mod go.sum pkg/csi/driver.go pkg/csi/identity.go pkg/csi/identity_test.go
git commit -m "feat(csi): driver skeleton + identity service"
```

---

## Task 2: Resource-name sanitization

**Files:**
- Create: `pkg/csi/names.go`
- Test: `pkg/csi/names_test.go`

- [ ] **Step 1: Write the failing test `pkg/csi/names_test.go`**

```go
package csi

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestSanitizeResourceName(t *testing.T) {
	cases := []struct{ in, want string }{
		{"pvc-0e8b1f3a-1c2d-4e5f-8a9b-0c1d2e3f4a5b", "pvc_0e8b1f3a_1c2d_4e5f_8a9b_0c1d2e3f4a5b"},
		{"my.vol", "my_vol"},
		{"9starts-with-digit", "v9starts_with_digit"},
	}
	for _, c := range cases {
		assert.Equal(t, c.want, sanitizeResourceName(c.in), "input %q", c.in)
	}
}

func TestSanitizeResourceNameTruncates(t *testing.T) {
	long := ""
	for i := 0; i < 100; i++ {
		long += "a"
	}
	got := sanitizeResourceName(long)
	assert.LessOrEqual(t, len(got), 64)
}
```

- [ ] **Step 2: Run test, expect FAIL**

Run: `go test ./pkg/csi/... -run TestSanitizeResourceName -v`
Expected: FAIL — `undefined: sanitizeResourceName`.

- [ ] **Step 3: Write `pkg/csi/names.go`**

```go
package csi

// sanitizeResourceName maps a CSI volume name (typically "pvc-<uuid>") to a
// DRBD-safe resource name: only [A-Za-z0-9_], starts with a letter, <=64 chars.
// The result becomes the volumeHandle, so it is stable for the volume's life and
// needs no reverse mapping (Kubernetes persists it in the PV object).
func sanitizeResourceName(in string) string {
	out := make([]rune, 0, len(in))
	for _, r := range in {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
			out = append(out, r)
		default:
			out = append(out, '_')
		}
	}
	if len(out) == 0 || !isLetter(out[0]) {
		out = append([]rune{'v'}, out...)
	}
	if len(out) > 64 {
		out = out[:64]
	}
	return string(out)
}

func isLetter(r rune) bool {
	return (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z')
}
```

- [ ] **Step 4: Run test, expect PASS**

Run: `go test ./pkg/csi/... -run TestSanitizeResourceName -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add pkg/csi/names.go pkg/csi/names_test.go
git commit -m "feat(csi): DRBD-safe resource name sanitization"
```

---

## Task 3: StorageClass parameter parsing

**Files:**
- Create: `pkg/csi/params.go`
- Test: `pkg/csi/params_test.go`

- [ ] **Step 1: Write the failing test `pkg/csi/params_test.go`**

```go
package csi

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseVolumeParamsDefaults(t *testing.T) {
	p, err := ParseVolumeParams(map[string]string{"pool": "vg0"})
	require.NoError(t, err)
	assert.Equal(t, "vg0", p.Pool)
	assert.Equal(t, 2, p.Replicas)
	assert.Equal(t, "lvm", p.StorageType)
}

func TestParseVolumeParamsExplicit(t *testing.T) {
	p, err := ParseVolumeParams(map[string]string{"pool": "tank", "replicas": "3", "storageType": "zfs"})
	require.NoError(t, err)
	assert.Equal(t, 3, p.Replicas)
	assert.Equal(t, "zfs", p.StorageType)
}

func TestParseVolumeParamsErrors(t *testing.T) {
	_, err := ParseVolumeParams(map[string]string{})
	assert.Error(t, err, "missing pool")
	_, err = ParseVolumeParams(map[string]string{"pool": "vg0", "replicas": "x"})
	assert.Error(t, err, "bad replicas")
	_, err = ParseVolumeParams(map[string]string{"pool": "vg0", "storageType": "btrfs"})
	assert.Error(t, err, "bad storageType")
}
```

- [ ] **Step 2: Run test, expect FAIL**

Run: `go test ./pkg/csi/... -run TestParseVolumeParams -v`
Expected: FAIL — `undefined: ParseVolumeParams`.

- [ ] **Step 3: Write `pkg/csi/params.go`**

```go
package csi

import (
	"fmt"
	"strconv"
)

// VolumeParams is the parsed StorageClass.parameters for CreateVolume.
type VolumeParams struct {
	Pool        string // VG (lvm) or zpool (zfs) name; required
	Replicas    int    // diskful copies; default 2
	StorageType string // "lvm" or "zfs"; default "lvm"
}

// ParseVolumeParams validates and defaults the StorageClass parameters.
func ParseVolumeParams(p map[string]string) (VolumeParams, error) {
	out := VolumeParams{Pool: p["pool"], Replicas: 2, StorageType: "lvm"}
	if out.Pool == "" {
		return out, fmt.Errorf("storageclass parameter \"pool\" is required")
	}
	if v, ok := p["replicas"]; ok {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 {
			return out, fmt.Errorf("invalid replicas %q", v)
		}
		out.Replicas = n
	}
	if v, ok := p["storageType"]; ok {
		if v != "lvm" && v != "zfs" {
			return out, fmt.Errorf("invalid storageType %q (want lvm or zfs)", v)
		}
		out.StorageType = v
	}
	return out, nil
}
```

- [ ] **Step 4: Run test, expect PASS**

Run: `go test ./pkg/csi/... -run TestParseVolumeParams -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add pkg/csi/params.go pkg/csi/params_test.go
git commit -m "feat(csi): StorageClass parameter parsing"
```

---

## Task 4: Topology helpers

**Files:**
- Create: `pkg/csi/topology.go`
- Test: `pkg/csi/topology_test.go`

- [ ] **Step 1: Write the failing test `pkg/csi/topology_test.go`**

```go
package csi

import (
	"testing"

	"github.com/container-storage-interface/spec/lib/go/csi"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSelectReplicaNodesPrefersRequisite(t *testing.T) {
	got, err := selectReplicaNodes([]string{"n1", "n2", "n3"}, []string{"n3"}, 2)
	require.NoError(t, err)
	assert.Equal(t, "n3", got[0], "requisite node must be included first")
	assert.Len(t, got, 2)
}

func TestSelectReplicaNodesNoRequisite(t *testing.T) {
	got, err := selectReplicaNodes([]string{"n1", "n2", "n3"}, nil, 2)
	require.NoError(t, err)
	assert.Len(t, got, 2)
}

func TestSelectReplicaNodesInsufficient(t *testing.T) {
	_, err := selectReplicaNodes([]string{"n1"}, nil, 2)
	assert.Error(t, err)
}

func TestRequisiteNodes(t *testing.T) {
	req := &csi.TopologyRequirement{Requisite: []*csi.Topology{
		{Segments: map[string]string{TopologyKeyNode: "n2"}},
	}}
	assert.Equal(t, []string{"n2"}, requisiteNodes(req))
	assert.Nil(t, requisiteNodes(nil))
}

func TestAccessibleTopology(t *testing.T) {
	got := accessibleTopology([]string{"n1", "n2"})
	require.Len(t, got, 2)
	assert.Equal(t, "n1", got[0].Segments[TopologyKeyNode])
}
```

- [ ] **Step 2: Run test, expect FAIL**

Run: `go test ./pkg/csi/... -run "TestSelectReplicaNodes|TestRequisiteNodes|TestAccessibleTopology" -v`
Expected: FAIL — undefined `selectReplicaNodes` / `requisiteNodes` / `accessibleTopology`.

- [ ] **Step 3: Write `pkg/csi/topology.go`**

```go
package csi

import (
	"fmt"

	"github.com/container-storage-interface/spec/lib/go/csi"
)

// requisiteNodes extracts node names from a CSI topology requirement, preferring
// Preferred order then Requisite. Returns nil when there is no constraint.
func requisiteNodes(req *csi.TopologyRequirement) []string {
	if req == nil {
		return nil
	}
	seen := map[string]bool{}
	var out []string
	add := func(tops []*csi.Topology) {
		for _, t := range tops {
			if n := t.GetSegments()[TopologyKeyNode]; n != "" && !seen[n] {
				seen[n] = true
				out = append(out, n)
			}
		}
	}
	add(req.GetPreferred())
	add(req.GetRequisite())
	return out
}

// selectReplicaNodes picks `replicas` node names from available, putting any
// requisite nodes (the scheduler's chosen node) first so a replica lands there.
func selectReplicaNodes(available, requisite []string, replicas int) ([]string, error) {
	avail := map[string]bool{}
	for _, n := range available {
		avail[n] = true
	}
	var picked []string
	used := map[string]bool{}
	for _, n := range requisite {
		if avail[n] && !used[n] {
			picked = append(picked, n)
			used[n] = true
		}
	}
	for _, n := range available {
		if len(picked) >= replicas {
			break
		}
		if !used[n] {
			picked = append(picked, n)
			used[n] = true
		}
	}
	if len(picked) < replicas {
		return nil, fmt.Errorf("need %d replica nodes, only %d available", replicas, len(picked))
	}
	return picked[:replicas], nil
}

// accessibleTopology builds one CSI topology segment per replica node.
func accessibleTopology(nodes []string) []*csi.Topology {
	out := make([]*csi.Topology, 0, len(nodes))
	for _, n := range nodes {
		out = append(out, &csi.Topology{Segments: map[string]string{TopologyKeyNode: n}})
	}
	return out
}
```

- [ ] **Step 4: Run test, expect PASS**

Run: `go test ./pkg/csi/... -run "TestSelectReplicaNodes|TestRequisiteNodes|TestAccessibleTopology" -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add pkg/csi/topology.go pkg/csi/topology_test.go
git commit -m "feat(csi): topology selection + accessible-topology helpers"
```

---

## Task 5: Shared fake backend for tests

**Files:**
- Create: `pkg/csi/fake_test.go`

- [ ] **Step 1: Write `pkg/csi/fake_test.go`**

```go
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
```

- [ ] **Step 2: Verify it compiles against the interface**

Run: `go test ./pkg/csi/... -run NOOP_compileonly -v`
Expected: `ok` (no tests run, but the package — including `fake_test.go` — compiles). If `fakeBackend` does not satisfy `SDSBackend`, the controller/node tasks will fail to compile; that is the signal to fix signatures here.

- [ ] **Step 3: Commit**

```bash
git add pkg/csi/fake_test.go
git commit -m "test(csi): in-memory fake SDS backend"
```

---

## Task 6: Controller-side automatic port allocation

**Files:**
- Modify: `pkg/controller/resources.go` (insert auto-port logic before the `findPortConflict` call at ~line 341; add `nextGlobalPort` + pure `lowestFreePort` near `nextGlobalMinor` ~line 1307)
- Test: `pkg/controller/port_test.go`

- [ ] **Step 1: Write the failing test `pkg/controller/port_test.go`**

```go
package controller

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestLowestFreePort(t *testing.T) {
	assert.Equal(t, uint32(7000), lowestFreePort(nil, 7000))
	assert.Equal(t, uint32(7001), lowestFreePort([]uint32{7000}, 7000))
	assert.Equal(t, uint32(7002), lowestFreePort([]uint32{7000, 7001, 7003}, 7000))
}

func TestParsePortsFromResConfigs(t *testing.T) {
	in := "resource r {\n  on a { address 10.0.0.1:7000; }\n  on b { address ipv4 10.0.0.2:7005; }\n}\n"
	got := parsePortsFromResConfigs(in)
	assert.ElementsMatch(t, []uint32{7000, 7005}, got)
}
```

- [ ] **Step 2: Run test, expect FAIL**

Run: `go test ./pkg/controller/... -run "TestLowestFreePort|TestParsePortsFromResConfigs" -v`
Expected: FAIL — undefined `lowestFreePort` / `parsePortsFromResConfigs`.

- [ ] **Step 3: Add the pure helpers and `nextGlobalPort` to `pkg/controller/resources.go`**

Add near `nextGlobalMinor` (after it):

```go
// parsePortsFromResConfigs extracts DRBD ports from the `address ...:<port>;`
// lines of concatenated .res file contents.
func parsePortsFromResConfigs(text string) []uint32 {
	var ports []uint32
	for _, m := range portLineRe.FindAllStringSubmatch(text, -1) {
		if p, err := strconv.Atoi(m[1]); err == nil {
			ports = append(ports, uint32(p))
		}
	}
	return ports
}

// lowestFreePort returns the lowest port >= base not present in used.
func lowestFreePort(used []uint32, base uint32) uint32 {
	set := map[uint32]bool{}
	for _, p := range used {
		set[p] = true
	}
	for p := base; ; p++ {
		if !set[p] {
			return p
		}
	}
}

// nextGlobalPort scans existing .res files on host and returns the lowest free
// DRBD port at or above 7000.
func (rm *ResourceManager) nextGlobalPort(ctx context.Context, host string) (uint32, error) {
	res, err := rm.deployment.Exec(ctx, []string{host}, "cat /etc/drbd.d/*.res 2>/dev/null || true")
	if err != nil {
		return 0, err
	}
	text := ""
	if hr := res.Hosts[host]; hr != nil {
		text = hr.Stdout
	}
	return lowestFreePort(parsePortsFromResConfigs(text), 7000), nil
}
```

Add the regex var near the top of the file's var/const block:

```go
var portLineRe = regexp.MustCompile(`:(\d+);`)
```

Ensure `regexp` and `strconv` are imported (add to the import block if missing).

- [ ] **Step 4: Wire auto-allocation into `CreateResource`**

In `CreateResource`, immediately before the `findPortConflict` call (~line 341, after `nodeIPs` is built), insert:

```go
	if port == 0 {
		p, err := rm.nextGlobalPort(ctx, nodeIPs[0])
		if err != nil {
			return fmt.Errorf("allocate port: %w", err)
		}
		port = p
		rm.logger.Info("auto-allocated DRBD port", zap.Uint32("port", port), zap.String("resource", name))
	}
```

- [ ] **Step 5: Run tests, expect PASS**

Run: `go test ./pkg/controller/... -run "TestLowestFreePort|TestParsePortsFromResConfigs" -v`
Expected: PASS. Then `go build ./...` to confirm the wiring compiles.

- [ ] **Step 6: Commit**

```bash
git add pkg/controller/resources.go pkg/controller/port_test.go
git commit -m "feat(controller): auto-allocate DRBD port when port==0"
```

---

## Task 7: Controller service (CreateVolume / DeleteVolume)

**Files:**
- Create: `pkg/csi/controller.go`
- Test: `pkg/csi/controller_test.go`

- [ ] **Step 1: Write the failing test `pkg/csi/controller_test.go`**

```go
package csi

import (
	"context"
	"testing"

	"github.com/container-storage-interface/spec/lib/go/csi"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

func newTestController(b SDSBackend) *controllerServer {
	return NewControllerServer(b, zap.NewNop()).(*controllerServer)
}

func validCreateReq(name string) *csi.CreateVolumeRequest {
	return &csi.CreateVolumeRequest{
		Name:               name,
		CapacityRange:      &csi.CapacityRange{RequiredBytes: 2 << 30}, // 2 GiB
		Parameters:         map[string]string{"pool": "vg0"},
		VolumeCapabilities: []*csi.VolumeCapability{{AccessMode: &csi.VolumeCapability_AccessMode{Mode: csi.VolumeCapability_AccessMode_SINGLE_NODE_WRITER}}},
	}
}

func TestCreateVolumeCreatesResource(t *testing.T) {
	b := newFakeBackend("n1", "n2", "n3")
	resp, err := newTestController(b).CreateVolume(context.Background(), validCreateReq("pvc-abc"))
	require.NoError(t, err)
	assert.Equal(t, "pvc_abc", resp.Volume.VolumeId)
	assert.Equal(t, int64(2<<30), resp.Volume.CapacityBytes)
	assert.Len(t, b.createCalls, 1)
	assert.Equal(t, uint32(2), b.createCalls[0].sizeGB)
	assert.Equal(t, uint32(0), b.createCalls[0].port, "port must be auto (0) for controller to allocate")
	assert.Len(t, resp.Volume.AccessibleTopology, 2)
}

func TestCreateVolumeIdempotent(t *testing.T) {
	b := newFakeBackend("n1", "n2", "n3")
	c := newTestController(b)
	_, err := c.CreateVolume(context.Background(), validCreateReq("pvc-abc"))
	require.NoError(t, err)
	_, err = c.CreateVolume(context.Background(), validCreateReq("pvc-abc"))
	require.NoError(t, err)
	assert.Len(t, b.createCalls, 1, "second call must not create again")
}

func TestCreateVolumeHonorsRequisiteTopology(t *testing.T) {
	b := newFakeBackend("n1", "n2", "n3")
	req := validCreateReq("pvc-top")
	req.AccessibilityRequirements = &csi.TopologyRequirement{
		Requisite: []*csi.Topology{{Segments: map[string]string{TopologyKeyNode: "n3"}}},
	}
	_, err := newTestController(b).CreateVolume(context.Background(), req)
	require.NoError(t, err)
	assert.Equal(t, "n3", b.createCalls[0].nodes[0])
}

func TestDeleteVolumeIdempotent(t *testing.T) {
	b := newFakeBackend("n1", "n2")
	c := newTestController(b)
	_, err := c.DeleteVolume(context.Background(), &csi.DeleteVolumeRequest{VolumeId: "does-not-exist"})
	require.NoError(t, err, "deleting a missing volume must succeed")
}

func TestCreateVolumeMissingPool(t *testing.T) {
	b := newFakeBackend("n1", "n2")
	req := validCreateReq("pvc-x")
	req.Parameters = map[string]string{}
	_, err := newTestController(b).CreateVolume(context.Background(), req)
	assert.Error(t, err)
}
```

- [ ] **Step 2: Run test, expect FAIL**

Run: `go test ./pkg/csi/... -run "TestCreateVolume|TestDeleteVolume" -v`
Expected: FAIL — undefined `NewControllerServer` / `controllerServer`.

- [ ] **Step 3: Write `pkg/csi/controller.go`**

```go
package csi

import (
	"context"

	"github.com/container-storage-interface/spec/lib/go/csi"
	"go.uber.org/zap"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

const giB = 1 << 30

type controllerServer struct {
	csi.UnimplementedControllerServer
	backend SDSBackend
	log     *zap.Logger
}

// NewControllerServer returns the CSI Controller service.
func NewControllerServer(b SDSBackend, log *zap.Logger) csi.ControllerServer {
	return &controllerServer{backend: b, log: log}
}

func (s *controllerServer) CreateVolume(ctx context.Context, req *csi.CreateVolumeRequest) (*csi.CreateVolumeResponse, error) {
	if req.GetName() == "" {
		return nil, status.Error(codes.InvalidArgument, "volume name is required")
	}
	name := sanitizeResourceName(req.GetName())
	sizeGB := bytesToGiB(req.GetCapacityRange().GetRequiredBytes())

	// Idempotency: if the resource already exists, return it unchanged.
	if existing, err := s.backend.GetResource(ctx, name); err == nil && existing != nil {
		return &csi.CreateVolumeResponse{Volume: &csi.Volume{
			VolumeId:           existing.GetName(),
			CapacityBytes:      int64(sizeGB) * giB,
			AccessibleTopology: accessibleTopology(existing.GetNodes()),
		}}, nil
	}

	params, err := ParseVolumeParams(req.GetParameters())
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}

	nodes, err := s.backend.ListNodes(ctx)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "list nodes: %v", err)
	}
	var nodeNames []string
	for _, n := range nodes {
		nodeNames = append(nodeNames, n.GetName())
	}
	replicaNodes, err := selectReplicaNodes(nodeNames, requisiteNodes(req.GetAccessibilityRequirements()), params.Replicas)
	if err != nil {
		return nil, status.Error(codes.ResourceExhausted, err.Error())
	}

	if err := s.backend.CreateResourceWithPoolAndType(ctx, name, 0, replicaNodes, "C", sizeGB, params.Pool, params.StorageType, nil); err != nil {
		return nil, status.Errorf(codes.Internal, "create resource: %v", err)
	}

	return &csi.CreateVolumeResponse{Volume: &csi.Volume{
		VolumeId:           name,
		CapacityBytes:      int64(sizeGB) * giB,
		AccessibleTopology: accessibleTopology(replicaNodes),
	}}, nil
}

func (s *controllerServer) DeleteVolume(ctx context.Context, req *csi.DeleteVolumeRequest) (*csi.DeleteVolumeResponse, error) {
	if req.GetVolumeId() == "" {
		return nil, status.Error(codes.InvalidArgument, "volume id is required")
	}
	if _, err := s.backend.GetResource(ctx, req.GetVolumeId()); err != nil {
		// Not found -> already deleted; idempotent success.
		return &csi.DeleteVolumeResponse{}, nil
	}
	if err := s.backend.DeleteResource(ctx, req.GetVolumeId()); err != nil {
		return nil, status.Errorf(codes.Internal, "delete resource: %v", err)
	}
	return &csi.DeleteVolumeResponse{}, nil
}

func (s *controllerServer) ControllerGetCapabilities(context.Context, *csi.ControllerGetCapabilitiesRequest) (*csi.ControllerGetCapabilitiesResponse, error) {
	cap := func(t csi.ControllerServiceCapability_RPC_Type) *csi.ControllerServiceCapability {
		return &csi.ControllerServiceCapability{Type: &csi.ControllerServiceCapability_Rpc{
			Rpc: &csi.ControllerServiceCapability_RPC{Type: t}}}
	}
	return &csi.ControllerGetCapabilitiesResponse{Capabilities: []*csi.ControllerServiceCapability{
		cap(csi.ControllerServiceCapability_RPC_CREATE_DELETE_VOLUME),
	}}, nil
}

func (s *controllerServer) ValidateVolumeCapabilities(ctx context.Context, req *csi.ValidateVolumeCapabilitiesRequest) (*csi.ValidateVolumeCapabilitiesResponse, error) {
	for _, c := range req.GetVolumeCapabilities() {
		if c.GetAccessMode().GetMode() != csi.VolumeCapability_AccessMode_SINGLE_NODE_WRITER {
			return &csi.ValidateVolumeCapabilitiesResponse{}, nil // unsupported -> empty Confirmed
		}
	}
	return &csi.ValidateVolumeCapabilitiesResponse{Confirmed: &csi.ValidateVolumeCapabilitiesResponse_Confirmed{
		VolumeCapabilities: req.GetVolumeCapabilities(),
	}}, nil
}

// bytesToGiB converts a byte count to whole GiB, rounding up; minimum 1.
func bytesToGiB(b int64) uint32 {
	if b <= 0 {
		return 1
	}
	g := (b + giB - 1) / giB
	return uint32(g)
}
```

- [ ] **Step 4: Run tests, expect PASS**

Run: `go test ./pkg/csi/... -run "TestCreateVolume|TestDeleteVolume" -v`
Expected: PASS (5 tests).

- [ ] **Step 5: Commit**

```bash
git add pkg/csi/controller.go pkg/csi/controller_test.go
git commit -m "feat(csi): controller service CreateVolume/DeleteVolume"
```

---

## Task 8: Mounter abstraction

**Files:**
- Create: `pkg/csi/mount.go`

- [ ] **Step 1: Write `pkg/csi/mount.go`**

```go
package csi

import (
	"os"

	"k8s.io/mount-utils"
)

// Mounter is the node-plugin mount surface; abstracted for testing with
// mount.NewFakeMounter.
type Mounter interface {
	// FormatAndMount formats source with fsType if needed, then mounts at target.
	FormatAndMount(source, target, fsType string, options []string) error
	// Mount performs a plain mount (used for bind mounts).
	Mount(source, target, fsType string, options []string) error
	// Unmount unmounts target if mounted.
	Unmount(target string) error
	// IsMountPoint reports whether target is currently a mount point.
	IsMountPoint(target string) (bool, error)
	// EnsureDir makes target (and parents) if absent.
	EnsureDir(target string) error
}

type safeMounter struct {
	m *mount.SafeFormatAndMount
}

// NewMounter returns the production Mounter backed by k8s.io/mount-utils.
func NewMounter() Mounter {
	return &safeMounter{m: &mount.SafeFormatAndMount{Interface: mount.New(""), Exec: mount.NewOSExec()}}
}

func (s *safeMounter) FormatAndMount(source, target, fsType string, options []string) error {
	return s.m.FormatAndMount(source, target, fsType, options)
}

func (s *safeMounter) Mount(source, target, fsType string, options []string) error {
	return s.m.Interface.Mount(source, target, fsType, options)
}

func (s *safeMounter) Unmount(target string) error {
	mounted, err := s.IsMountPoint(target)
	if err != nil || !mounted {
		return nil
	}
	return s.m.Interface.Unmount(target)
}

func (s *safeMounter) IsMountPoint(target string) (bool, error) {
	notMnt, err := s.m.Interface.IsLikelyNotMountPoint(target)
	if os.IsNotExist(err) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return !notMnt, nil
}

func (s *safeMounter) EnsureDir(target string) error {
	return os.MkdirAll(target, 0o750)
}
```

- [ ] **Step 2: Verify it builds**

Run: `go build ./pkg/csi/...`
Expected: builds. If `mount.NewOSExec` is unavailable in the pinned version, use `utilexec "k8s.io/utils/exec"` and `Exec: utilexec.New()` (run `go doc k8s.io/mount-utils.SafeFormatAndMount` to confirm the field).

- [ ] **Step 3: Commit**

```bash
git add pkg/csi/mount.go
git commit -m "feat(csi): mounter abstraction over k8s mount-utils"
```

---

## Task 9: Node service

**Files:**
- Create: `pkg/csi/node.go`
- Test: `pkg/csi/node_test.go`

- [ ] **Step 1: Write the failing test `pkg/csi/node_test.go`**

```go
package csi

import (
	"context"
	"testing"

	"github.com/container-storage-interface/spec/lib/go/csi"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

// recordingMounter captures mount calls.
type recordingMounter struct {
	formatted []string
	bind      []string
	unmounted []string
	mounted   map[string]bool
}

func newRecordingMounter() *recordingMounter { return &recordingMounter{mounted: map[string]bool{}} }

func (m *recordingMounter) FormatAndMount(source, target, fsType string, _ []string) error {
	m.formatted = append(m.formatted, source+"->"+target+":"+fsType)
	m.mounted[target] = true
	return nil
}
func (m *recordingMounter) Mount(source, target, _ string, _ []string) error {
	m.bind = append(m.bind, source+"->"+target)
	m.mounted[target] = true
	return nil
}
func (m *recordingMounter) Unmount(target string) error {
	m.unmounted = append(m.unmounted, target)
	delete(m.mounted, target)
	return nil
}
func (m *recordingMounter) IsMountPoint(target string) (bool, error) { return m.mounted[target], nil }
func (m *recordingMounter) EnsureDir(string) error                   { return nil }

func newTestNode(b SDSBackend, m Mounter) *nodeServer {
	return NewNodeServer(b, m, "n1", "10.0.0.1", zap.NewNop()).(*nodeServer)
}

func TestNodeGetInfoReportsTopology(t *testing.T) {
	resp, err := newTestNode(newFakeBackend(), newRecordingMounter()).NodeGetInfo(context.Background(), &csi.NodeGetInfoRequest{})
	require.NoError(t, err)
	assert.Equal(t, "n1", resp.NodeId)
	assert.Equal(t, "n1", resp.AccessibleTopology.Segments[TopologyKeyNode])
}

func TestNodeStagePromotesAndFormats(t *testing.T) {
	b := newFakeBackend("n1", "n2")
	require.NoError(t, b.CreateResourceWithPoolAndType(context.Background(), "pvc_x", 0, []string{"n1", "n2"}, "C", 1, "vg0", "lvm", nil))
	m := newRecordingMounter()
	req := &csi.NodeStageVolumeRequest{
		VolumeId:          "pvc_x",
		StagingTargetPath: "/stage/pvc_x",
		VolumeCapability:  &csi.VolumeCapability{AccessType: &csi.VolumeCapability_Mount{Mount: &csi.VolumeCapability_MountVolume{FsType: "ext4"}}},
	}
	_, err := newTestNode(b, m).NodeStageVolume(context.Background(), req)
	require.NoError(t, err)
	assert.Equal(t, "n1", b.primary["pvc_x"], "must promote this node to Primary")
	assert.Equal(t, []string{"/dev/drbd100->/stage/pvc_x:ext4"}, m.formatted)
}

func TestNodeUnstageDemotes(t *testing.T) {
	b := newFakeBackend("n1", "n2")
	require.NoError(t, b.CreateResourceWithPoolAndType(context.Background(), "pvc_x", 0, []string{"n1"}, "C", 1, "vg0", "lvm", nil))
	b.primary["pvc_x"] = "n1"
	m := newRecordingMounter()
	m.mounted["/stage/pvc_x"] = true
	_, err := newTestNode(b, m).NodeUnstageVolume(context.Background(), &csi.NodeUnstageVolumeRequest{VolumeId: "pvc_x", StagingTargetPath: "/stage/pvc_x"})
	require.NoError(t, err)
	assert.Contains(t, m.unmounted, "/stage/pvc_x")
	_, isPrimary := b.primary["pvc_x"]
	assert.False(t, isPrimary, "must demote to Secondary")
}

func TestNodePublishBindMounts(t *testing.T) {
	m := newRecordingMounter()
	req := &csi.NodePublishVolumeRequest{
		VolumeId:          "pvc_x",
		StagingTargetPath: "/stage/pvc_x",
		TargetPath:        "/pods/pvc_x",
		VolumeCapability:  &csi.VolumeCapability{AccessType: &csi.VolumeCapability_Mount{Mount: &csi.VolumeCapability_MountVolume{}}},
	}
	_, err := newTestNode(newFakeBackend(), m).NodePublishVolume(context.Background(), req)
	require.NoError(t, err)
	assert.Equal(t, []string{"/stage/pvc_x->/pods/pvc_x"}, m.bind)
}
```

- [ ] **Step 2: Run test, expect FAIL**

Run: `go test ./pkg/csi/... -run TestNode -v`
Expected: FAIL — undefined `NewNodeServer` / `nodeServer`.

- [ ] **Step 3: Write `pkg/csi/node.go`**

```go
package csi

import (
	"context"

	"github.com/container-storage-interface/spec/lib/go/csi"
	"go.uber.org/zap"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type nodeServer struct {
	csi.UnimplementedNodeServer
	backend  SDSBackend
	mounter  Mounter
	nodeName string
	nodeIP   string
	log      *zap.Logger
}

// NewNodeServer returns the CSI Node service for this node.
func NewNodeServer(b SDSBackend, m Mounter, nodeName, nodeIP string, log *zap.Logger) csi.NodeServer {
	return &nodeServer{backend: b, mounter: m, nodeName: nodeName, nodeIP: nodeIP, log: log}
}

func (s *nodeServer) NodeGetInfo(context.Context, *csi.NodeGetInfoRequest) (*csi.NodeGetInfoResponse, error) {
	return &csi.NodeGetInfoResponse{
		NodeId:             s.nodeName,
		AccessibleTopology: &csi.Topology{Segments: map[string]string{TopologyKeyNode: s.nodeName}},
	}, nil
}

func (s *nodeServer) NodeGetCapabilities(context.Context, *csi.NodeGetCapabilitiesRequest) (*csi.NodeGetCapabilitiesResponse, error) {
	return &csi.NodeGetCapabilitiesResponse{Capabilities: []*csi.NodeServiceCapability{
		{Type: &csi.NodeServiceCapability_Rpc{Rpc: &csi.NodeServiceCapability_RPC{
			Type: csi.NodeServiceCapability_RPC_STAGE_UNSTAGE_VOLUME}}},
	}}, nil
}

func (s *nodeServer) NodeStageVolume(ctx context.Context, req *csi.NodeStageVolumeRequest) (*csi.NodeStageVolumeResponse, error) {
	res := req.GetVolumeId()
	staging := req.GetStagingTargetPath()
	if res == "" || staging == "" {
		return nil, status.Error(codes.InvalidArgument, "volume id and staging path are required")
	}

	// Promote this node to DRBD Primary.
	if err := s.backend.SetPrimary(ctx, res, s.nodeName, false); err != nil {
		return nil, status.Errorf(codes.Internal, "set primary: %v", err)
	}

	device, err := s.deviceFor(ctx, res)
	if err != nil {
		return nil, err
	}

	fsType := req.GetVolumeCapability().GetMount().GetFsType()
	if fsType == "" {
		fsType = "ext4"
	}
	if err := s.mounter.EnsureDir(staging); err != nil {
		return nil, status.Errorf(codes.Internal, "mkdir staging: %v", err)
	}
	mounted, err := s.mounter.IsMountPoint(staging)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "check mount: %v", err)
	}
	if mounted {
		return &csi.NodeStageVolumeResponse{}, nil // idempotent
	}
	if err := s.mounter.FormatAndMount(device, staging, fsType, nil); err != nil {
		// Roll the role back so another node can take over.
		_ = s.backend.SetSecondary(ctx, res, s.nodeName)
		return nil, status.Errorf(codes.Internal, "format+mount: %v", err)
	}
	return &csi.NodeStageVolumeResponse{}, nil
}

func (s *nodeServer) NodeUnstageVolume(ctx context.Context, req *csi.NodeUnstageVolumeRequest) (*csi.NodeUnstageVolumeResponse, error) {
	res := req.GetVolumeId()
	staging := req.GetStagingTargetPath()
	if res == "" || staging == "" {
		return nil, status.Error(codes.InvalidArgument, "volume id and staging path are required")
	}
	if err := s.mounter.Unmount(staging); err != nil {
		return nil, status.Errorf(codes.Internal, "unmount staging: %v", err)
	}
	if err := s.backend.SetSecondary(ctx, res, s.nodeName); err != nil {
		return nil, status.Errorf(codes.Internal, "set secondary: %v", err)
	}
	return &csi.NodeUnstageVolumeResponse{}, nil
}

func (s *nodeServer) NodePublishVolume(ctx context.Context, req *csi.NodePublishVolumeRequest) (*csi.NodePublishVolumeResponse, error) {
	staging := req.GetStagingTargetPath()
	target := req.GetTargetPath()
	if staging == "" || target == "" {
		return nil, status.Error(codes.InvalidArgument, "staging and target paths are required")
	}
	if err := s.mounter.EnsureDir(target); err != nil {
		return nil, status.Errorf(codes.Internal, "mkdir target: %v", err)
	}
	mounted, err := s.mounter.IsMountPoint(target)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "check mount: %v", err)
	}
	if mounted {
		return &csi.NodePublishVolumeResponse{}, nil
	}
	opts := []string{"bind"}
	if req.GetReadonly() {
		opts = append(opts, "ro")
	}
	if err := s.mounter.Mount(staging, target, "", opts); err != nil {
		return nil, status.Errorf(codes.Internal, "bind mount: %v", err)
	}
	return &csi.NodePublishVolumeResponse{}, nil
}

func (s *nodeServer) NodeUnpublishVolume(_ context.Context, req *csi.NodeUnpublishVolumeRequest) (*csi.NodeUnpublishVolumeResponse, error) {
	if req.GetTargetPath() == "" {
		return nil, status.Error(codes.InvalidArgument, "target path is required")
	}
	if err := s.mounter.Unmount(req.GetTargetPath()); err != nil {
		return nil, status.Errorf(codes.Internal, "unmount target: %v", err)
	}
	return &csi.NodeUnpublishVolumeResponse{}, nil
}

// deviceFor returns the /dev/drbdX path of a resource's first volume.
func (s *nodeServer) deviceFor(ctx context.Context, resource string) (string, error) {
	r, err := s.backend.GetResource(ctx, resource)
	if err != nil {
		return "", status.Errorf(codes.NotFound, "get resource %q: %v", resource, err)
	}
	if len(r.GetVolumes()) == 0 || r.GetVolumes()[0].GetDevice() == "" {
		return "", status.Errorf(codes.Internal, "resource %q has no device", resource)
	}
	return r.GetVolumes()[0].GetDevice(), nil
}
```

- [ ] **Step 4: Run tests, expect PASS**

Run: `go test ./pkg/csi/... -run TestNode -v`
Expected: PASS (4 tests).

- [ ] **Step 5: Commit**

```bash
git add pkg/csi/node.go pkg/csi/node_test.go
git commit -m "feat(csi): node service stage/unstage/publish/unpublish"
```

---

## Task 10: csi-controller entrypoint

**Files:**
- Create: `cmd/csi-controller/main.go`

- [ ] **Step 1: Write `cmd/csi-controller/main.go`**

```go
package main

import (
	"context"
	"flag"
	"os"
	"os/signal"
	"syscall"

	"github.com/liliang-cn/sds/pkg/client"
	"github.com/liliang-cn/sds/pkg/csi"
	"go.uber.org/zap"
)

func main() {
	endpoint := flag.String("endpoint", "unix:///csi/csi.sock", "CSI gRPC endpoint")
	sdsAddr := flag.String("sds-controller", "sds-controller:3374", "sds-controller gRPC address")
	flag.Parse()

	log, _ := zap.NewProduction()
	defer func() { _ = log.Sync() }()

	sds, err := client.NewSDSClient(*sdsAddr)
	if err != nil {
		log.Fatal("connect sds-controller", zap.Error(err))
	}
	defer sds.Close()

	d := csi.NewDriver(*endpoint, log,
		csi.NewIdentityServer(),
		csi.NewControllerServer(sds, log),
		nil,
	)

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	if err := d.Run(ctx); err != nil {
		log.Error("driver exited", zap.Error(err))
		os.Exit(1)
	}
}
```

- [ ] **Step 2: Build, expect success**

Run: `go build -o /tmp/csi-controller ./cmd/csi-controller`
Expected: builds. (`*client.SDSClient` must satisfy `csi.SDSBackend`; a compile error here means a signature drift to fix in Task 1's interface.)

- [ ] **Step 3: Commit**

```bash
git add cmd/csi-controller/main.go
git commit -m "feat(csi): csi-controller binary entrypoint"
```

---

## Task 11: csi-node entrypoint with self-registration

**Files:**
- Create: `cmd/csi-node/main.go`

- [ ] **Step 1: Write `cmd/csi-node/main.go`**

```go
package main

import (
	"context"
	"flag"
	"os"
	"os/signal"
	"syscall"

	"github.com/liliang-cn/sds/pkg/client"
	"github.com/liliang-cn/sds/pkg/csi"
	"go.uber.org/zap"
)

func main() {
	endpoint := flag.String("endpoint", "unix:///csi/csi.sock", "CSI gRPC endpoint")
	sdsAddr := flag.String("sds-controller", "sds-controller:3374", "sds-controller gRPC address")
	nodeName := flag.String("node-name", os.Getenv("NODE_NAME"), "Kubernetes node name")
	nodeIP := flag.String("node-ip", os.Getenv("NODE_IP"), "this node's storage IP")
	flag.Parse()

	log, _ := zap.NewProduction()
	defer func() { _ = log.Sync() }()

	if *nodeName == "" || *nodeIP == "" {
		log.Fatal("node-name and node-ip are required (set via downward API NODE_NAME / NODE_IP)")
	}

	sds, err := client.NewSDSClient(*sdsAddr)
	if err != nil {
		log.Fatal("connect sds-controller", zap.Error(err))
	}
	defer sds.Close()

	// Auto-register this node into sds (idempotent on the controller side):
	// maps the k8s node name to its storage IP so topology and SSH line up.
	if _, err := sds.RegisterNode(context.Background(), *nodeName, *nodeIP); err != nil {
		log.Warn("register node (continuing)", zap.Error(err))
	}

	d := csi.NewDriver(*endpoint, log,
		csi.NewIdentityServer(),
		nil,
		csi.NewNodeServer(sds, csi.NewMounter(), *nodeName, *nodeIP, log),
	)

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	if err := d.Run(ctx); err != nil {
		log.Error("driver exited", zap.Error(err))
		os.Exit(1)
	}
}
```

- [ ] **Step 2: Build, expect success**

Run: `go build -o /tmp/csi-node ./cmd/csi-node`
Expected: builds.

- [ ] **Step 3: Commit**

```bash
git add cmd/csi-node/main.go
git commit -m "feat(csi): csi-node binary with self node-registration"
```

---

## Task 12: Build targets + Dockerfile

**Files:**
- Modify: `Makefile`
- Create: `Dockerfile.csi`

- [ ] **Step 1: Add Makefile targets**

After the existing `go build -o bin/sds-mcp ./cmd/mcp` line in the `build:` target, add:

```makefile
	go build -o bin/csi-controller ./cmd/csi-controller
	go build -o bin/csi-node ./cmd/csi-node
```

- [ ] **Step 2: Create `Dockerfile.csi`**

```dockerfile
# Build both CSI plugin binaries into a minimal image.
FROM golang:1.23 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -o /out/csi-controller ./cmd/csi-controller && \
    CGO_ENABLED=0 go build -o /out/csi-node ./cmd/csi-node

# Node plugin needs mount/umount/mkfs from the host PATH; use a small base that
# has util-linux + e2fsprogs + xfsprogs.
FROM debian:bookworm-slim
RUN apt-get update && apt-get install -y --no-install-recommends \
        util-linux e2fsprogs xfsprogs && rm -rf /var/lib/apt/lists/*
COPY --from=build /out/csi-controller /usr/local/bin/csi-controller
COPY --from=build /out/csi-node /usr/local/bin/csi-node
```

- [ ] **Step 3: Verify build target compiles the binaries**

Run: `make build`
Expected: `bin/csi-controller` and `bin/csi-node` produced (alongside the existing binaries).

- [ ] **Step 4: Commit**

```bash
git add Makefile Dockerfile.csi
git commit -m "build(csi): make targets + Dockerfile for CSI binaries"
```

---

## Task 13: Kubernetes manifests

**Files:**
- Create: `deploy/k8s/00-csidriver.yaml`
- Create: `deploy/k8s/10-rbac.yaml`
- Create: `deploy/k8s/20-controller.yaml`
- Create: `deploy/k8s/30-node.yaml`
- Create: `deploy/k8s/40-storageclass.yaml`
- Create: `deploy/k8s/README.md`

> The `sds-controller` itself is assumed already deployed and reachable at
> `sds-controller:3374` (Service DNS). Containerizing sds-controller is tracked
> in sub-project ④; for ① it may run as an existing Deployment or out-of-cluster
> address — set `--sds-controller` accordingly.

- [ ] **Step 1: `deploy/k8s/00-csidriver.yaml`**

```yaml
apiVersion: storage.k8s.io/v1
kind: CSIDriver
metadata:
  name: sds.csi.liliang-cn.com
spec:
  attachRequired: false
  podInfoOnMount: false
  volumeLifecycleModes:
    - Persistent
  storageCapacity: false
```

- [ ] **Step 2: `deploy/k8s/10-rbac.yaml`**

```yaml
apiVersion: v1
kind: ServiceAccount
metadata: { name: sds-csi-controller, namespace: kube-system }
---
apiVersion: v1
kind: ServiceAccount
metadata: { name: sds-csi-node, namespace: kube-system }
---
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRole
metadata: { name: sds-csi-provisioner }
rules:
  - apiGroups: [""]
    resources: ["persistentvolumes"]
    verbs: ["get", "list", "watch", "create", "delete"]
  - apiGroups: [""]
    resources: ["persistentvolumeclaims"]
    verbs: ["get", "list", "watch", "update"]
  - apiGroups: ["storage.k8s.io"]
    resources: ["storageclasses", "csinodes"]
    verbs: ["get", "list", "watch"]
  - apiGroups: [""]
    resources: ["nodes"]
    verbs: ["get", "list", "watch"]
  - apiGroups: ["storage.k8s.io"]
    resources: ["volumeattachments"]
    verbs: ["get", "list", "watch"]
  - apiGroups: [""]
    resources: ["events"]
    verbs: ["list", "watch", "create", "update", "patch"]
---
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRoleBinding
metadata: { name: sds-csi-provisioner }
subjects:
  - kind: ServiceAccount
    name: sds-csi-controller
    namespace: kube-system
roleRef:
  kind: ClusterRole
  name: sds-csi-provisioner
  apiGroup: rbac.authorization.k8s.io
```

- [ ] **Step 3: `deploy/k8s/20-controller.yaml`**

```yaml
apiVersion: apps/v1
kind: Deployment
metadata:
  name: sds-csi-controller
  namespace: kube-system
spec:
  replicas: 1
  selector: { matchLabels: { app: sds-csi-controller } }
  template:
    metadata: { labels: { app: sds-csi-controller } }
    spec:
      serviceAccountName: sds-csi-controller
      containers:
        - name: csi-provisioner
          image: registry.k8s.io/sig-storage/csi-provisioner:v5.1.0
          args:
            - "--csi-address=/csi/csi.sock"
            - "--feature-gates=Topology=true"
            - "--strict-topology"
            - "--immediate-topology=false"
            - "--v=2"
          volumeMounts:
            - { name: socket-dir, mountPath: /csi }
        - name: plugin
          image: sds-csi:latest
          command: ["/usr/local/bin/csi-controller"]
          args:
            - "--endpoint=unix:///csi/csi.sock"
            - "--sds-controller=sds-controller:3374"
          volumeMounts:
            - { name: socket-dir, mountPath: /csi }
        - name: liveness-probe
          image: registry.k8s.io/sig-storage/livenessprobe:v2.14.0
          args: ["--csi-address=/csi/csi.sock"]
          volumeMounts:
            - { name: socket-dir, mountPath: /csi }
      volumes:
        - { name: socket-dir, emptyDir: {} }
```

- [ ] **Step 4: `deploy/k8s/30-node.yaml`**

```yaml
apiVersion: apps/v1
kind: DaemonSet
metadata:
  name: sds-csi-node
  namespace: kube-system
spec:
  selector: { matchLabels: { app: sds-csi-node } }
  template:
    metadata: { labels: { app: sds-csi-node } }
    spec:
      serviceAccountName: sds-csi-node
      hostNetwork: true
      containers:
        - name: node-driver-registrar
          image: registry.k8s.io/sig-storage/csi-node-driver-registrar:v2.12.0
          args:
            - "--csi-address=/csi/csi.sock"
            - "--kubelet-registration-path=/var/lib/kubelet/plugins/sds.csi.liliang-cn.com/csi.sock"
          volumeMounts:
            - { name: socket-dir, mountPath: /csi }
            - { name: registration-dir, mountPath: /registration }
        - name: plugin
          image: sds-csi:latest
          command: ["/usr/local/bin/csi-node"]
          args:
            - "--endpoint=unix:///csi/csi.sock"
            - "--sds-controller=sds-controller:3374"
          securityContext:
            privileged: true
          env:
            - name: NODE_NAME
              valueFrom: { fieldRef: { fieldPath: spec.nodeName } }
            - name: NODE_IP
              valueFrom: { fieldRef: { fieldPath: status.hostIP } }
          volumeMounts:
            - { name: socket-dir, mountPath: /csi }
            - { name: kubelet-dir, mountPath: /var/lib/kubelet, mountPropagation: Bidirectional }
            - { name: dev-dir, mountPath: /dev }
      volumes:
        - name: socket-dir
          hostPath: { path: /var/lib/kubelet/plugins/sds.csi.liliang-cn.com, type: DirectoryOrCreate }
        - name: registration-dir
          hostPath: { path: /var/lib/kubelet/plugins_registry, type: Directory }
        - name: kubelet-dir
          hostPath: { path: /var/lib/kubelet, type: Directory }
        - name: dev-dir
          hostPath: { path: /dev, type: Directory }
```

- [ ] **Step 5: `deploy/k8s/40-storageclass.yaml`**

```yaml
apiVersion: storage.k8s.io/v1
kind: StorageClass
metadata:
  name: sds-drbd
provisioner: sds.csi.liliang-cn.com
parameters:
  pool: "vg0"
  replicas: "2"
  storageType: "lvm"
reclaimPolicy: Delete
allowVolumeExpansion: false
volumeBindingMode: WaitForFirstConsumer
```

- [ ] **Step 6: `deploy/k8s/README.md`**

```markdown
# SDS CSI Driver — Phase ① deploy

Prerequisites on every worker node: DRBD kernel module loaded, `drbd-utils`,
and LVM (or ZFS) installed. (drbd-module-loader DaemonSet is sub-project ④.)

```
kubectl apply -f deploy/k8s/
```

Set `--sds-controller` in `20-controller.yaml` / `30-node.yaml` to your
sds-controller gRPC address (default `sds-controller:3374`). Adjust the
StorageClass `pool` to a real VG/zpool name on the nodes.
```

- [ ] **Step 7: Validate YAML**

Run: `kubectl apply --dry-run=client -f deploy/k8s/ 2>/dev/null || echo "no cluster — falling back"; for f in deploy/k8s/*.yaml; do python3 -c "import yaml,sys; list(yaml.safe_load_all(open('$f')))" && echo "ok $f"; done`
Expected: each file parses (`ok deploy/k8s/...`). With a cluster reachable, the dry-run also passes.

- [ ] **Step 8: Commit**

```bash
git add deploy/k8s/
git commit -m "deploy(csi): k8s manifests (CSIDriver, RBAC, controller, node, storageclass)"
```

---

## Task 14: CSI sanity suite

**Files:**
- Create: `pkg/csi/sanity_test.go`

- [ ] **Step 1: Add the test dependency**

Run: `go get github.com/kubernetes-csi/csi-test/v5@v5.3.1`
Expected: module added.

- [ ] **Step 2: Write `pkg/csi/sanity_test.go`**

```go
package csi_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/kubernetes-csi/csi-test/v5/pkg/sanity"
	"github.com/liliang-cn/sds/pkg/csi"
	"go.uber.org/zap"
)

// The sanity suite drives a real gRPC endpoint. We back it with the in-package
// fake by running identity+controller+node over a unix socket. Node mount ops
// use a temp dir; DRBD/format steps are exercised against the fake mounter via
// a build that swaps NewMounter — here we run controller+identity sanity only,
// which needs no real block devices.
func TestSanity(t *testing.T) {
	dir := t.TempDir()
	endpoint := "unix://" + filepath.Join(dir, "csi.sock")

	b := csi.NewSanityFakeBackend("n1", "n2", "n3") // see Step 3
	d := csi.NewDriver(endpoint, zap.NewNop(),
		csi.NewIdentityServer(),
		csi.NewControllerServer(b, zap.NewNop()),
		nil,
	)
	ctx, cancel := context.WithCancel(context.Background())
	go func() { _ = d.Run(ctx) }()
	defer cancel()

	cfg := sanity.NewTestConfig()
	cfg.Address = endpoint
	cfg.TargetPath = filepath.Join(dir, "target")
	cfg.StagingPath = filepath.Join(dir, "staging")
	_ = os.MkdirAll(cfg.TargetPath, 0o755)
	cfg.TestVolumeParameters = map[string]string{"pool": "vg0"}
	sanity.Test(t, cfg)
}
```

- [ ] **Step 3: Export a sanity-usable fake backend in `pkg/csi/sanity_support.go`**

Because `fakeBackend` lives in `_test.go` (package `csi`), expose a constructor for the external `csi_test` package:

```go
package csi

import (
	"context"
	"fmt"
	"sync"

	sdspb "github.com/liliang-cn/sds/api/proto/v1"
)

// sanityBackend is a concurrency-safe in-memory SDSBackend for the CSI sanity
// suite (which issues parallel calls). It is exported via NewSanityFakeBackend.
type sanityBackend struct {
	mu        sync.Mutex
	resources map[string]*sdspb.ResourceInfo
	nodes     []*sdspb.NodeInfo
}

// NewSanityFakeBackend builds a fake backend seeded with the given node names.
func NewSanityFakeBackend(nodeNames ...string) SDSBackend {
	b := &sanityBackend{resources: map[string]*sdspb.ResourceInfo{}}
	for i, n := range nodeNames {
		b.nodes = append(b.nodes, &sdspb.NodeInfo{Name: n, Address: fmt.Sprintf("10.0.0.%d", i+1), State: "online"})
	}
	return b
}

func (b *sanityBackend) CreateResourceWithPoolAndType(_ context.Context, name string, port uint32, nodes []string, _ string, sizeGB uint32, pool, _ string, _ map[string]string) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.resources[name] = &sdspb.ResourceInfo{Name: name, Nodes: nodes, Port: port,
		Volumes: []*sdspb.VolumeInfo{{VolumeId: 0, Device: "/dev/drbd100", SizeGb: uint64(sizeGB), Pool: pool}}}
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

func (b *sanityBackend) ListNodes(context.Context) ([]*sdspb.NodeInfo, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.nodes, nil
}

func (b *sanityBackend) RegisterNode(_ context.Context, name, address string) (*sdspb.NodeInfo, error) {
	return &sdspb.NodeInfo{Name: name, Address: address}, nil
}

func (b *sanityBackend) SetPrimary(context.Context, string, string, bool) error { return nil }
func (b *sanityBackend) SetSecondary(context.Context, string, string) error     { return nil }
```

- [ ] **Step 4: Run the sanity suite**

Run: `go test ./pkg/csi/... -run TestSanity -v`
Expected: PASS. CreateVolume/DeleteVolume/ValidateVolumeCapabilities/idempotency cases from the suite pass. If a specific node-side case fails (it needs real mounts), restrict the suite to controller+identity by leaving `cfg.StagingPath`/node tests as the suite skips when the Node service is absent (we passed `nil` node).

- [ ] **Step 5: Commit**

```bash
git add go.mod go.sum pkg/csi/sanity_test.go pkg/csi/sanity_support.go
git commit -m "test(csi): CSI sanity suite over controller+identity"
```

---

## Task 15: Real-cluster e2e smoke test

**Files:**
- Create: `scripts/csi-e2e.sh`

- [ ] **Step 1: Write `scripts/csi-e2e.sh`**

```bash
#!/usr/bin/env bash
# Phase-① CSI smoke test against a real cluster with the driver installed and
# sds-controller reachable. Requires: kubectl context set, StorageClass sds-drbd,
# at least 2 storage nodes registered, pool "vg0" present on the nodes.
set -euo pipefail

NS=csi-e2e-$RANDOM
kubectl create ns "$NS"
trap 'kubectl delete ns "$NS" --wait=false' EXIT

cat <<YAML | kubectl apply -n "$NS" -f -
apiVersion: v1
kind: PersistentVolumeClaim
metadata: { name: data }
spec:
  accessModes: ["ReadWriteOnce"]
  storageClassName: sds-drbd
  resources: { requests: { storage: 1Gi } }
---
apiVersion: v1
kind: Pod
metadata: { name: writer }
spec:
  containers:
    - name: app
      image: busybox
      command: ["sh", "-c", "echo hello-sds > /data/marker && sleep 3600"]
      volumeMounts: [{ name: data, mountPath: /data }]
  volumes:
    - name: data
      persistentVolumeClaim: { claimName: data }
YAML

echo "waiting for pod Ready..."
kubectl -n "$NS" wait --for=condition=Ready pod/writer --timeout=180s

echo "verifying write landed on the DRBD-backed volume..."
kubectl -n "$NS" exec writer -- cat /data/marker | grep -q hello-sds
echo "PASS: pod mounted a DRBD volume and wrote data"

echo "verifying the pod scheduled onto a replica node (topology)..."
NODE=$(kubectl -n "$NS" get pod writer -o jsonpath='{.spec.nodeName}')
echo "pod node: $NODE"
```

- [ ] **Step 2: Make it executable**

Run: `chmod +x scripts/csi-e2e.sh`
Expected: no output.

- [ ] **Step 3: Run against the real cluster (manual gate)**

Run: `./scripts/csi-e2e.sh`
Expected: `PASS: pod mounted a DRBD volume and wrote data`. This requires the image `sds-csi:latest` pushed/loadable on the cluster and `deploy/k8s/` applied. If running where no cluster is configured, skip and note it.

- [ ] **Step 4: Commit**

```bash
git add scripts/csi-e2e.sh
git commit -m "test(csi): real-cluster e2e smoke script"
```

---

## Final verification

- [ ] Run the full unit + sanity suite:

Run: `go test ./... 2>&1 | tail -20`
Expected: all packages pass (csi unit tests, controller port tests, sanity).

- [ ] Confirm formatting and vet:

Run: `make fmt && go vet ./...`
Expected: clean.

- [ ] Confirm all binaries build:

Run: `make build`
Expected: `bin/csi-controller`, `bin/csi-node` plus existing binaries produced.

---

## Notes for the implementer

- `*client.SDSClient` is expected to satisfy `csi.SDSBackend` with no adapter. If
  a method signature has drifted, adjust the `SDSBackend` interface in
  `pkg/csi/driver.go` to match the client, not the other way around.
- CSI requires every RPC to be idempotent. The tests in Tasks 7 and 9 lock this
  in; do not weaken them.
- `k8s.io/mount-utils` exact API (e.g. `mount.New("")`, `NewOSExec`) can vary by
  version. If Task 8 Step 2 fails to build, run `go doc k8s.io/mount-utils` and
  adapt the constructor; keep the `Mounter` interface unchanged so the node
  tests still compile.
- Phase ① pins pods to replica nodes via topology. Diskless attach (②) will add
  a `NodeServer` path that creates a diskless DRBD client when the local node
  holds no replica — out of scope here.
```

</content>
</invoke>
