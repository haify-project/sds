package controller

import (
	"context"
	crand "crypto/rand"
	"encoding/json"
	"fmt"
	"math/big"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/liliang-cn/sds/pkg/alert"
	"github.com/liliang-cn/sds/pkg/database"
	"github.com/liliang-cn/sds/pkg/deployment"
	"github.com/liliang-cn/sds/pkg/wanproxy"
	"go.uber.org/zap"
)

// ResourceInfo represents DRBD resource information
type ResourceInfo struct {
	Name       string
	Port       uint32
	Protocol   string
	Nodes      []string
	Role       string
	Volumes    []*ResourceVolumeInfo
	NodeStates map[string]*ResourceNodeState
	// DisklessNodes are nodes that join the resource purely as quorum
	// tiebreakers: they vote but store no data.
	DisklessNodes []string
	// DisklessClients are nodes attached as diskless data clients: no local
	// replica, but connected over DRBD and promotable to serve the volume over
	// the network. Distinct from DisklessNodes (quorum-only tiebreakers).
	DisklessClients []string
	// QuorumRisk is true when the resource has exactly two diskful nodes and
	// no tiebreaker, so losing either node suspends I/O (no quorum majority).
	QuorumRisk bool
	Labels     map[string]string
	Profile    string
	// WANMode marks a resource with an off-site asynchronous copy, and DRNode
	// names which of Nodes holds it. Listing the DR as just another replica is
	// misleading: it is a different site, replicates under protocol A, and never
	// takes over automatically.
	WANMode bool
	DRNode  string
	// Encrypted is true when every replica's backing volume is a LUKS2
	// container (DRBD → LUKS → LVM). Encryption at rest only: DRBD is
	// above the crypt layer, so replication traffic is plaintext.
	Encrypted bool
}

// ResourceNodeState represents detailed state of a node for a resource
type ResourceNodeState struct {
	Role        string
	DiskState   string
	Replication string
	// SyncPercent is the resync completion for a peer (0..100). It is 100 for a
	// node that is fully in sync / not resyncing, and only carries a meaningful
	// intermediate value while Replication is a resync state (SyncSource/
	// SyncTarget/PausedSync*).
	SyncPercent float64
	// SyncPercentKnown says whether SyncPercent was actually derived from DRBD
	// output. Only the structured `drbdsetup status --json` carries completion;
	// the plain-text fallback has no such field, so its states leave this false
	// with SyncPercent at its zero value.
	//
	// Anything charting completion over time must consult this first: a
	// text-parsed zero graphed as "0% synced" turns a perfectly healthy cluster
	// into one whose resync appears never to have started.
	SyncPercentKnown bool
	// Quorum is whether this node holds DRBD quorum for the resource, or nil
	// when DRBD did not report it. Nil is the normal case for peers — a node's
	// status only carries its own quorum — and for the plain-text parse, which
	// does not surface quorum at all.
	Quorum *bool
	// Connection is the peer's DRBD connection state as the answering node sees
	// it: "Connected", "Connecting", "StandAlone", ... Empty for the answering
	// node itself, which has no connection to describe.
	//
	// It exists because a peer that is not Connected reports no role, no disk
	// and no replication at all — drbdadm prints one line, "<peer>
	// connection:Connecting", and nothing else. Until this field, such a peer
	// was simply absent from the map, and absent reads as "nothing wrong with
	// it" everywhere downstream. Two real failures came out of that: a replica
	// sat StandAlone after a split brain for a day with no alert, and a node
	// rejoining the cluster — whose peers are all Connecting for a few seconds
	// — made the whole resource look like it had no Primary.
	Connection string
}

// ResourceVolumeInfo represents DRBD volume information
type ResourceVolumeInfo struct {
	VolumeID uint32
	Device   string
	SizeGB   uint64
	// Pool is the storage pool (volume group) backing this volume.
	Pool string
	// BackingVolume is the logical volume name inside the pool
	// (e.g. "<resource>_data"); "<pool>/<backing_volume>" is the path
	// consumed by snapshot operations.
	BackingVolume string
	// Encrypted is true when DRBD reaches this volume through a LUKS2
	// container rather than the LV/zvol directly. A snapshot of an encrypted
	// volume is a snapshot of the ciphertext.
	Encrypted bool
}

// ResourceManager manages DRBD resources using dispatch
type ResourceManager struct {
	controller *Controller
	deployment deploymentClient
	hosts      []string
	hostMap    map[string]string // hostname -> IP for config generation
	mu         sync.RWMutex
}

// NewResourceManager creates a new resource manager
func NewResourceManager(ctrl *Controller) *ResourceManager {
	return &ResourceManager{
		controller: ctrl,
		hosts:      make([]string, 0),
		hostMap:    make(map[string]string),
	}
}

// SetDeployment sets the deployment client
func (rm *ResourceManager) SetDeployment(client deploymentClient) {
	rm.mu.Lock()
	defer rm.mu.Unlock()
	rm.deployment = client
}

// SetHosts sets the list of hosts for resource operations
func (rm *ResourceManager) SetHosts(hosts []string) {
	rm.mu.Lock()
	defer rm.mu.Unlock()

	// Clean hosts list and build map
	var cleanHosts []string
	rm.hostMap = make(map[string]string)

	for _, host := range hosts {
		// Try to resolve hostname to IP
		parts := strings.Split(host, ":")
		if len(parts) > 1 {
			// Format: "hostname:ip"
			hostname := parts[0]
			ip := parts[1]

			rm.hostMap[hostname] = ip
			cleanHosts = append(cleanHosts, ip)
		} else {
			// Format: "hostname"
			rm.hostMap[host] = host
			cleanHosts = append(cleanHosts, host)
		}
	}
	rm.hosts = cleanHosts
}

// GetHosts returns the list of hosts
func (rm *ResourceManager) GetHosts() []string {
	rm.mu.RLock()
	defer rm.mu.RUnlock()
	return rm.hosts
}

func (rm *ResourceManager) resourceHosts(ctx context.Context, resource string) ([]string, error) {
	if rm.controller.db != nil {
		dbRes, err := rm.controller.db.GetResource(ctx, resource)
		if err == nil && dbRes != nil {
			var hosts []string
			for _, node := range strings.Split(dbRes.Nodes, ",") {
				node = strings.TrimSpace(node)
				if node == "" {
					continue
				}
				hosts = append(hosts, rm.controller.ResolveHost(node))
			}
			if len(hosts) > 0 {
				return hosts, nil
			}
		}
	}

	rm.mu.RLock()
	defer rm.mu.RUnlock()
	if len(rm.hosts) == 0 {
		return nil, fmt.Errorf("no hosts configured")
	}
	return append([]string(nil), rm.hosts...), nil
}

// disklessHosts returns the resolved addresses of a resource's diskless quorum
// tiebreaker nodes, or nil when it has none. Kept separate from resourceHosts
// because tiebreakers must never be treated as data-bearing nodes (e.g. as
// failover Primary candidates). They do hold a copy of the resource config,
// though, so teardown and every change to the volume set must reach them —
// see disklessParticipantHosts.
func (rm *ResourceManager) disklessHosts(ctx context.Context, resource string) []string {
	if rm.controller.db == nil {
		return nil
	}
	dbRes, err := rm.controller.db.GetResource(ctx, resource)
	if err != nil || dbRes == nil || dbRes.DisklessNodes == "" {
		return nil
	}
	var hosts []string
	for _, node := range strings.Split(dbRes.DisklessNodes, ",") {
		node = strings.TrimSpace(node)
		if node == "" {
			continue
		}
		hosts = append(hosts, rm.controller.ResolveHost(node))
	}
	return hosts
}

type resourceConfigVolume struct {
	VolumeID  int
	Minor     int
	DiskPath  string
	StartLine int
	EndLine   int
}

func parseResourceConfigVolumes(content string) []resourceConfigVolume {
	lines := strings.Split(content, "\n")
	var volumes []resourceConfigVolume
	var current *resourceConfigVolume
	depth := 0

	for idx, line := range lines {
		trimmed := strings.TrimSpace(line)

		if current == nil && strings.HasPrefix(trimmed, "volume ") && strings.Contains(trimmed, "{") {
			parts := strings.Fields(trimmed)
			if len(parts) >= 2 {
				volID, err := strconv.Atoi(strings.TrimSuffix(parts[1], "{"))
				if err == nil {
					current = &resourceConfigVolume{VolumeID: volID, Minor: -1, StartLine: idx}
					depth = strings.Count(line, "{") - strings.Count(line, "}")
					if depth <= 0 {
						current.EndLine = idx
						volumes = append(volumes, *current)
						current = nil
						depth = 0
					}
					continue
				}
			}
		}

		if current == nil {
			continue
		}

		if strings.Contains(trimmed, "device") && strings.Contains(trimmed, "minor") {
			parts := strings.Fields(trimmed)
			for i, part := range parts {
				if part == "minor" && i+1 < len(parts) {
					if minor, err := strconv.Atoi(strings.TrimSuffix(parts[i+1], ";")); err == nil {
						current.Minor = minor
					}
					break
				}
			}
		}

		if strings.HasPrefix(trimmed, "disk") {
			parts := strings.Fields(trimmed)
			if len(parts) >= 2 {
				current.DiskPath = strings.TrimSuffix(parts[1], ";")
			}
		}

		depth += strings.Count(line, "{") - strings.Count(line, "}")
		if depth <= 0 {
			current.EndLine = idx
			volumes = append(volumes, *current)
			current = nil
			depth = 0
		}
	}

	// Fall back to the older single-volume syntax (device/disk declared at the
	// resource level, no `volume {}` block) when no volume blocks were found,
	// so adopting a pre-9 / hand-written resource still discovers its volume.
	if len(volumes) == 0 {
		if implicit := parseImplicitVolume0(content); implicit != nil {
			volumes = append(volumes, *implicit)
		}
	}

	return volumes
}

// parseImplicitVolume0 handles the older single-volume DRBD syntax where the
// device/disk are declared directly at the resource level instead of inside a
// `volume {}` block, e.g.:
//
//	resource r {
//	  device    /dev/drbd0;
//	  disk      /dev/sdb;
//	  meta-disk internal;
//	  on node { ... }
//	}
//
// It returns a synthesized volume 0, or nil if no resource-level disk is found.
func parseImplicitVolume0(content string) *resourceConfigVolume {
	depth := 0
	vol := resourceConfigVolume{VolumeID: 0, Minor: -1}
	haveDisk := false
	for idx, line := range strings.Split(content, "\n") {
		trimmed := strings.TrimSpace(line)
		// Only resource-level declarations (depth 1) count — not lines inside
		// on{}, net{}, options{} or a disk{} options block.
		if depth == 1 {
			switch {
			case strings.HasPrefix(trimmed, "device") && !strings.Contains(trimmed, "minor"):
				// old form: `device /dev/drbdN;`
				if fields := strings.Fields(trimmed); len(fields) >= 2 {
					if m, ok := parseDevNodeMinor(strings.TrimSuffix(fields[1], ";")); ok {
						vol.Minor = m
						vol.EndLine = idx
					}
				}
			case strings.HasPrefix(trimmed, "disk") && !strings.Contains(trimmed, "{"):
				// `disk /dev/sdb;` — not a `disk {` options block; "meta-disk"
				// does not match the "disk" prefix.
				if fields := strings.Fields(trimmed); len(fields) >= 2 {
					vol.DiskPath = strings.TrimSuffix(fields[1], ";")
					vol.StartLine = idx
					vol.EndLine = idx
					haveDisk = true
				}
			}
		}
		depth += strings.Count(line, "{") - strings.Count(line, "}")
	}
	if !haveDisk {
		return nil
	}
	return &vol
}

func backingPathForVolume(pool, volumeName, storageType string) string {
	if storageType == "zfs" || storageType == "zfs-thin" {
		return fmt.Sprintf("/dev/zvol/%s/%s", pool, volumeName)
	}
	return fmt.Sprintf("/dev/%s/%s", pool, volumeName)
}

func findVolumeRecord(volumes []*database.Volume, volumeID uint32) *database.Volume {
	for _, volume := range volumes {
		if volume.VolumeID == int(volumeID) {
			return volume
		}
	}
	return nil
}

// VolumeSpec describes one DRBD volume to create: its size and (optionally) the
// pool it is backed by. The storage type is a resource-level property shared by
// all volumes.
type VolumeSpec struct {
	SizeGB uint32
	Pool   string
}

// resolvedVolume is a VolumeSpec with its pool auto-selected/normalized, a
// concrete backing-volume name and (later) an allocated device minor.
type resolvedVolume struct {
	id         int
	volumeName string
	pool       string
	sizeGB     uint32
	minor      int
	// encrypted routes DRBD at this volume's LUKS container instead of at the
	// LV/zvol. Carried per volume rather than passed alongside so the config
	// generator cannot be handed a volume list and the wrong flag.
	encrypted bool
}

// backingDevice is the path DRBD is pointed at for this volume: the crypt
// container when it is encrypted, the LV or zvol when it is not.
func (v resolvedVolume) backingDevice(storageType string) string {
	if v.encrypted {
		return luksMapperPath(v.pool, v.volumeName)
	}
	return backingPathForVolume(v.pool, v.volumeName, storageType)
}

// CreateResource creates a single-volume DRBD resource. It is a thin wrapper
// over CreateResourceWithVolumes retained for existing callers (CLI, self-HA,
// CSI) that only ever create one volume.
func (rm *ResourceManager) CreateResource(ctx context.Context, name string, port uint32, nodes []string, protocol string, sizeGB uint32, pool string, storageType string, drbdOptions map[string]string) error {
	// LAN path: WAN is nil, so the resource is created exactly as before.
	return rm.CreateResourceWithVolumes(ctx, name, port, nodes, protocol, storageType, drbdOptions,
		[]VolumeSpec{{SizeGB: sizeGB, Pool: pool}}, nil)
}

// WANSpec carries the opt-in WAN-replication parameters supplied to
// CreateResourceWithVolumes. Nil ⇒ an ordinary LAN resource (behavior
// unchanged). When set, the resource replicates between the single primary node
// in `nodes` and DRNode across the internet via a per-resource sds-proxy pair
// (protocol A + loopback-routed DRBD). See docs/2026-07-05-wan-replication-design.md.
type WANSpec struct {
	// DRNode is the DR-site node name. It must be a registered node and distinct
	// from the primary; it becomes the resource's second (and only) peer.
	DRNode string
	// DREndpoint is the DR site's public WAN address the primary dials.
	DREndpoint string
	// EgressAddress optionally pins the source address the primary's proxy binds
	// before dialing out, putting WAN replication on a chosen interface. Empty
	// lets the primary's routing table decide.
	EgressAddress string
	// WANPort is the WAN mTLS port the DR acceptor binds. Zero ⇒ the controller
	// picks a random high port (> 3000).
	WANPort uint32
}

// ResourceMetadata carries the resource-level choices that are not part of the
// volume list: organizational metadata, and whether the backing volumes are
// encrypted at rest.
type ResourceMetadata struct {
	Labels  map[string]string
	Profile string
	// Encrypt wraps every replica's backing volume in a LUKS2 container, so
	// the stack becomes DRBD → LUKS → LVM. Unlike Labels and Profile this
	// one does change what gets built, and it can only be chosen here: see
	// assertEncryptionNotRetrofitted.
	Encrypt bool
}

// AdoptResult summarizes what AdoptResource recorded, so callers can display it.
type AdoptResult struct {
	Name     string
	Nodes    []string
	Port     uint32
	Protocol string
	Volumes  int
}

// AdoptResource imports an already-existing (foreign) DRBD resource — one
// created outside SDS, e.g. a hand-configured resource — into SDS management by
// writing its metadata into the SDS database. It lets subsequent SDS operations
// (MakeHa, gateways, ...) that require db.GetResource work against it.
//
// It NEVER creates or modifies the DRBD resource, its backing devices, or any
// data: no drbdadm create-md/up/down, no lvcreate/mkfs, no writes to the
// backing device. It only reads the live /etc/drbd.d/<name>.res on a reachable
// node and records what it finds. When nodes/port/protocol are supplied they
// override the auto-discovered values; anything left empty/zero is discovered
// from the .res. Adopting a resource that is already recorded simply refreshes
// the record (idempotent, no error).
func (rm *ResourceManager) AdoptResource(ctx context.Context, name string, nodes []string, port uint32, protocol string) (*AdoptResult, error) {
	if rm.deployment == nil {
		return nil, fmt.Errorf("deployment client not set")
	}
	if rm.controller.db == nil {
		return nil, fmt.Errorf("database not available")
	}
	name = strings.TrimSpace(name)
	if name == "" {
		return nil, fmt.Errorf("resource name is required")
	}

	// Normalize an explicit --nodes list (trim, drop blanks).
	var flagNodes []string
	for _, n := range nodes {
		if n = strings.TrimSpace(n); n != "" {
			flagNodes = append(flagNodes, n)
		}
	}

	// Ordered candidate hosts to read the .res from: an explicit --nodes flag
	// wins, then every registered node, then any statically configured host.
	candidates := rm.adoptCandidateHosts(ctx, flagNodes)
	if len(candidates) == 0 {
		return nil, fmt.Errorf("no reachable node to read DRBD config for %q: register a node or pass --nodes", name)
	}

	// Read the live config and confirm the resource actually exists. This is a
	// pure read — it never mutates the resource.
	configContent, usedHost, err := rm.readForeignResourceConfig(ctx, candidates, name)
	if err != nil {
		return nil, err
	}

	// Auto-discover from the config; flags override.
	discNodes := parseResourceConfigNodes(configContent)
	discPort := parsePortFromConfig(configContent)
	cfgVolumes := parseResourceConfigVolumes(configContent)

	adoptNodes := flagNodes
	if len(adoptNodes) == 0 {
		adoptNodes = discNodes
	}
	if len(adoptNodes) == 0 {
		return nil, fmt.Errorf("could not determine nodes for %q from %s:/etc/drbd.d/%s.res; pass --nodes", name, usedHost, name)
	}
	if port == 0 {
		port = discPort
	}
	if protocol == "" {
		protocol = "C"
	}

	// Persist SDS metadata only — the DRBD resource and its data are left
	// exactly as they are.
	resRecord := &database.Resource{
		Name:     name,
		Nodes:    strings.Join(adoptNodes, ","),
		Port:     int(port),
		Protocol: protocol,
		Replicas: len(adoptNodes),
	}
	if existing, err := rm.controller.db.GetResource(ctx, name); err == nil {
		resRecord.Profile = existing.Profile
		resRecord.Labels = cloneStringMap(existing.Labels)
		resRecord.CreatedAt = existing.CreatedAt
	}
	if err := rm.controller.db.SaveResource(ctx, resRecord); err != nil {
		return nil, fmt.Errorf("record adopted resource %q: %w", name, err)
	}

	savedVolumes := 0
	seenVolID := make(map[int]bool)
	for _, v := range cfgVolumes {
		// Skip diskless volume overrides (`disk none;` in a tiebreaker's `on`
		// section) and any volume with no backing disk — they carry no data to
		// record and would duplicate the data-bearing volume of the same ID.
		if v.DiskPath == "" || v.DiskPath == "none" {
			continue
		}
		if seenVolID[v.VolumeID] {
			continue
		}
		seenVolID[v.VolumeID] = true

		volumeName, pool := volumeNameAndPoolFromDiskPath(v.DiskPath)
		if volumeName == "" {
			volumeName = fmt.Sprintf("%s_vol%d", name, v.VolumeID)
		}
		// SizeGB is best-effort and left 0: adoption never probes/opens the
		// backing device to size it.
		volRecord := &database.Volume{
			ResourceName: name,
			VolumeName:   volumeName,
			VolumeID:     v.VolumeID,
			Pool:         pool,
			Device:       v.DiskPath,
		}
		if err := rm.controller.db.SaveVolume(ctx, volRecord); err != nil {
			return nil, fmt.Errorf("record adopted volume %d of %q: %w", v.VolumeID, name, err)
		}
		savedVolumes++
	}

	rm.controller.logger.Info("Adopted foreign DRBD resource into SDS management",
		zap.String("resource", name),
		zap.Strings("nodes", adoptNodes),
		zap.Uint32("port", port),
		zap.String("protocol", protocol),
		zap.Int("volumes", savedVolumes),
		zap.String("read_from", usedHost))

	return &AdoptResult{
		Name:     name,
		Nodes:    adoptNodes,
		Port:     port,
		Protocol: protocol,
		Volumes:  savedVolumes,
	}, nil
}

// adoptCandidateHosts builds the ordered, de-duplicated list of node addresses
// to try when reading a foreign resource's .res: explicit flag nodes first,
// then every registered node, then any statically configured host.
func (rm *ResourceManager) adoptCandidateHosts(ctx context.Context, flagNodes []string) []string {
	var hosts []string
	seen := make(map[string]bool)
	add := func(h string) {
		if h = strings.TrimSpace(h); h == "" || seen[h] {
			return
		}
		seen[h] = true
		hosts = append(hosts, h)
	}
	for _, n := range flagNodes {
		add(rm.controller.ResolveHost(n))
	}
	if rm.controller.nodes != nil {
		if list, err := rm.controller.nodes.ListNodes(ctx); err == nil {
			for _, n := range list {
				add(n.Address)
			}
		}
	}
	for _, h := range rm.GetHosts() {
		add(h)
	}
	return hosts
}

// readForeignResourceConfig reads /etc/drbd.d/<name>.res from the first
// candidate host that actually has it. It validates that the resource exists
// (a present, non-empty .res that declares the resource) so adoption never
// fabricates a record for a resource that is not really there. Returns the
// config content and the host it was read from.
func (rm *ResourceManager) readForeignResourceConfig(ctx context.Context, hosts []string, name string) (string, string, error) {
	cmd := fmt.Sprintf("cat /etc/drbd.d/%s.res 2>/dev/null || true", name)
	var lastErr error
	for _, host := range hosts {
		result, err := rm.deployment.Exec(ctx, []string{host}, cmd)
		if err != nil {
			lastErr = err
			continue
		}
		var content string
		for _, hr := range result.Hosts {
			if hr != nil && hr.Success {
				content = hr.Output
				break
			}
		}
		if strings.Contains(content, "resource "+name) ||
			(strings.Contains(content, "resource ") && strings.Contains(content, "on ")) {
			return content, host, nil
		}
	}
	if lastErr != nil {
		return "", "", fmt.Errorf("read DRBD config for %q: %w", name, lastErr)
	}
	return "", "", fmt.Errorf("DRBD resource %q not found: no /etc/drbd.d/%s.res on any reachable node — refusing to adopt a resource that does not exist", name, name)
}

// onNodeRe matches a DRBD `on <node> {` section header.
var onNodeRe = regexp.MustCompile(`(?m)^\s*on\s+(\S+)\s*\{`)

// parseResourceConfigNodes extracts the participating node names (the `on
// <node> {` sections) from a DRBD .res file, in file order and de-duplicated.
func parseResourceConfigNodes(content string) []string {
	var nodes []string
	seen := make(map[string]bool)
	for _, m := range onNodeRe.FindAllStringSubmatch(content, -1) {
		n := m[1]
		if n == "" || seen[n] {
			continue
		}
		seen[n] = true
		nodes = append(nodes, n)
	}
	return nodes
}

// parsePortFromConfig extracts the DRBD port from the first `address ...:<port>;`
// line in a .res file, reusing portLineRe (`:(\d+);`). Returns 0 when absent.
func parsePortFromConfig(content string) uint32 {
	for _, line := range strings.Split(content, "\n") {
		if !strings.Contains(line, "address") {
			continue
		}
		if m := portLineRe.FindStringSubmatch(line); m != nil {
			if p, err := strconv.Atoi(m[1]); err == nil {
				return uint32(p)
			}
		}
	}
	return 0
}

// volumeNameAndPoolFromDiskPath best-effort derives (volumeName, pool) from a
// DRBD backing-disk path: "/dev/<pool>/<lv>" (LVM) or
// "/dev/zvol/<pool>/<dataset>" (ZFS). Returns empty strings when the path does
// not carry that information.
func volumeNameAndPoolFromDiskPath(diskPath string) (volumeName, pool string) {
	diskPath = strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(diskPath), ";"))
	if diskPath == "" {
		return "", ""
	}
	if strings.HasPrefix(diskPath, "/dev/zvol/") {
		rest := strings.TrimPrefix(diskPath, "/dev/zvol/")
		if parts := strings.SplitN(rest, "/", 2); len(parts) == 2 {
			return parts[1], parts[0]
		}
		return rest, ""
	}
	if strings.HasPrefix(diskPath, "/dev/") {
		parts := strings.Split(strings.TrimPrefix(diskPath, "/dev/"), "/")
		if len(parts) >= 2 {
			return parts[len(parts)-1], parts[len(parts)-2]
		}
		return parts[len(parts)-1], ""
	}
	return filepath.Base(diskPath), ""
}

// CreateResourceWithVolumes creates a DRBD resource with one or more volumes
// (volume 0..N) atomically across the given nodes. All volumes share the
// resource's storage type; each may target its own pool.
func (rm *ResourceManager) CreateResourceWithVolumes(ctx context.Context, name string, port uint32, nodes []string, protocol string, storageType string, drbdOptions map[string]string, volumes []VolumeSpec, wan *WANSpec) error {
	return rm.CreateResourceWithVolumesMetadata(ctx, name, port, nodes, protocol, storageType, drbdOptions, volumes, wan, ResourceMetadata{})
}

// CreateResourceWithVolumesMetadata creates a resource and persists its
// organizational metadata with the resolved resource configuration.
func (rm *ResourceManager) CreateResourceWithVolumesMetadata(ctx context.Context, name string, port uint32, nodes []string, protocol string, storageType string, drbdOptions map[string]string, volumes []VolumeSpec, wan *WANSpec, metadata ResourceMetadata) error {
	if rm.deployment == nil {
		return fmt.Errorf("deployment client not set")
	}
	if len(volumes) == 0 {
		return fmt.Errorf("at least one volume is required")
	}

	if storageType == "" {
		storageType = "lvm"
	}
	if protocol == "" {
		protocol = "C"
	}

	// WAN mode is opt-in and strictly gated: when wan == nil the entire block
	// below is skipped and the LAN path stays byte-for-byte unchanged. When set,
	// the resource becomes a two-endpoint (primary + DR) async replica routed
	// through a per-resource sds-proxy pair.
	var wanCfg *wanConfig
	if wan != nil {
		// The primary site may hold several synchronous replicas — the
		// "两地三中心" shape: protocol C inside the production site, one
		// asynchronous copy far away. Only the legs that cross the WAN go
		// through sds-proxy.
		primaries := make([]string, 0, len(nodes))
		seen := make(map[string]bool, len(nodes))
		for _, n := range nodes {
			p := strings.TrimSpace(n)
			if p == "" {
				continue
			}
			if seen[p] {
				return fmt.Errorf("WAN resource %q lists primary node %q twice", name, p)
			}
			seen[p] = true
			primaries = append(primaries, p)
		}
		if len(primaries) == 0 {
			return fmt.Errorf("WAN resource %q requires at least one primary-site node in --nodes", name)
		}

		drNode := strings.TrimSpace(wan.DRNode)
		if drNode == "" {
			return fmt.Errorf("WAN resource %q requires a DR node (--dr-node)", name)
		}
		if seen[drNode] {
			return fmt.Errorf("WAN DR node %q must not also be a primary-site node of %q", drNode, name)
		}
		if rm.controller.nodes.GetNodeAddressByName(drNode) == "" {
			return fmt.Errorf("WAN DR node %q is not a registered node", drNode)
		}
		if strings.TrimSpace(wan.DREndpoint) == "" {
			return fmt.Errorf("WAN resource %q requires a DR endpoint (--dr-endpoint)", name)
		}
		// WAN legs are systemd instances named "<resource>_<node>". A resource
		// named like one of those would be indistinguishable from another
		// resource's leg, and leg reconciliation would treat one's tunnels as
		// the other's litter. Refusing the name is cheaper than disambiguating
		// it forever.
		if rm.controller.db != nil {
			if existing, lerr := rm.controller.db.ListResources(ctx); lerr == nil {
				names := make([]string, 0, len(existing))
				for _, e := range existing {
					names = append(names, e.Name)
				}
				if verr := wanproxy.ValidateResourceNameForWAN(name, names); verr != nil {
					return verr
				}
			}
		}
		if wan.WANPort == 0 {
			wan.WANPort = randomWANPort()
			rm.controller.logger.Info("auto-allocated WAN proxy port",
				zap.String("resource", name), zap.Uint32("wan_port", wan.WANPort))
		}

		// Protocol: a single-replica primary site is the historic two-endpoint
		// WAN resource and is async end to end. With several replicas the LAN
		// mesh stays synchronous (that is the point of having them) and only
		// the WAN legs are async — expressed per connection, below.
		if len(primaries) == 1 {
			protocol = "A"
		}
		nodes = append(append([]string{}, primaries...), drNode)
		wanCfg = &wanConfig{DRNode: drNode, PrimaryNodes: primaries}

		// Each primary's DRBD needs a loopback port to bind for its WAN leg that
		// nothing else on that host has claimed; a fixed offset collides with
		// another resource whose DRBD port happens to sit one offset away.
		if len(primaries) > 1 && rm.deployment != nil {
			primaryAddrs := make([]string, 0, len(primaries))
			for _, n := range primaries {
				primaryAddrs = append(primaryAddrs, rm.controller.ResolveHost(n))
			}
			bindPorts, perr := rm.pickWANBindPorts(ctx, primaryAddrs, int(port))
			if perr != nil {
				return fmt.Errorf("choose WAN bind ports: %w", perr)
			}
			wanCfg.BindPorts = bindPorts
		}
	}

	// Resolve every volume: auto-select+normalize its pool and derive a backing
	// volume name. Volume 0 keeps the historical "<name>_data" name (so existing
	// resources and callers are unaffected); extra volumes use "<name>_vol<K>".
	resolved := make([]resolvedVolume, len(volumes))
	for i, v := range volumes {
		if v.SizeGB == 0 {
			return fmt.Errorf("volume %d: size must be greater than 0 GB", i)
		}
		pool := v.Pool
		if pool == "" {
			// Auto-select the pool when none was given: with exactly one
			// registered pool name the choice is unambiguous; otherwise the
			// caller must decide.
			selected, err := rm.autoSelectPool(ctx)
			if err != nil {
				return err
			}
			pool = selected
		}
		volumeName := fmt.Sprintf("%s_data", name)
		if i > 0 {
			volumeName = fmt.Sprintf("%s_vol%d", name, i)
		}
		resolved[i] = resolvedVolume{
			id:         i,
			volumeName: volumeName,
			pool:       normalizeManagedName(pool),
			sizeGB:     v.SizeGB,
			encrypted:  metadata.Encrypt,
		}
		if metadata.Encrypt {
			if err := validateLUKSNames(resolved[i].pool, volumeName); err != nil {
				return err
			}
		}
	}

	// Encryption is decided here and nowhere else, so the refusal to change it
	// belongs before anything is built. A name that already carries a resource
	// with the other setting is a request to convert in place, which this does
	// not do.
	if err := rm.assertEncryptionNotRetrofitted(ctx, name, metadata.Encrypt); err != nil {
		return err
	}
	if metadata.Encrypt {
		if err := assertEncryptableStorage(storageType); err != nil {
			return err
		}
	}

	rm.controller.logger.Info("Creating DRBD resource",
		zap.String("name", name),
		zap.Uint32("port", port),
		zap.Strings("nodes", nodes),
		zap.String("protocol", protocol),
		zap.Int("volumes", len(resolved)),
		zap.String("storage_type", storageType),
		zap.Bool("encrypted", metadata.Encrypt),
		zap.Any("options", drbdOptions))

	// Quorum tiebreaker: a 2-node resource under quorum=majority loses its
	// majority the moment either node fails (the survivor is only 1/2), so
	// DRBD suspends I/O. Add a third diskless node — it votes in quorum but
	// stores no data — when one is available, so the survivor keeps a 2/3
	// majority through any single-node failure. Mirrors LINSTOR's
	// auto-add-quorum-tiebreaker. If no spare node exists we proceed with a
	// bare 2-node resource but flag the quorum risk loudly.
	// WAN resources are strictly two-endpoint (primary + DR); a diskless
	// tiebreaker would need a third mesh connection the sds-proxy pair does not
	// carry, so the auto-tiebreaker is skipped entirely for WAN.
	var disklessNodes []string
	if len(nodes) == 2 && wan == nil {
		if rm.controller.config != nil && rm.controller.config.Resource.AutoTiebreaker {
			if tb := rm.selectTiebreaker(ctx, nodes); tb != "" {
				disklessNodes = []string{tb}
				rm.controller.logger.Info("Adding diskless quorum tiebreaker to 2-node resource",
					zap.String("resource", name),
					zap.String("tiebreaker", tb))
			}
		}
		if len(disklessNodes) == 0 {
			rm.controller.logger.Warn("2-node resource has no quorum tiebreaker: a single node failure will suspend I/O (no quorum majority). Register a third node, or it stays a degraded 2-node resource.",
				zap.String("resource", name))
		}
	}

	// Convert diskful node names to IP addresses for deployment.
	nodeIPs := make([]string, len(nodes))
	for i, node := range nodes {
		ip := rm.controller.nodes.GetNodeAddressByName(node)
		if ip == "" {
			ip = node // fallback to node name
		}
		nodeIPs[i] = ip
	}

	// Diskless tiebreaker IPs, and the union of all participating node IPs.
	// Config, `drbdadm up` and teardown reach every node; LV creation and
	// create-md touch diskful nodes only.
	disklessIPs := make([]string, len(disklessNodes))
	for i, node := range disklessNodes {
		ip := rm.controller.nodes.GetNodeAddressByName(node)
		if ip == "" {
			ip = node
		}
		disklessIPs[i] = ip
	}
	allIPs := append(append([]string{}, nodeIPs...), disklessIPs...)

	if port == 0 {
		p, err := rm.nextGlobalPort(ctx, nodeIPs[0])
		if err != nil {
			return fmt.Errorf("allocate port: %w", err)
		}
		port = p
		rm.controller.logger.Info("auto-allocated DRBD port", zap.Uint32("port", port), zap.String("resource", name))
	}

	// Pre-flight: reject a port already bound by another DRBD resource on the
	// nodes (including ones SDS does not manage) with a clear message, rather
	// than letting `drbdadm create-md` fail later with an opaque error.
	if conflict, err := rm.findPortConflict(ctx, nodeIPs[0], port, name); err != nil {
		rm.controller.logger.Warn("port conflict pre-check failed; continuing",
			zap.Uint32("port", port), zap.Error(err))
	} else if conflict != "" {
		return fmt.Errorf("DRBD port %d is already in use by resource %q; choose a different port", port, conflict)
	}

	// Pre-flight the crypt layer on every diskful node before the first volume
	// is made. Discovering a node without cryptsetup halfway through would
	// leave encrypted volumes on the nodes already visited and a rollback to
	// unwind, for a request that could never have succeeded.
	if metadata.Encrypt {
		if err := rm.assertEncryptionSupported(ctx, nodeIPs, nodes); err != nil {
			return err
		}
	}

	// Roll back partial state if a later step fails: a half-created resource
	// (e.g. LVs made but create-md failed) otherwise leaves orphaned backing
	// volumes and a stray .res that block a clean retry.
	committed := false
	defer func() {
		if committed {
			return
		}
		rm.controller.logger.Warn("Resource create failed; rolling back partial state",
			zap.String("name", name))
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer cancel()
		_, _ = rm.deployment.DRBDDown(cleanupCtx, allIPs, name)
		_, _ = rm.deployment.Exec(cleanupCtx, allIPs, fmt.Sprintf("sudo rm -f /etc/drbd.d/%s.res", name))
		// A WAN create may have provisioned the sds-proxy pair before failing;
		// tear it down too so a retry starts clean. Best-effort (idempotent at
		// the shell level). nodeIPs is [primaryIP, drIP] for a WAN resource.
		if wan != nil && len(nodeIPs) >= 2 {
			_ = wanproxy.DeprovisionMulti(cleanupCtx, rm.wanproxyDeployClient(), wanproxy.MultiSpec{
				Resource:         name,
				PrimaryNodeAddrs: nodeIPs[:len(nodeIPs)-1],
				DRNodeAddr:       nodeIPs[len(nodeIPs)-1],
				BaseWANPort:      int(wan.WANPort),
				BaseDRBDPort:     int(port),
			})
		}
		// Backing volumes exist on diskful nodes only. An encrypted volume's
		// container has to be closed and its key destroyed first: while the
		// container is open the LV is held and lvremove refuses, and a key left
		// behind after a failed create is a secret nothing will ever collect.
		for _, v := range resolved {
			if v.encrypted {
				rm.closeBackingVolumeOn(cleanupCtx, nodeIPs, v.pool, v.volumeName)
			}
			if storageType == "zfs" || storageType == "zfs-thin" {
				_, _ = rm.deployment.ZFSDestroyDataset(cleanupCtx, nodeIPs, fmt.Sprintf("%s/%s", v.pool, v.volumeName))
			} else {
				_, _ = rm.deployment.LVRemove(cleanupCtx, nodeIPs, fmt.Sprintf("/dev/%s/%s", v.pool, v.volumeName))
			}
		}
	}()

	// 1. Create the backing storage for every volume on all diskful nodes.
	for _, v := range resolved {
		if err := rm.createBackingVolume(ctx, nodeIPs, nodes, storageType, v.pool, v.volumeName, v.sizeGB, v.encrypted); err != nil {
			return err
		}
	}

	// 2. Generate DRBD config.
	// Allocate node-global device minors: minors are shared across every DRBD
	// resource on a node. nextGlobalMinor returns (max existing minor)+1, so a
	// run of len(resolved) consecutive minors from that base is collision-free.
	baseMinor, err := rm.nextGlobalMinor(ctx, allIPs)
	if err != nil {
		return fmt.Errorf("failed to allocate device minor: %w", err)
	}
	for i := range resolved {
		resolved[i].minor = baseMinor + i
	}
	// wanCfg is nil for a LAN resource (output unchanged); non-nil renders the
	// WAN variant (protocol A + pull-ahead + loopback addresses).
	drbdConfig := rm.generateDrbdConfig(name, port, resolved, nodes, disklessNodes, protocol, storageType, drbdOptions, wanCfg)

	// 3. Distribute config to all nodes (diskful + diskless tiebreaker)
	configResult, err := rm.deployment.DistributeConfig(ctx, allIPs, drbdConfig, fmt.Sprintf("/etc/drbd.d/%s.res", name))
	if err != nil {
		return fmt.Errorf("failed to distribute config: %w", err)
	}
	if !configResult.Success {
		return fmt.Errorf("config distribution failed: %s", configResult.FailureDetails())
	}

	// 4. Create metadata on diskful nodes only. A diskless tiebreaker has no
	// backing disk, so `drbdadm create-md` does not apply to it.
	mdResult, err := rm.deployment.DRBDCreateMD(ctx, nodeIPs, name, minMetadataPeers)
	if err != nil {
		return fmt.Errorf("failed to create metadata: %w", err)
	}
	if !mdResult.AllSuccess() {
		return fmt.Errorf("metadata creation failed: %s", mdResult.FailureDetails())
	}

	// 4a. WAN only: bring up the per-resource sds-proxy pair BEFORE `drbdadm up`.
	// In WAN mode DRBD connects to 127.0.0.1:<port> (the local proxy), so the
	// loopback proxy must be listening first — otherwise the resource comes up
	// with nothing to connect to. The rollback defer deprovisions on any later
	// failure. This step is entirely gated behind wan != nil.
	if wan != nil {
		// nodes is [primaries..., DR]; the DR is always last (set when the WAN
		// spec was validated), so the primary addresses are everything before it.
		primaryAddrs := nodeIPs[:len(nodeIPs)-1]
		drAddr := nodeIPs[len(nodeIPs)-1]

		multi := wanproxy.MultiSpec{
			Resource:         name,
			PrimaryNodeAddrs: primaryAddrs,
			// Names, not addresses, decide what each leg is called: a node that
			// is later renumbered keeps its name, and so keeps its leg.
			PrimaryNodeKeys:   nodes[:len(nodes)-1],
			DRNodeAddr:        drAddr,
			DRPublicEndpoint:  wan.DREndpoint,
			BaseWANPort:       int(wan.WANPort),
			BaseDRBDPort:      int(port),
			PrimaryEgressAddr: wan.EgressAddress,
			BinaryFor:         rm.wanproxyBinaryResolver(ctx, append(append([]string{}, primaryAddrs...), drAddr)),
		}
		rm.controller.logger.Info("Provisioning WAN replication proxy before DRBD up",
			zap.String("resource", name),
			zap.Strings("primaries", primaryAddrs),
			zap.String("dr", drAddr),
			zap.String("dr_endpoint", wan.DREndpoint),
			zap.Int("base_wan_port", int(wan.WANPort)))
		if err := wanproxy.ProvisionMulti(ctx, rm.wanproxyDeployClient(), multi); err != nil {
			return fmt.Errorf("provision WAN proxy for %s: %w", name, err)
		}
	}

	// 5. Bring up resource on all nodes. The diskless node comes up Diskless
	// and connects; it only participates in quorum.
	upResult, err := rm.deployment.DRBDUp(ctx, allIPs, name)
	if err != nil {
		return fmt.Errorf("failed to bring up resource: %w", err)
	}
	if !upResult.AllSuccess() {
		return fmt.Errorf("resource up failed: %s", upResult.FailureDetails())
	}

	// 5a. Establish the initial UpToDate generation. A freshly created resource
	// comes up Inconsistent on EVERY volume of EVERY node with no UpToDate copy
	// anywhere, so it cannot be promoted (a normal `drbdsetup primary` fails with
	// "Need access to UpToDate data", exit 17) and it never resyncs — there is no
	// sync source. Force-promote the first diskful node once, then demote it back
	// to Secondary: that marks its volumes UpToDate and gives peers a source to
	// sync from, leaving the resource in a neutral Secondary+UpToDate state.
	//
	// Doing it HERE (before any gateway state volume is added) is what makes the
	// later gateway promote a plain non-forced promote: otherwise the auto-added
	// state volume becomes UpToDate on its own while the data volume stays
	// Inconsistent, and the promote fails on the data volume. This mirrors the
	// initial force the CSI/filesystem path already performs. It is safe because
	// create-md (step 4) just wiped all metadata: every replica is Inconsistent,
	// so there is no data anywhere to lose. This path only ever runs for a
	// brand-new resource — adopting an existing resource goes through
	// AdoptResource, which never reaches here.
	//
	// On thin storage there is nothing to sync; see initial_sync.go.
	skipped := false
	if rm.backedByZeroReadingStorage(ctx, storageType, nodeIPs, resolved) {
		if err := rm.skipInitialSync(ctx, name, nodeIPs[0]); err != nil {
			rm.controller.logger.Warn("Could not skip the initial sync; running a full one",
				zap.String("resource", name), zap.Error(err))
		} else {
			skipped = true
		}
	}
	if !skipped {
		if err := rm.establishInitialSync(ctx, name, nodeIPs[0]); err != nil {
			return fmt.Errorf("failed to establish initial sync for %s: %w", name, err)
		}
	}

	// 5a. WAN only: now that the resource exists and its peers are configured,
	// narrow quorum to the primary site so the DR does not get a vote on whether
	// home may write. This could not be done in the generated config — a numeric
	// quorum would have blocked the force-promote above. Best effort: a resource
	// that is otherwise created should not be failed for a tuning step, and the
	// setting can be applied later with `resource set-options`.
	if wan != nil {
		// allIPs is every participant; nodeIPs is the diskful ones, the DR last.
		localVoters := localSiteVoters(len(allIPs))
		if err := rm.applyLocalSiteQuorum(ctx, name, allIPs, localVoters); err != nil {
			rm.controller.logger.Warn("Could not narrow quorum to the primary site; the DR still votes",
				zap.String("resource", name), zap.Error(err))
		}
	}

	// 5b. Ensure a DRBD boot unit is installed and enabled on every
	// participating node so a rebooted node re-runs `drbdadm adjust all` on boot
	// and auto-rejoins replication without a manual `drbdadm adjust`. The
	// packaged drbd.service is an LSB/SysV unit whose Default-Start header is
	// empty, so `systemctl enable drbd.service` fails ("Default-Start contains
	// no runlevels") and it can never be enabled. Instead we install our own
	// native systemd oneshot (sds-drbd-up.service) that runs `drbdadm adjust all`
	// before drbd-reactor, letting the reactor promote once resources are up.
	// Installing/enabling is idempotent and harmless on any node with
	// drbd-utils (diskful or diskless tiebreaker). Best-effort: never fail
	// resource creation just because it did not stick — the resource is
	// already up at this point.
	//
	// Except when the resource is encrypted. That same unit is what opens the
	// crypt containers at boot, so without it a rebooted node comes back with
	// no backing device and the replica attaches Diskless — silently, and only
	// discovered the next time the node restarts, which is the worst possible
	// moment to find out. A resource that cannot survive a reboot is not one to
	// hand back as created.
	if bootErr := rm.ensureDRBDBootUnitEnabled(ctx, allIPs); bootErr != nil && metadata.Encrypt {
		return fmt.Errorf("install the boot unit that opens the LUKS containers: %w", bootErr)
	}

	// 6. Save to database
	if rm.controller.db != nil {
		dbRes := &database.Resource{
			Name:          name,
			Port:          int(port),
			Nodes:         strings.Join(nodes, ","),
			Protocol:      protocol,
			Replicas:      len(nodes),
			DisklessNodes: strings.Join(disklessNodes, ","),
			Labels:        cloneStringMap(metadata.Labels),
			Profile:       metadata.Profile,
			Encrypted:     metadata.Encrypt,
		}
		// Persist WAN metadata so DeleteResource can deprovision the proxy pair
		// and the UI/CLI can show the resource is WAN-replicated.
		if wan != nil {
			dbRes.WANMode = true
			dbRes.DRNode = wan.DRNode
			dbRes.DREndpoint = wan.DREndpoint
			dbRes.WANPort = int(wan.WANPort)
			dbRes.WANEgressAddress = wan.EgressAddress
		}
		if err := rm.controller.db.SaveResource(ctx, dbRes); err != nil {
			rm.controller.logger.Warn("Failed to save resource to database", zap.Error(err))
		}

		for _, v := range resolved {
			volumeRecord := &database.Volume{
				ResourceName: name,
				VolumeName:   v.volumeName,
				VolumeID:     v.id,
				Pool:         v.pool,
				SizeGB:       int(v.sizeGB),
				// The device DRBD actually consumes, crypt container included.
				// Teardown and resize read this back to decide what they are
				// dealing with, exactly as they already do for a zvol.
				Device: v.backingDevice(storageType),
			}
			if err := rm.controller.db.SaveVolume(ctx, volumeRecord); err != nil {
				rm.controller.logger.Warn("Failed to save volume to database",
					zap.String("resource", name),
					zap.Int("volume", v.id),
					zap.Error(err))
			}
		}
	}

	rm.controller.logger.Info("DRBD resource created successfully",
		zap.String("name", name))

	committed = true
	return nil
}

// drbdBootUnit is the name of the native systemd oneshot we install to bring
// all DRBD resources up at boot. We deliberately do NOT reuse the packaged
// drbd.service: on Ubuntu 24.04 / DRBD 9 that is an LSB/SysV init script whose
// Default-Start header is empty, so `systemctl enable drbd.service` fails with
// "Default-Start contains no runlevels, aborting" and the unit can never be
// enabled. Our own native unit sidesteps that entirely.
const drbdBootUnit = "sds-drbd-up.service"

// drbdBootUnitPath is where the generated unit is written on each node.
const drbdBootUnitPath = "/etc/systemd/system/sds-drbd-up.service"

// drbdBootScriptPath is the helper script ExecStart runs. Keeping the logic in
// a script (rather than an inline ExecStart) lets it activate LVM first and
// reconcile each resource tolerantly at boot.
const drbdBootScriptPath = "/usr/local/sbin/sds-drbd-up.sh"

// drbdBootUnitInstallCmd renders the single shell command that (idempotently)
// installs and enables the DRBD boot bring-up on a node: a helper script plus a
// oneshot systemd unit that runs it before drbd-reactor. The script:
//   - activates LVM volume groups (`vgchange -ay`) so DRBD backing devices
//     exist before attach — otherwise a resource comes up Diskless because its
//     backing LV was not yet active at boot;
//   - opens any LUKS containers SDS has registered on the node, between those
//     two steps: an encrypted resource's backing device is the crypt mapping,
//     which cannot exist before the LV does and must exist before DRBD
//     attaches. Doing it inside this one script is what makes that ordering
//     certain — as three separate units it would depend on cryptsetup.target
//     landing after an LVM activation this script does not trust in the first
//     place;
//   - adjusts EACH resource independently with `|| true`, so a foreign resource
//     already brought up by its own drbd-reactor promoter (which fails with
//     "minor exists" / exit 10) can neither abort the remaining resources nor
//     fail the unit. `drbdadm` is discovered inside the script at runtime.
func drbdBootUnitInstallCmd() string {
	return `set -e
sudo tee ` + drbdBootScriptPath + ` > /dev/null <<'EOSCRIPT'
#!/bin/sh
PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/sbin:/usr/bin:/bin
export PATH
DRBDADM="$(command -v drbdadm 2>/dev/null || true)"
[ -n "$DRBDADM" ] || DRBDADM=/usr/sbin/drbdadm
# 1. Activate LVM so DRBD backing devices exist before we attach them.
vgchange -ay >/dev/null 2>&1 || true
udevadm settle >/dev/null 2>&1 || true
` + luksBootOpenSnippet() + `
# 2. Reconcile each resource independently; tolerate ones already up (a foreign
#    reactor-managed resource yields "minor exists" / exit 10).
for res in $("$DRBDADM" sh-resources 2>/dev/null); do
  "$DRBDADM" adjust "$res" >/dev/null 2>&1 || true
done
exit 0
EOSCRIPT
sudo chmod +x ` + drbdBootScriptPath + `
sudo tee ` + drbdBootUnitPath + ` > /dev/null <<'EOF'
[Unit]
Description=Bring up all SDS DRBD resources at boot
After=network-online.target lvm2-monitor.service local-fs.target
Wants=network-online.target
Before=drbd-reactor.service

[Service]
Type=oneshot
RemainAfterExit=yes
ExecStart=` + drbdBootScriptPath + `

[Install]
WantedBy=multi-user.target
EOF
sudo systemctl daemon-reload
sudo systemctl enable ` + drbdBootUnit
}

// ensureDRBDBootUnitEnabled installs and enables the DRBD boot unit on the
// given nodes so their resources auto-come-up (`drbdadm adjust all`) after a
// reboot and re-sync without manual intervention; drbd-reactor then promotes
// once the resources are up. `adjust all` (not `up all`) is used deliberately:
// it is idempotent and reconciles config->running state, so it attaches backing
// disks AND tolerates a resource that is already up (e.g. a non-sds DRBD
// resource on the same node) instead of aborting with a "minor exists" error
// and leaving later resources half-up (Diskless). Writing the same unit file and
// re-enabling it are idempotent, so repeated calls across resource creations are
// safe.
//
// The failure is logged here and also returned, because how much it matters
// depends on the caller. For a plaintext resource it is cosmetic — the resource
// is already up, and a missing boot unit costs one `drbdadm adjust` after a
// reboot. For an encrypted one this same unit is what opens the crypt
// containers, so its absence means the node comes back with no backing device
// at all; that caller treats the error as fatal.
func (rm *ResourceManager) ensureDRBDBootUnitEnabled(ctx context.Context, hosts []string) error {
	if rm.deployment == nil || len(hosts) == 0 {
		return nil
	}
	result, err := rm.deployment.Exec(ctx, hosts, drbdBootUnitInstallCmd())
	if err != nil {
		rm.controller.logger.Warn("Failed to install DRBD boot unit; nodes may not auto-up resources after reboot",
			zap.String("unit", drbdBootUnit),
			zap.Strings("hosts", hosts),
			zap.Error(err))
		return fmt.Errorf("install %s on %v: %w", drbdBootUnit, hosts, err)
	}
	if !result.AllSuccess() {
		rm.controller.logger.Warn("Failed to install DRBD boot unit on some hosts; those nodes may not auto-up resources after reboot",
			zap.String("unit", drbdBootUnit),
			zap.Strings("failed_hosts", result.FailedHosts()))
		return fmt.Errorf("install %s on %v: %s", drbdBootUnit, result.FailedHosts(), result.FailureDetails())
	}
	rm.controller.logger.Info("Installed and enabled DRBD boot unit for reboot auto-recovery",
		zap.String("unit", drbdBootUnit),
		zap.Strings("hosts", hosts))
	return nil
}

// drbdMetadataBytes returns the space DRBD's INTERNAL metadata takes off the end
// of a backing volume, so a caller can add it on top of the size the user asked
// for. Internal metadata lives on the same device as the data, so without this
// allowance a "4 GiB" volume exports a device slightly SMALLER than 4 GiB.
//
// DRBD's layout is a fixed superblock/activity-log area plus one bitmap per
// peer, at one bit per 4 KiB of data:
//
//	metadata = 36 KiB + (dataBytes / 32768) * peers
//
// Verified against a live 4 GiB 3-node resource: 36 KiB + 2 x 128 KiB = 292 KiB,
// matching the measured shortfall exactly.
//
// peers is sized generously (see minMetadataPeers) because bitmap slots are
// fixed at create-md time, while diskless clients may attach later.
func drbdMetadataBytes(dataBytes uint64, peers int) uint64 {
	if peers < minMetadataPeers {
		peers = minMetadataPeers
	}
	const (
		fixedOverhead = 36 * 1024 // superblock + activity log
		bitmapDivisor = 32768     // 1 bit per 4 KiB of data
	)
	meta := uint64(fixedOverhead) + (dataBytes/bitmapDivisor)*uint64(peers)

	// Round up to a 1 MiB boundary. LVM allocates in extents (4 MiB by default)
	// anyway, so the rounding is free in practice and absorbs any difference
	// between DRBD versions rather than leaving the volume a few KiB short.
	const mib = 1024 * 1024
	return ((meta + mib - 1) / mib) * mib
}

// minMetadataPeers is the floor for the bitmap-slot count used when sizing
// metadata. DRBD fixes the number of bitmap slots when metadata is created, but
// diskless clients (Proxmox/CSI hosts) attach to a resource later — so the
// allowance assumes room to grow instead of exactly today's peer count. The
// cost of being wrong upward is a few MiB per volume; being wrong downward
// means the volume is short again.
const minMetadataPeers = 7

// backingVolumeSizeArg returns the size to hand lvcreate/zfs for one volume:
// the requested size plus DRBD's internal-metadata allowance, so the DRBD
// device the guest actually sees is at least as big as the user asked for.
func backingVolumeSizeArg(sizeGB uint32, peers int) string {
	return fmt.Sprintf("%dB", backingVolumeSizeBytes(sizeGB, peers, false))
}

// backingVolumeSizeBytes is the same calculation with the crypt layer folded
// in. A LUKS2 header sits at the FRONT of the device and is not part of the
// mapping DRBD sees, so an encrypted volume has to be that much larger for the
// DRBD device to come out the size that was asked for — the same shortfall
// drbdMetadataBytes exists to absorb, from the other end of the device.
func backingVolumeSizeBytes(sizeGB uint32, peers int, encrypted bool) uint64 {
	data := uint64(sizeGB) * 1024 * 1024 * 1024
	// LVM/ZFS accept byte suffixes; using bytes avoids rounding the allowance
	// away by expressing the total in whole gigabytes.
	total := data + drbdMetadataBytes(data, peers)
	if encrypted {
		total += luksHeaderBytes
	}
	return total
}

// createBackingVolume creates one volume's backing storage (ZFS zvol, LVM thin
// LV or plain LVM LV per storageType) on every diskful node. nodeIPs and nodes
// are parallel (IP for the command, name for error messages).
//
// It is IDEMPOTENT: a volume that already exists at a sufficient size counts as
// success. That matters because creation is not atomic — it walks the nodes one
// at a time, so a failure on the third node leaves volumes behind on the first
// two. Without this, the rollback's best-effort lvremove missing even one node
// would make every later attempt at the same name fail with "already exists",
// permanently blocking that volume (the same class of trap as a
// partially-applied resize).
//
// When encrypt is set each node's volume is wrapped in its own LUKS2 container
// and left open, so what the caller ends up with is /dev/mapper/<container>
// rather than the LV itself. The container is built per node, immediately after
// that node's volume, so a failure anywhere leaves at most one half-encrypted
// volume for the rollback to collect rather than a set of them.
func (rm *ResourceManager) createBackingVolume(ctx context.Context, nodeIPs, nodes []string, storageType, pool, volumeName string, sizeGB uint32, encrypt bool) error {
	size := fmt.Sprintf("%dB", backingVolumeSizeBytes(sizeGB, len(nodeIPs)-1, encrypt))
	for i, nodeIP := range nodeIPs {
		var result *deployment.ExecResult
		var err error
		switch storageType {
		case "zfs", "zfs-thin":
			result, err = rm.deployment.ZFSCreateThinDataset(ctx, []string{nodeIP}, pool, volumeName, size)
		default:
			// The shape comes from the node, not from storageType.
			//
			// --storage-type defaults to "lvm" and nothing reconciles it with the
			// pool it names, so a resource created in a thin pool without the flag
			// used to attempt a thick lvcreate in a volume group the thin pool had
			// already consumed. That fails on free space — an error naming the
			// symptom and not the cause — and it is unfixable by construction: a
			// full-VG thin pool leaves nothing for a thick LV to take. add-replica
			// and add-dr already ask the node (see createBackingVolumeOn); this is
			// the same question, so that a replica added later has the same shape
			// as the one creation made.
			//
			// The thin pool's name is asked for too, not assumed: `pool create`
			// builds "<pool>_thin" but converting a thick pool in place builds a
			// differently named one, and guessing fails on those nodes.
			thinPool, perr := rm.deployment.LVThinPoolIn(ctx, nodeIP, pool)
			if perr != nil {
				return fmt.Errorf("look for a thin pool in %s on %s: %w", pool, nodes[i], perr)
			}
			if thinPool == "" {
				if storageType == "lvm-thin" {
					return fmt.Errorf("pool %s on %s is recorded as lvm-thin but has no thin pool", pool, nodes[i])
				}
				result, err = rm.deployment.LVCreate(ctx, []string{nodeIP}, pool, volumeName, size)
				break
			}
			result, err = rm.deployment.LVCreateThinVolume(ctx, []string{nodeIP}, pool, thinPool, volumeName, size)
		}
		if err != nil {
			return fmt.Errorf("failed to create backing volume %s/%s on %s: %w", pool, volumeName, nodes[i], err)
		}
		if !result.AllSuccess() {
			for host, hres := range result.Hosts {
				if hres.Success {
					continue
				}
				// Tolerate a leftover from a previous failed attempt, but only
				// once we have confirmed it is actually big enough to hold what
				// was asked for — a stale SMALLER volume must still be an error
				// rather than silently handing the caller a short device.
				if storageType != "zfs" && storageType != "zfs-thin" &&
					strings.Contains(strings.ToLower(hres.Output), "already exists") {
					ok, verr := rm.backingVolumeAtLeast(ctx, host, pool, volumeName, sizeGB)
					if verr == nil && ok {
						rm.controller.logger.Info("Reusing existing backing volume from a previous attempt",
							zap.String("volume", pool+"/"+volumeName),
							zap.String("host", host))
						continue
					}
					return fmt.Errorf("backing volume %s/%s already exists on %s but is too small to reuse; remove it and retry",
						pool, volumeName, host)
				}
				return fmt.Errorf("backing volume %s/%s creation failed on %s: %s", pool, volumeName, host, hres.Output)
			}
		}
		if encrypt {
			if err := rm.encryptBackingVolumeOn(ctx, nodeIP, nodes[i], pool, volumeName,
				backingPathForVolume(pool, volumeName, storageType)); err != nil {
				return err
			}
		}
	}
	return nil
}

// backingVolumeAtLeast reports whether an existing logical volume is big enough
// to back a volume of sizeGB (data plus DRBD metadata).
func (rm *ResourceManager) backingVolumeAtLeast(ctx context.Context, host, pool, volumeName string, sizeGB uint32) (bool, error) {
	cmd := fmt.Sprintf("sudo lvs --noheadings --nosuffix --units b -o lv_size %s/%s", pool, volumeName)
	res, err := rm.deployment.Exec(ctx, []string{host}, cmd)
	if err != nil {
		return false, err
	}
	hres := res.Hosts[host]
	if hres == nil || !hres.Success {
		return false, fmt.Errorf("could not read size of %s/%s on %s", pool, volumeName, host)
	}
	have, perr := strconv.ParseUint(strings.TrimSpace(hres.Output), 10, 64)
	if perr != nil {
		return false, perr
	}
	return have >= uint64(sizeGB)*1024*1024*1024, nil
}

// selectTiebreaker picks a registered node, not already part of the resource,
// TiebreakerLabel opts a node out of automatic diskless-tiebreaker selection
// when set to "false" (`sds node label <node> sds.tiebreaker=false`). Use it on
// WAN/DR nodes, which cannot join a LAN resource's DRBD connection mesh.
const TiebreakerLabel = "sds.tiebreaker"

// to serve as a diskless quorum tiebreaker. Selection prefers, in order:
// online storage nodes, online compute-only nodes, then offline nodes; within
// each tier it is deterministic (lowest node name) so repeated creations are
// stable. Returns "" when no spare node is available — the caller then keeps
// the resource as a bare 2-node configuration.
//
// The storage-node preference matters in a mixed cluster: a hypervisor
// registered only to attach volumes as a diskless client (e.g. a Proxmox node)
// has no storage pool of its own. Dragging such a compute-only node into every
// 2-replica resource's quorum mesh is wrong — it should stay a pure client — so
// a real storage node is chosen for the tiebreaker whenever one is free.
func (rm *ResourceManager) selectTiebreaker(ctx context.Context, nodes []string) string {
	inUse := make(map[string]bool, len(nodes))
	for _, n := range nodes {
		inUse[n] = true
	}

	all, err := rm.controller.nodes.ListNodes(ctx)
	if err != nil {
		rm.controller.logger.Warn("Failed to list nodes for tiebreaker selection", zap.Error(err))
		return ""
	}

	hasPool := rm.storageNodeSet(ctx)

	// Four tiers, most-preferred first. Within a tier, lowest name wins.
	var onlineStorage, onlineCompute, offlineStorage, offlineCompute []string
	for _, n := range all {
		if n == nil || inUse[n.Name] {
			continue
		}
		// A tiebreaker joins the resource's DRBD connection mesh, so it must sit
		// on the replication network like any other peer. A remote/DR node does
		// not: it is reached over a WAN and, on a cloud instance, its public
		// address is not even configured on an interface, so `drbdadm up` fails
		// with "IP <addr> not found on this host" — after the volumes exist.
		// Nodes labelled sds.tiebreaker=false are therefore never auto-selected;
		// label DR sites that way. Same spirit as the compute-only rule below.
		if strings.EqualFold(n.Labels[TiebreakerLabel], "false") {
			rm.controller.logger.Debug("Skipping tiebreaker candidate opted out by label",
				zap.String("node", n.Name), zap.String("label", TiebreakerLabel))
			continue
		}
		storage := hasPool[n.Name]
		switch {
		case n.State == NodeStateOnline && storage:
			onlineStorage = append(onlineStorage, n.Name)
		case n.State == NodeStateOnline:
			onlineCompute = append(onlineCompute, n.Name)
		case storage:
			offlineStorage = append(offlineStorage, n.Name)
		default:
			offlineCompute = append(offlineCompute, n.Name)
		}
	}

	for _, tier := range [][]string{onlineStorage, onlineCompute, offlineStorage, offlineCompute} {
		if len(tier) > 0 {
			sort.Strings(tier)
			return tier[0]
		}
	}
	return ""
}

// storageNodeSet returns the set of node names that host at least one storage
// pool, i.e. real storage nodes as opposed to compute-only clients. It uses the
// StorageManager's authoritative pool view (live discovery with a persisted
// fallback), because pools are not always mirrored into the resource DB — a
// direct db.ListPools can come back empty even when nodes clearly have pools.
// Pools record their node as either a name or an address, so both forms are
// mapped back to the node name. An empty set (no storage manager, or no pools
// anywhere) makes selectTiebreaker fall back to name order across all
// candidates — the pre-existing behavior.
func (rm *ResourceManager) storageNodeSet(ctx context.Context) map[string]bool {
	set := make(map[string]bool)
	if rm.controller.storage == nil {
		return set
	}
	pools, err := rm.controller.storage.ListPools(ctx)
	if err != nil {
		rm.controller.logger.Warn("Failed to list pools for tiebreaker selection", zap.Error(err))
		return set
	}
	for _, p := range pools {
		if p == nil || strings.TrimSpace(p.Node) == "" {
			continue
		}
		// p.Node may be a name or an address; record the canonical node name.
		if name := rm.controller.nodes.GetNodeNameByAddress(p.Node); name != "" {
			set[name] = true
		} else {
			set[p.Node] = true
		}
	}
	return set
}

// resolveToIP resolves a hostname to an IP address. If the input is already
// an IP address, it returns it unchanged. If resolution fails or returns a
// loopback address, it tries to find a non-loopback IP from network interfaces.
func resolveToIP(host string) string {
	// Check if it's already an IP address
	if ip := net.ParseIP(host); ip != nil {
		if !ip.IsLoopback() {
			return host
		}
		// If it's a loopback IP, try to find a real IP
		return getFirstNonLoopbackIP()
	}

	// Try to resolve hostname to IP
	addrs, err := net.LookupHost(host)
	if err != nil || len(addrs) == 0 {
		return getFirstNonLoopbackIP()
	}

	// Prefer non-loopback IPv4 addresses
	for _, addr := range addrs {
		ip := net.ParseIP(addr)
		if ip != nil && ip.To4() != nil && !ip.IsLoopback() {
			return addr
		}
	}

	// Fall back to first non-loopback address
	for _, addr := range addrs {
		ip := net.ParseIP(addr)
		if ip != nil && !ip.IsLoopback() {
			return addr
		}
	}

	// If all resolved addresses are loopback, get IP from interfaces
	return getFirstNonLoopbackIP()
}

// getFirstNonLoopbackIP returns the first non-loopback IPv4 address from network interfaces
func getFirstNonLoopbackIP() string {
	interfaces, err := net.Interfaces()
	if err != nil {
		return "127.0.0.1"
	}

	for _, iface := range interfaces {
		// Skip loopback and down interfaces
		if iface.Flags&net.FlagLoopback != 0 || iface.Flags&net.FlagUp == 0 {
			continue
		}

		addrs, err := iface.Addrs()
		if err != nil {
			continue
		}

		for _, addr := range addrs {
			var ip net.IP
			switch v := addr.(type) {
			case *net.IPNet:
				ip = v.IP
			case *net.IPAddr:
				ip = v.IP
			}

			if ip != nil && ip.To4() != nil && !ip.IsLoopback() {
				return ip.String()
			}
		}
	}

	return "127.0.0.1"
}

// wanConfig carries the WAN-replication parameters for a resource. When non-nil,
// generateDrbdConfig emits the opt-in WAN variant: protocol A, DRBD-level
// pull-ahead, and loopback-routed addresses so DRBD talks to the local
// per-resource sds-proxy instead of the peer's real IP. Nil ⇒ ordinary LAN
// config (unchanged). See docs/2026-07-05-wan-replication-design.md.
type wanConfig struct {
	DRNode string // the DR-site node

	// PrimaryNodes are the production-site replicas, in config order. With one
	// entry this is the historic two-endpoint WAN resource; with several it is
	// a synchronous primary site plus one asynchronous DR copy, and each
	// primary gets its own WAN leg (DRBD 9 is a full mesh, so the DR must peer
	// with every one of them — otherwise a failover inside the primary site
	// lands on a node with no path to the DR and replication stops).
	PrimaryNodes []string

	// BindPorts are the loopback ports each primary's DRBD binds for its WAN
	// leg, in the same order as PrimaryNodes. Empty means "use the default
	// offset", which is only safe when nothing else on the host has taken those
	// ports — see wanDRBDBindOffset.
	BindPorts []int
}

// bindPort returns the loopback port primary i binds for its WAN leg.
func (w *wanConfig) bindPort(legPort, i int) int {
	if i < len(w.BindPorts) && w.BindPorts[i] > 0 {
		return w.BindPorts[i]
	}
	return legPort + wanDRBDBindOffset
}

// wanDRBDBindOffset is the starting guess for the loopback port DRBD binds on
// the PRIMARY side of a WAN leg. It has to differ from the leg port itself,
// which the local dialer already listens on; the primary's DRBD then connects to
// the leg port and lands on that dialer.
//
// It is only a starting point, because a fixed offset collides across resources:
// a resource on port 7300 would bind 7400, which is exactly the leg port of a
// second resource on 7400 — the primary's DRBD then fails to bind with
// EADDRINUSE and the leg never comes up. Callers resolve the real port against
// what the host is already using; see pickWANBindPorts.
const wanDRBDBindOffset = 100

// multiPrimary reports whether the primary site holds more than one replica.
func (w *wanConfig) multiPrimary() bool { return w != nil && len(w.PrimaryNodes) > 1 }

// wanproxyLocalBinaryPath is the controller-local path to the sds-proxy binary
// the WAN provisioner pushes to both nodes. We follow the same convention as
// the service-ip / sds-controller helpers: a well-known /usr/local/bin path.
var wanproxyLocalBinaryPath = "/usr/local/bin/sds-proxy"

// wanproxyBinaryPath returns the controller-local sds-proxy binary to push to
// the WAN nodes, or "" when it is not present locally. Returning "" makes
// wanproxy.Provision skip the binary push and assume the binary was pre-staged
// on the nodes (a warning is logged) rather than failing the create outright —
// most fleets stage sds-proxy alongside drbd-utils via their image/package.
//
// This is the architecture-blind answer, kept for callers that push to a single
// known-compatible node. Anything pushing to a set of nodes should use
// wanproxyBinaryResolver, because the two ends of a WAN leg frequently differ:
// the off-site node is whatever the cloud rents, the primary site is whatever is
// on the shelf.
//
// Nothing calls it today: every WAN path currently provisions a set of nodes and
// so goes through wanproxyBinaryResolver. It is kept rather than deleted because
// it is the single-node half of that pair, and the next single-node caller would
// otherwise have to rediscover the rule stated above — a missing binary is a
// warning, not a failed create. The unused check is silenced for exactly that
// reason, not because the function is a leftover.
//
//nolint:unused // deliberately retained; see the note above.
func (rm *ResourceManager) wanproxyBinaryPath() string {
	if _, err := os.Stat(wanproxyLocalBinaryPath); err != nil {
		rm.controller.logger.Warn("sds-proxy binary not found on controller; assuming it is pre-staged on WAN nodes",
			zap.String("path", wanproxyLocalBinaryPath))
		return ""
	}
	return wanproxyLocalBinaryPath
}

// nodeArchProbe reports a node's machine architecture in Go's naming.
const nodeArchProbe = `case "$(uname -m)" in x86_64) echo amd64;; aarch64|arm64) echo arm64;; *) uname -m;; esac`

// wanproxyBinaryResolver returns a function that picks the controller-local
// sds-proxy binary appropriate to each node.
//
// Pushing one file to every node is right only while the fleet is uniform, and
// a two-site cluster is the case least likely to be: an arm64 machine at home
// replicating to whatever architecture the off-site provider rents. The same
// push then installs an unrunnable file, and the failure surfaces nowhere near
// the cause — systemd reports 203/EXEC on the node while the operator sees a
// DRBD connection that never forms.
//
// Per-architecture binaries are looked for beside the default path, named
// "<path>-<goarch>" (e.g. /usr/local/bin/sds-proxy-arm64). A node whose
// architecture matches the controller's own falls back to the plain path, which
// keeps every existing single-architecture deployment working untouched.
func (rm *ResourceManager) wanproxyBinaryResolver(ctx context.Context, hosts []string) func(string) string {
	arch := make(map[string]string, len(hosts))
	for _, h := range hosts {
		if res, err := rm.deployment.Exec(ctx, []string{h}, nodeArchProbe); err == nil && res != nil {
			for _, r := range res.Hosts {
				arch[h] = strings.TrimSpace(r.Output)
				break
			}
		}
	}

	return func(host string) string {
		a := arch[host]
		if a != "" {
			if p := wanproxyLocalBinaryPath + "-" + a; fileExists(p) {
				return p
			}
		}
		// The plain path is the controller's own architecture. Offer it only when
		// the node agrees, or when the probe failed and there is nothing better
		// to go on — pushing a binary of the wrong architecture is worse than
		// pushing none, because "missing" is a failure the operator can read.
		if (a == runtime.GOARCH || a == "") && fileExists(wanproxyLocalBinaryPath) {
			return wanproxyLocalBinaryPath
		}
		rm.controller.logger.Warn("No sds-proxy binary on the controller for this node's architecture; assuming it is pre-staged",
			zap.String("host", host), zap.String("arch", a),
			zap.String("looked_for", wanproxyLocalBinaryPath+"-"+a))
		return ""
	}
}

func fileExists(p string) bool {
	st, err := os.Stat(p)
	return err == nil && !st.IsDir()
}

// wanproxyDeployClient adapts the resource manager's deployment client to the
// wanproxy.DeploymentClient interface used by the WAN provisioning path.
func (rm *ResourceManager) wanproxyDeployClient() wanproxy.DeploymentClient {
	return NewWanproxyDeploymentClient(rm.deployment)
}

// wanEndpointAddrs resolves a stored WAN resource's primary and DR node
// addresses (used by the delete path to deprovision the proxy). The DR node is
// dbRes.DRNode; the primary is the other node in dbRes.Nodes.
// wanPrimaryAddrs returns every primary-site node address of a WAN resource
// plus the DR address. A multi-replica primary site has one WAN leg per
// primary, so tearing the resource down has to reach all of them — using only
// the first would strand the other legs' proxy units and configs on the nodes.
func (rm *ResourceManager) wanPrimaryAddrs(dbRes *database.Resource) (primaryAddrs []string, drAddr string) {
	drNode := strings.TrimSpace(dbRes.DRNode)
	drAddr = rm.controller.ResolveHost(drNode)
	for _, n := range strings.Split(dbRes.Nodes, ",") {
		n = strings.TrimSpace(n)
		if n == "" || n == drNode {
			continue
		}
		primaryAddrs = append(primaryAddrs, rm.controller.ResolveHost(n))
	}
	return primaryAddrs, drAddr
}

func (rm *ResourceManager) wanEndpointAddrs(dbRes *database.Resource) (primaryAddr, drAddr string) {
	drNode := strings.TrimSpace(dbRes.DRNode)
	drAddr = rm.controller.ResolveHost(drNode)
	for _, n := range strings.Split(dbRes.Nodes, ",") {
		n = strings.TrimSpace(n)
		if n == "" || n == drNode {
			continue
		}
		primaryAddr = rm.controller.ResolveHost(n)
		break
	}
	return primaryAddr, drAddr
}

// WANStatusInfo carries a WAN resource's DR endpoints and the live
// sds-proxy@<resource> unit state on each WAN node (keyed by node name).
type WANStatusInfo struct {
	DRNode     string
	DREndpoint string
	WANPort    int
	// ProxyState maps a node name to its `systemctl is-active sds-proxy@<res>`
	// result ("active" / "inactive" / "failed" / "unknown").
	ProxyState map[string]string
	// WANReachable is true when the primary can currently reach the DR WAN
	// endpoint over TCP (firewall/security group permits the mTLS port).
	WANReachable bool
	// Metrics is the primary side's published proxy counters, or nil when they
	// could not be read. Nil means UNKNOWN and must never be rendered as zero:
	// the headline figure here is the un-replicated backlog, i.e. how much a DR
	// failover would lose, and a confident "0" would be dangerous.
	Metrics *wanproxy.Metrics
}

// WANStatus returns the WAN replication view for a resource, or (nil, nil) for a
// LAN resource (WANMode false / no record). It probes the sds-proxy unit on the
// primary and DR nodes so `resource status` can surface proxy health.
func (rm *ResourceManager) WANStatus(ctx context.Context, name string) (*WANStatusInfo, error) {
	if rm.controller.db == nil {
		return nil, nil
	}
	dbRes, err := rm.controller.db.GetResource(ctx, name)
	if err != nil || dbRes == nil || !dbRes.WANMode {
		return nil, nil
	}
	info := &WANStatusInfo{
		DRNode:     dbRes.DRNode,
		DREndpoint: dbRes.DREndpoint,
		WANPort:    dbRes.WANPort,
		ProxyState: map[string]string{},
	}
	_, drAddr := rm.wanEndpointAddrs(dbRes)

	// One leg per primary-site replica, each its own systemd instance. Probing
	// only "sds-proxy@<resource>" reports every leg of a multi-replica resource
	// as inactive, because that unit name only exists in the single-replica
	// shape.
	primaryNodes := make([]string, 0, 4)
	for _, n := range strings.Split(dbRes.Nodes, ",") {
		n = strings.TrimSpace(n)
		if n != "" && n != dbRes.DRNode {
			primaryNodes = append(primaryNodes, n)
		}
	}
	single := len(primaryNodes) <= 1

	probe := func(label, addr, unit string) {
		state := "unknown"
		if addr != "" && rm.deployment != nil {
			// `|| true` so an inactive unit (non-zero exit) still yields its state.
			if res, err := rm.deployment.Exec(ctx, []string{addr},
				"systemctl is-active "+unit+" 2>/dev/null || true"); err == nil && res != nil {
				if hr, ok := res.Hosts[addr]; ok {
					if s := strings.TrimSpace(hr.Output); s != "" {
						state = s
					}
				}
			}
		}
		info.ProxyState[label] = state
	}

	// The DR terminates every leg, so it runs one unit per primary. Report them
	// per leg rather than collapsing to one line for the DR node, or a single
	// dead tunnel hides behind a healthy one.
	for _, n := range primaryNodes {
		unit := wanproxy.UnitInstance(wanproxy.LegID(name, rm.controller.ResolveHost(n), single))
		probe(n, rm.controller.ResolveHost(n), unit)
		label := dbRes.DRNode
		if !single {
			label = dbRes.DRNode + " (leg " + n + ")"
		}
		probe(label, drAddr, unit)
	}
	// Reachability and counters come from a leg whose dialer is actually
	// running. Probing from the first node in the list instead reports "WAN
	// unreachable" whenever that particular replica happens to be down, which
	// points at the wrong end of the link.
	multi := rm.wanMultiSpecFor(dbRes)
	if st, serr := wanproxy.StatusMulti(ctx, rm.wanproxyDeployClient(), multi); serr == nil && st != nil {
		info.WANReachable = st.WANReachable
		// Proxy counters from a live primary (the side that holds the backlog).
		// Best effort: an older proxy publishes nothing, and that must not fail
		// status.
		if host := liveWANPrimary(st); host != "" {
			info.Metrics = wanproxy.ReadNodeMetrics(ctx, rm.wanproxyDeployClient(), host, wanLegFor(st, host))
		}
	}
	return info, nil
}

// liveWANPrimary returns the address of a primary-site node whose dialer is
// running, or "" when none is.
func liveWANPrimary(st *wanproxy.MultiStatus) string {
	for _, l := range st.Legs {
		if l.PrimaryActive {
			return l.PrimaryHost
		}
	}
	return ""
}

// wanLegFor returns the leg id served by the given primary host.
func wanLegFor(st *wanproxy.MultiStatus, host string) string {
	for _, l := range st.Legs {
		if l.PrimaryHost == host {
			return l.LegID
		}
	}
	return st.Resource
}

// randomWANPort picks a random TCP port in [3001, 65535] for a WAN proxy when
// the caller does not specify one, matching the project convention of using
// high, non-well-known ports.
func randomWANPort() uint32 {
	const lo, hi = 3001, 65535
	n, err := crand.Int(crand.Reader, big.NewInt(int64(hi-lo+1)))
	if err != nil {
		// crypto/rand should never fail; fall back to a time-derived port so we
		// still return a usable high port rather than aborting the create.
		return uint32(lo + int(time.Now().UnixNano()%(hi-lo+1)))
	}
	return uint32(lo) + uint32(n.Int64())
}

// generateDrbdConfig generates a DRBD resource configuration file for one or
// more volumes (volume 0..N). Diskful nodes share the resource-level volume
// blocks; diskless tiebreaker nodes override each with `disk none`.
//
// wan is nil for a normal LAN resource (output unchanged). When set, the config
// is rendered in WAN mode (protocol A + pull-ahead + loopback addresses).

// applyLocalSiteQuorum narrows a running WAN resource's quorum to a majority of
// its primary site.
//
// It is a separate step from config generation on purpose. A numeric quorum is
// enforced absolutely, from the moment the resource exists — before any peer has
// connected there is exactly one node visible, so the force-promote that
// establishes the first UpToDate generation is refused with "No quorum". DRBD's
// own "majority" is adaptive to the membership it has seen, so creation needs
// it. Once the peers are up, the number is both satisfiable and the thing we
// actually want: see localSiteQuorum for why the DR must not vote.
func (rm *ResourceManager) applyLocalSiteQuorum(ctx context.Context, resource string, hosts []string, localVoters int) error {
	if len(hosts) == 0 {
		return nil
	}
	resPath := fmt.Sprintf("/etc/drbd.d/%s.res", resource)
	catRes, err := rm.deployment.Exec(ctx, []string{hosts[0]}, "cat "+resPath)
	if err != nil {
		return fmt.Errorf("read resource config to set quorum: %w", err)
	}
	live, ok := "", false
	for _, r := range catRes.Hosts {
		live, ok = r.Output, r.Success
		break
	}
	if !ok || strings.TrimSpace(live) == "" {
		return fmt.Errorf("read resource config for %q: %s", resource, catRes.FailureDetails())
	}

	updated := setLocalSiteQuorum(live, localVoters)
	if updated == live {
		return nil
	}
	if _, err := rm.deployment.DistributeConfig(ctx, hosts, updated, resPath); err != nil {
		return fmt.Errorf("distribute quorum change: %w", err)
	}
	return rm.execAllSuccess(ctx, hosts, fmt.Sprintf("sudo drbdadm adjust %s", resource),
		"apply the primary-site quorum")
}

// localSiteVoters counts the members entitled to decide whether the primary
// site may write. allMembers is every configured participant of a WAN resource,
// exactly one of which is the off-site DR; the DR replicates and still counts as
// a member, it just does not get to decide whether home can write. See
// primarySiteQuorum for why.
//
// A named function rather than a `- 1` at the call site: the subtraction is the
// entire safety property, and an inline one is neither greppable nor testable.
func localSiteVoters(allMembers int) int {
	return allMembers - 1
}

func (rm *ResourceManager) generateDrbdConfig(name string, port uint32, volumes []resolvedVolume, nodes, disklessNodes []string, protocol, storageType string, options map[string]string, wan *wanConfig) string {
	var config strings.Builder

	// Organize options by section -> key -> value
	sections := make(map[string]map[string]string)

	// Helper to set option
	setOption := func(section, key, value string) {
		if sections[section] == nil {
			sections[section] = make(map[string]string)
		}
		sections[section][key] = value
	}

	// Add defaults
	setOption("options", "auto-promote", "no")
	// Always "majority" here, even for a WAN resource whose steady-state quorum
	// should exclude the DR. A fixed number is absolute from the very first
	// moment, including before any peer has ever connected, so it blocks the
	// force-promote that gives a brand-new resource its first UpToDate
	// generation. `majority` is adaptive to the membership DRBD has actually
	// seen, so it lets creation proceed. The numeric value is applied once the
	// resource is up and its peers are connected — see applyLocalSiteQuorum.
	setOption("options", "quorum", "majority")
	setOption("options", "on-no-quorum", "io-error")
	setOption("options", "on-no-data-accessible", "io-error")
	setOption("options", "on-suspended-primary-outdated", "force-secondary")

	setOption("net", "rr-conflict", "retry-connect")

	// WAN mode: force async protocol A and enable DRBD's own congestion
	// pull-ahead so the primary goes Ahead (keeps writing) instead of blocking
	// when the WAN buffer fills. These are defaults — the user-options loop below
	// still overrides any of them. See the design doc for the rationale.
	//
	// Only for the two-endpoint shape, where every connection crosses the WAN.
	// With a multi-replica primary site these belong to the WAN legs alone and
	// are emitted per connection: applying them at resource level would quietly
	// downgrade the synchronous primary-site mesh to async, which is the exact
	// guarantee those replicas exist to provide.
	if wan != nil && !wan.multiPrimary() {
		protocol = "A"
		setOption("net", "on-congestion", "pull-ahead")
		setOption("net", "congestion-fill", "2M")
		setOption("net", "congestion-extents", "500")
		setOption("net", "ping-timeout", "20")
	}

	// Process user options
	for k, v := range options {
		parts := strings.SplitN(k, "/", 2)
		if len(parts) == 2 {
			// section/key format (e.g. disk/on-io-error)
			section := strings.ToLower(parts[0])
			key := parts[1]
			setOption(section, key, v)
		} else {
			// default to options section
			setOption("options", k, v)
		}
	}

	fmt.Fprintf(&config, "resource %s {\n", name)

	// Write configuration sections
	knownSections := []string{"options", "net", "startup", "handlers"} // disk handled separately inside volume
	processed := make(map[string]bool)

	for _, s := range knownSections {
		opts, ok := sections[s]

		// Always write net section to include protocol
		if s == "net" {
			config.WriteString("\n    net {\n")
			fmt.Fprintf(&config, "        protocol %s;\n", protocol)
			// Without a verify algorithm `drbdadm verify` refuses to start,
			// so the one tool that proves two replicas hold the same data is
			// unavailable exactly when someone needs it. It costs nothing
			// until a verify runs. An explicit option still wins.
			if _, set := opts["verify-alg"]; !set {
				config.WriteString("        verify-alg crc32c;\n")
			}
			if ok {
				var keys []string
				for k := range opts {
					keys = append(keys, k)
				}
				sort.Strings(keys)
				for _, k := range keys {
					fmt.Fprintf(&config, "        %s %s;\n", k, opts[k])
				}
			}
			config.WriteString("    }\n")
			processed[s] = true
			continue
		}

		if ok && len(opts) > 0 {
			fmt.Fprintf(&config, "\n    %s {\n", s)

			// Sort keys for deterministic output
			var keys []string
			for k := range opts {
				keys = append(keys, k)
			}
			sort.Strings(keys)

			for _, k := range keys {
				fmt.Fprintf(&config, "        %s %s;\n", k, opts[k])
			}
			config.WriteString("    }\n")
			processed[s] = true
		}
	}

	// Write any other custom sections (excluding disk which is handled in volume)
	var customSections []string
	for s := range sections {
		if !processed[s] && s != "disk" {
			customSections = append(customSections, s)
		}
	}
	sort.Strings(customSections)

	for _, s := range customSections {
		// Generic write
		opts := sections[s]
		fmt.Fprintf(&config, "\n    %s {\n", s)
		var keys []string
		for k := range opts {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			fmt.Fprintf(&config, "        %s %s;\n", k, opts[k])
		}
		config.WriteString("    }\n")
	}

	// Gather disk options once; they are applied to every volume block.
	var diskOptKeys []string
	if diskOpts, ok := sections["disk"]; ok && len(diskOpts) > 0 {
		for k := range diskOpts {
			diskOptKeys = append(diskOptKeys, k)
		}
		sort.Strings(diskOptKeys)
	}

	// Generate a resource-level block for each volume (volume 0..N).
	for _, v := range volumes {
		fmt.Fprintf(&config, "\n    volume %d {\n", v.id)
		fmt.Fprintf(&config, "        device    minor %d;\n", v.minor)

		// The ZFS or LVM device path per storage type — or, for an encrypted
		// volume, the crypt container that sits on top of it. DRBD must attach
		// to the mapping and never to the LV underneath: pointing it at the LV
		// would have it write plaintext straight past the layer that exists to
		// encrypt it.
		fmt.Fprintf(&config, "        disk      %s;\n", v.backingDevice(storageType))
		config.WriteString("        meta-disk internal;\n")

		if len(diskOptKeys) > 0 {
			diskOpts := sections["disk"]
			config.WriteString("        disk {\n")
			for _, k := range diskOptKeys {
				fmt.Fprintf(&config, "            %s %s;\n", k, diskOpts[k])
			}
			config.WriteString("        }\n")
		}

		config.WriteString("    }\n")
	}

	// Generate on sections for each node. Diskful nodes come first and share
	// the resource-level volume blocks above; diskless tiebreaker nodes follow
	// and override every volume with `disk none` so they join quorum without
	// storing data. node-id is the position in this combined ordering.
	allNodes := append(append([]string{}, nodes...), disklessNodes...)
	diskless := make(map[string]bool, len(disklessNodes))
	for _, n := range disklessNodes {
		diskless[n] = true
	}

	for i, node := range allNodes {
		// The REPLICATION address, which is the management address unless the node
		// was registered with a dedicated one. Using it here (and only here) is
		// what puts DRBD traffic on its own NIC/subnet while the controller keeps
		// reaching the node over the management address for SSH.
		ip := rm.controller.nodes.GetReplicationAddressByName(node)

		// Fallback: try direct lookup in hostMap
		if ip == "" {
			rm.mu.RLock()
			ip = rm.hostMap[node]
			rm.mu.RUnlock()
		}

		// Final fallback to node name if still not found
		if ip == "" {
			ip = node
		}

		// Resolve hostname to IP address if not already an IP
		ip = resolveToIP(ip)

		// `on <name>` must be the node's real hostname: drbdadm only applies a
		// resource to a host that finds itself in one of these sections. The
		// SDS node name is an operator-chosen label and may differ.
		fmt.Fprintf(&config, "\n    on %s {\n", rm.controller.nodes.GetDRBDNameByRef(node))
		if wan.multiPrimary() {
			// Multi-replica primary site: the per-node `address` is the LAN
			// address the other replicas reach it on. The WAN legs cannot use it
			// (they go through a loopback proxy) and are therefore written as
			// explicit `connection` sections after the host stanzas — DRBD lets
			// a connection override the endpoint addresses per peer pair, which
			// is the only way one node can speak LAN to its siblings and
			// loopback-proxy to the DR at the same time.
			if node == wan.DRNode {
				// The DR has no LAN peers; every one of its connections is a
				// WAN leg, so this address is never used. Keep it on loopback
				// so a stray direct connect cannot leave the tunnel.
				fmt.Fprintf(&config, "        address   127.0.0.1:%d;\n", port)
			} else {
				fmt.Fprintf(&config, "        address   %s:%d;\n", ip, port)
			}
		} else if wan != nil {
			// WAN: route through the local per-resource sds-proxy on loopback
			// instead of the peer's real IP. The DR node binds `port` (the
			// acceptor dials it there); the primary binds `port+9` and connects
			// out to `port` = the local dialer's drbd_listen. Both addresses are
			// loopback so each node reaches its own local proxy.
			addrPort := port + 9
			if node == wan.DRNode {
				addrPort = port
			}
			fmt.Fprintf(&config, "        address   127.0.0.1:%d;\n", addrPort)
		} else {
			fmt.Fprintf(&config, "        address   %s:%d;\n", ip, port)
		}
		fmt.Fprintf(&config, "        node-id   %d;\n", i)
		if diskless[node] {
			for _, v := range volumes {
				fmt.Fprintf(&config, "        volume %d {\n", v.id)
				fmt.Fprintf(&config, "            device    minor %d;\n", v.minor)
				config.WriteString("            disk      none;\n")
				config.WriteString("        }\n")
			}
		}
		config.WriteString("    }\n")
	}

	if wan.multiPrimary() {
		// Two-site topology: a synchronous mesh inside the primary site, plus
		// one asynchronous leg from every primary replica to the DR.
		//
		// The primary-site mesh is spelled out rather than left to
		// connection-mesh because connection-mesh would also pair each replica
		// with the DR using the host stanza addresses, which for a WAN peer are
		// meaningless (the DR is only reachable through the local proxy).
		drName := rm.controller.nodes.GetDRBDNameByRef(wan.DRNode)

		if len(allNodes) > 2 {
			// LAN mesh: every primary-site node with every other, including any
			// diskless tiebreaker, on their real addresses.
			lanHosts := make([]string, 0, len(allNodes))
			for _, node := range allNodes {
				if node == wan.DRNode {
					continue
				}
				lanHosts = append(lanHosts, rm.controller.nodes.GetDRBDNameByRef(node))
			}
			if len(lanHosts) > 1 {
				config.WriteString("\n    connection-mesh {\n")
				config.WriteString("        hosts")
				for _, h := range lanHosts {
					fmt.Fprintf(&config, " %s", h)
				}
				config.WriteString(";\n")
				config.WriteString("    }\n")
			}
		}

		// One explicit connection per WAN leg. Leg i uses loopback port
		// (port + wanLegPortStride*i) on the primary and the same +
		// wanDRLoopbackOffset on the DR, matching wanproxy's leg layout: each
		// tunnel gets a private pair of loopback endpoints so two legs cannot
		// collide on the DR, which terminates all of them.
		for i, primary := range wan.PrimaryNodes {
			primaryName := rm.controller.nodes.GetDRBDNameByRef(primary)
			legPort := port + uint32(i)
			config.WriteString("\n    connection {\n")
			// The primary binds a port the proxy does not use, and connects to
			// legPort — which on its own loopback is the local dialer. The DR
			// binds legPort, where its local acceptor dials it. Both ends read
			// the same numbers as their own loopback, which is what lets one
			// connection stanza describe a tunnel with two different endpoints.
			fmt.Fprintf(&config, "        host %s address 127.0.0.1:%d;\n", primaryName, wan.bindPort(int(legPort), i))
			fmt.Fprintf(&config, "        host %s address 127.0.0.1:%d;\n", drName, legPort)
			// Async across the WAN, whatever the LAN mesh uses. pull-ahead lets
			// a stalled tunnel drop behind instead of blocking the primary.
			config.WriteString("        net {\n")
			config.WriteString("            protocol A;\n")
			config.WriteString("            on-congestion pull-ahead;\n")
			config.WriteString("            congestion-fill 400M;\n")
			config.WriteString("        }\n")
			config.WriteString("    }\n")
		}
	} else if len(allNodes) > 2 {
		// Add connection-mesh for multi-node DRBD 9
		// DRBD 9 requires a full mesh of connections between all nodes
		config.WriteString("\n    connection-mesh {\n")
		config.WriteString("        hosts")
		for _, node := range allNodes {
			// Same rule as the `on` sections: the mesh lists DRBD host names.
			fmt.Fprintf(&config, " %s", rm.controller.nodes.GetDRBDNameByRef(node))
		}
		config.WriteString(";\n")
		config.WriteString("    }\n")
	}

	config.WriteString("}\n")

	return config.String()
}

// GetResource gets resource information from database with live status
func (rm *ResourceManager) GetResource(ctx context.Context, name string) (*ResourceInfo, error) {
	if rm.controller.db == nil {
		return nil, fmt.Errorf("database not available")
	}

	hosts, err := rm.resourceHosts(ctx, name)
	if err != nil {
		return nil, err
	}

	// Get resource info from database
	dbRes, err := rm.controller.db.GetResource(ctx, name)
	if err != nil {
		return nil, fmt.Errorf("resource not found: %s", name)
	}

	dbVolumes, err := rm.controller.db.ListVolumes(ctx, name)
	if err != nil {
		rm.controller.logger.Warn("Failed to list resource volumes from database",
			zap.String("resource", name),
			zap.Error(err))
		dbVolumes = nil
	}
	dbVolumeByID := make(map[int]*database.Volume, len(dbVolumes))
	for _, volume := range dbVolumes {
		dbVolumeByID[volume.VolumeID] = volume
	}

	// Parse nodeAddresses from comma-separated string
	var nodeAddresses []string
	if dbRes.Nodes != "" {
		nodeAddresses = strings.Split(dbRes.Nodes, ",")
	}

	var disklessNodes []string
	if dbRes.DisklessNodes != "" {
		disklessNodes = strings.Split(dbRes.DisklessNodes, ",")
	}

	rm.controller.logger.Debug("GetResource",
		zap.String("name", name),
		zap.String("dbRes.Nodes", dbRes.Nodes),
		zap.Strings("parsed_nodeAddresses", nodeAddresses))

	// Ask hosts in turn until one answers. Asking only hosts[0] made a resource
	// entirely unobservable whenever its first node was down — role Unknown, no
	// node states, every peer greyed out — while healthy peers sat there able to
	// answer. Any live replica reports the whole resource, so which one replies
	// does not matter; that it is reachable does.
	// answeredAt indexes both hosts and nodeAddresses: resourceHosts builds them
	// in the same order, so the node that answered can be named. Both parsers
	// need that name — the status output describes the answering node as
	// "local", and filing it under the wrong node reports a machine that is
	// down as healthy.
	result, answeredAt, err := rm.drbdStatusFromAnyHost(ctx, hosts, name)

	var volumes []*ResourceVolumeInfo
	nodeStates := make(map[string]*ResourceNodeState)
	localRole := "Unknown"

	if err == nil {
		for _, r := range result.Hosts {
			if r.Success {
				rm.controller.logger.Debug("DRBD status output",
					zap.String("output", r.Output))

				// Parse local node role
				localRole = parseRoleFromStatus(r.Output)

				// Parse volumes
				volInfo := parseVolumesFromStatus(r.Output)
				for _, v := range volInfo {
					sizeGB := v.sizeGB
					pool := ""
					backingVolume := ""
					encrypted := false
					if dbVol, ok := dbVolumeByID[v.id]; ok {
						if sizeGB == 0 {
							sizeGB = uint64(max(dbVol.SizeGB, 0))
						}
						pool = dbVol.Pool
						backingVolume = dbVol.VolumeName
						// v.device is the DRBD device (/dev/drbdN); the crypt
						// layer only shows in the recorded BACKING device. The
						// resource flag is required as well, so an adopted
						// foreign resource that merely happens to live under
						// /dev/mapper is not reported as one of ours.
						encrypted = dbRes.Encrypted && luksIsMapperPath(dbVol.Device)
					}
					volumes = append(volumes, &ResourceVolumeInfo{
						VolumeID:      uint32(v.id),
						Device:        v.device,
						SizeGB:        sizeGB,
						Pool:          pool,
						BackingVolume: backingVolume,
						Encrypted:     encrypted,
					})
				}

				// Parse node states from status output
				nodeStates = parseNodeStatesFromStatus(r.Output, nodesLocalFirst(nodeAddresses, answeredAt))

				rm.controller.logger.Debug("Parsed node states",
					zap.Int("count", len(nodeStates)))

				break
			}
		}
	}

	// Prefer structured `drbdsetup status --json`: it is keyed by node name and
	// exposes per-peer replication state and resync completion (percent) that
	// the plain-text parse above cannot surface. It must query the same host the
	// text status came from: the JSON "local" node is whichever node answered,
	// not whichever is configured first.
	// On any failure (older drbd without --json, non-zero exit, parse error) we
	// keep the text-parsed states above and degrade gracefully.
	if len(nodeAddresses) > 0 && answeredAt >= 0 && answeredAt < len(nodeAddresses) {
		localNode := nodeAddresses[answeredAt]
		if jsonResult, jerr := rm.deployment.DRBDStatusJSON(ctx, []string{hosts[answeredAt]}, name); jerr == nil {
			for _, r := range jsonResult.Hosts {
				if !r.Success {
					continue
				}
				parsed, perr := parseNodeStatesFromJSON(r.Output, localNode)
				if perr != nil {
					rm.controller.logger.Debug("drbdsetup status --json parse failed; keeping text-parsed states",
						zap.String("resource", name), zap.Error(perr))
					break
				}
				if len(parsed) > 0 {
					nodeStates = parsed
					if local, ok := parsed[localNode]; ok && local.Role != "" {
						localRole = local.Role
					}
				}
				break
			}
		} else {
			rm.controller.logger.Debug("drbdsetup status --json unavailable; keeping text-parsed states",
				zap.String("resource", name), zap.Error(jerr))
		}
	}

	info := &ResourceInfo{
		Name:            dbRes.Name,
		Port:            uint32(dbRes.Port),
		Protocol:        dbRes.Protocol,
		Nodes:           nodeAddresses,
		Role:            localRole, // Local node's role
		Volumes:         volumes,
		NodeStates:      nodeStates,
		DisklessNodes:   disklessNodes,
		DisklessClients: splitCSV(dbRes.DisklessClients),
		// Two diskful nodes with no tiebreaker means a single failure drops
		// below quorum majority and suspends I/O.
		QuorumRisk: len(nodeAddresses) == 2 && len(disklessNodes) == 0,
		Labels:     cloneStringMap(dbRes.Labels),
		Profile:    dbRes.Profile,
		WANMode:    dbRes.WANMode,
		DRNode:     dbRes.DRNode,
		Encrypted:  dbRes.Encrypted,
	}

	if len(info.Volumes) == 0 && len(dbVolumes) > 0 {
		for _, volume := range dbVolumes {
			info.Volumes = append(info.Volumes, &ResourceVolumeInfo{
				VolumeID:      uint32(volume.VolumeID),
				Device:        fmt.Sprintf("/dev/drbd/by-res/%s/%d", name, volume.VolumeID),
				SizeGB:        uint64(max(volume.SizeGB, 0)),
				Pool:          volume.Pool,
				BackingVolume: volume.VolumeName,
				Encrypted:     dbRes.Encrypted && luksIsMapperPath(volume.Device),
			})
		}
	}

	return info, nil
}

// ListResources lists all resources from database with live status
func (rm *ResourceManager) ListResources(ctx context.Context) ([]*ResourceInfo, error) {
	if rm.controller.db == nil {
		return nil, fmt.Errorf("database not available")
	}

	// Get resources from database
	dbResources, err := rm.controller.db.ListResources(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to list resources from database: %w", err)
	}

	var resources []*ResourceInfo
	for _, dbRes := range dbResources {
		// Parse nodeAddresses from comma-separated string
		var nodeAddresses []string
		if dbRes.Nodes != "" {
			nodeAddresses = strings.Split(dbRes.Nodes, ",")
		}

		// Volume metadata comes from the database: listing must not fan out
		// SSH status calls per resource, and the persisted records carry
		// everything the API exposes (id, size, pool, backing volume).
		var volumes []*ResourceVolumeInfo
		if dbVolumes, err := rm.controller.db.ListVolumes(ctx, dbRes.Name); err == nil {
			for _, volume := range dbVolumes {
				volumes = append(volumes, &ResourceVolumeInfo{
					VolumeID:      uint32(volume.VolumeID),
					Device:        fmt.Sprintf("/dev/drbd/by-res/%s/%d", dbRes.Name, volume.VolumeID),
					SizeGB:        uint64(max(volume.SizeGB, 0)),
					Pool:          volume.Pool,
					BackingVolume: volume.VolumeName,
					Encrypted:     dbRes.Encrypted && luksIsMapperPath(volume.Device),
				})
			}
		}

		resources = append(resources, &ResourceInfo{
			Name:       dbRes.Name,
			Port:       uint32(dbRes.Port),
			Protocol:   dbRes.Protocol,
			Nodes:      nodeAddresses,
			Role:       "Unknown", // Live role comes from GetResource/ResourceStatus
			Volumes:    volumes,
			NodeStates: make(map[string]*ResourceNodeState),
			// Persisted diskless membership so the list view (and the web UI)
			// can distinguish quorum tiebreakers from diskless data clients
			// without a per-resource status fan-out.
			DisklessNodes:   splitCSV(dbRes.DisklessNodes),
			DisklessClients: splitCSV(dbRes.DisklessClients),
			Labels:          cloneStringMap(dbRes.Labels),
			Profile:         dbRes.Profile,
			// Same derivations the single-resource view makes. Omitting them
			// here left the list unable to flag a two-node resource with no
			// tiebreaker, or to tell an off-site DR from a local replica —
			// precisely the things a list is for.
			QuorumRisk: len(nodeAddresses) == 2 && dbRes.DisklessNodes == "",
			WANMode:    dbRes.WANMode,
			DRNode:     dbRes.DRNode,
			Encrypted:  dbRes.Encrypted,
		})
	}

	return resources, nil
}

func cloneStringMap(values map[string]string) map[string]string {
	if len(values) == 0 {
		return nil
	}
	cloned := make(map[string]string, len(values))
	for key, value := range values {
		cloned[key] = value
	}
	return cloned
}

// drbdConfigReferencesDisk reports whether a DRBD .res config already contains a
// volume whose backing disk is diskRef (e.g. "/dev/vg0/res_state1;"). Used to
// keep volume adds idempotent: appending a second volume block for a disk that
// is already referenced makes drbdadm reject the config with "conflicting use
// of disk". The "meta-disk" line is skipped so only real backing-disk lines
// match.
func drbdConfigReferencesDisk(config, diskRef string) bool {
	for _, line := range strings.Split(config, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "disk") && strings.Contains(trimmed, diskRef) {
			return true
		}
	}
	return false
}

// AddVolume adds a volume to an existing DRBD resource
func (rm *ResourceManager) AddVolume(ctx context.Context, resource, volume, pool string, sizeGB uint32) error {
	rm.controller.logger.Info("Adding volume to resource",
		zap.String("resource", resource),
		zap.String("volume", volume),
		zap.String("pool", pool),
		zap.Uint32("size_gb", sizeGB))

	if rm.deployment == nil {
		return fmt.Errorf("deployment client not set")
	}

	// Auto-select the pool when none was given: with exactly one registered
	// pool name the choice is unambiguous; otherwise the caller must decide.
	// (A hardcoded fallback name here used to send lvcreate at a volume
	// group that doesn't exist.)
	if pool == "" {
		selected, err := rm.autoSelectPool(ctx)
		if err != nil {
			return err
		}
		pool = selected
	}
	pool = normalizeManagedName(pool)

	// A volume added to an encrypted resource is encrypted too. Anything else
	// would put plaintext on the pool disks of a resource whose whole point is
	// that it does not — and would do it invisibly, since nothing in the DRBD
	// config makes one volume's crypt layer more visible than another's.
	encrypt := false
	if rm.controller.db != nil {
		dbRes, derr := rm.controller.db.GetResource(ctx, resource)
		if derr == nil && dbRes != nil {
			encrypt = dbRes.Encrypted
		}
	}
	if encrypt {
		if err := validateLUKSNames(pool, volume); err != nil {
			return err
		}
	}

	hosts, err := rm.resourceHosts(ctx, resource)
	if err != nil {
		return err
	}
	// Tiebreakers and diskless clients carry the resource config too. They get
	// no LV and no metadata, but the new volume has to appear in their copy —
	// as `disk none` — or DRBD refuses their connection. See
	// addDisklessVolumeOverrides.
	allHosts := append(append([]string(nil), hosts...), rm.disklessParticipantHosts(ctx, resource)...)

	// Get current config to find next volume number and minor
	result, err := rm.deployment.Exec(ctx, []string{hosts[0]}, fmt.Sprintf("cat /etc/drbd.d/%s.res", resource))
	if err != nil {
		return fmt.Errorf("failed to read config: %w", err)
	}

	var hostResult *deployment.HostResult
	for _, r := range result.Hosts {
		hostResult = r
		break
	}

	if hostResult == nil || !hostResult.Success {
		return fmt.Errorf("failed to get config")
	}

	// Volume numbers are scoped to this resource's config.
	maxVolNum := -1
	lines := strings.Split(hostResult.Output, "\n")
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "volume ") {
			parts := strings.Fields(trimmed)
			if len(parts) >= 2 {
				if volNum, err := strconv.Atoi(strings.TrimSuffix(parts[1], "{")); err == nil {
					if volNum > maxVolNum {
						maxVolNum = volNum
					}
				}
			}
		}
	}
	newVolNum := maxVolNum + 1

	// Idempotency guard: if a volume already references this backing LV in the
	// resource config, a previous add already created it. Appending a SECOND
	// volume block for the same disk makes drbdadm reject the whole config with
	// "conflicting use of disk ... first used here" and every create-md on the
	// duplicate minor fails. This is exactly what a retried gateway state-volume
	// provision used to do — each attempt appended another volume N pointing at
	// the same <res>_state1 LV. Treat an already-referenced disk as done.
	backingDevice := fmt.Sprintf("/dev/%s/%s", pool, volume)
	drbdDisk := backingDevice
	if encrypt {
		drbdDisk = luksMapperPath(pool, volume)
	}
	diskRef := drbdDisk + ";"
	if drbdConfigReferencesDisk(hostResult.Output, diskRef) {
		rm.controller.logger.Info("Volume already present in resource config; skipping duplicate add",
			zap.String("resource", resource),
			zap.String("volume", volume),
			zap.String("disk", diskRef))
		return nil
	}

	// Device minors are GLOBAL on a node: scanning only this resource's
	// config hands out minors already claimed by other resources and
	// drbdadm rejects the whole config with "conflicting use of
	// device-minor". Collect minors across every resource file instead.
	// (The previous in-file scan was additionally broken — it required 4
	// fields on a 3-field line and always allocated minor 0.)
	newMinor, err := rm.nextGlobalMinor(ctx, allHosts)
	if err != nil {
		return fmt.Errorf("failed to allocate device minor: %w", err)
	}

	// Extend the synchronized DRBD resource config with the new volume block.
	// LINBIT recommends updating the config identically on all nodes and then
	// calling `drbdadm adjust <resource>` to let DRBD enable the new volume.
	volumeBlock := fmt.Sprintf("    volume %d {\n        device    minor %d;\n        disk      %s;\n        meta-disk internal;\n    }",
		newVolNum, newMinor, drbdDisk)

	// Roll back partial state if a later step fails. Without this, a retry of a
	// failed add (e.g. create-md errored) re-reads the .res that still carries
	// the half-added volume block and appends ANOTHER block for the same LV,
	// which DRBD then rejects for "conflicting use of disk". On failure restore
	// the pre-add config on every node and remove the LV we created here.
	originalConfig := hostResult.Output
	committed := false
	defer func() {
		if committed {
			return
		}
		rm.controller.logger.Warn("Volume add failed; rolling back appended volume block and backing LV",
			zap.String("resource", resource),
			zap.String("volume", volume))
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer cancel()
		_, _ = rm.deployment.DistributeConfig(cleanupCtx, allHosts, originalConfig, fmt.Sprintf("/etc/drbd.d/%s.res", resource))
		if encrypt {
			// Close before removing: the open container holds the LV, and a key
			// left behind outlives the volume it was protecting.
			rm.closeBackingVolumeOn(cleanupCtx, hosts, pool, volume)
		}
		_, _ = rm.deployment.LVRemove(cleanupCtx, hosts, backingDevice)
	}()

	// Create LVs on all nodes
	for _, host := range hosts {
		_, err := rm.deployment.LVCreate(ctx, []string{host}, pool, volume, fmt.Sprintf("%dG", sizeGB))
		if err != nil {
			return fmt.Errorf("failed to create LV on %s: %w", host, err)
		}
		if encrypt {
			if err := rm.encryptBackingVolumeOn(ctx, host, host, pool, volume, backingDevice); err != nil {
				return err
			}
		}
	}

	lines = strings.Split(hostResult.Output, "\n")
	insertIdx := len(lines)
	for idx := len(lines) - 1; idx >= 0; idx-- {
		if strings.TrimSpace(lines[idx]) == "}" {
			insertIdx = idx
			break
		}
	}
	updatedLines := append([]string{}, lines[:insertIdx]...)
	updatedLines = append(updatedLines, volumeBlock)
	updatedLines = append(updatedLines, lines[insertIdx:]...)
	updatedConfig := addDisklessVolumeOverrides(strings.Join(updatedLines, "\n"), newVolNum, newMinor)

	if _, err := rm.deployment.DistributeConfig(ctx, allHosts, updatedConfig, fmt.Sprintf("/etc/drbd.d/%s.res", resource)); err != nil {
		return fmt.Errorf("failed to distribute updated config: %w", err)
	}

	// The new volume's backing device has no DRBD metadata yet; without
	// create-md the subsequent adjust attaches it Diskless.
	createMDCmd := fmt.Sprintf("sudo drbdadm create-md --force %s/%d", resource, newVolNum)
	mdResult, err := rm.deployment.Exec(ctx, hosts, createMDCmd)
	if err != nil {
		return fmt.Errorf("failed to create metadata for new volume: %w", err)
	}
	if !mdResult.AllSuccess() {
		return fmt.Errorf("metadata creation for new volume failed: %s", mdResult.FailureDetails())
	}

	adjustCmd := fmt.Sprintf("sudo drbdadm adjust %s", resource)
	adjustResult, err := rm.deployment.Exec(ctx, allHosts, adjustCmd)
	if err != nil {
		return fmt.Errorf("failed to adjust resource after volume add: %w", err)
	}
	if !adjustResult.AllSuccess() {
		return fmt.Errorf("resource adjust failed on hosts: %s", adjustResult.FailureDetails())
	}

	// A brand-new volume is Inconsistent on every node with no UpToDate
	// peer to sync from, so DRBD refuses to open it ("Could not open")
	// until an initial sync source exists. The volume is empty, so skip
	// the pointless full sync the LINSTOR way: declare a new current UUID
	// with a cleared bitmap on one node, which marks all replicas UpToDate.
	skipSyncCmd := fmt.Sprintf("sudo drbdadm new-current-uuid --clear-bitmap %s/%d", resource, newVolNum)
	if err := rm.execAllSuccess(ctx, []string{hosts[0]}, skipSyncCmd,
		"failed to initialize new volume sync state"); err != nil {
		return err
	}

	// The volume is now fully attached and UpToDate. Persisting to the database
	// is best-effort below, so a save failure must NOT roll back the working
	// volume — mark the add committed here.
	committed = true

	rm.controller.logger.Info("Volume added successfully",
		zap.String("resource", resource),
		zap.String("volume", volume))

	if rm.controller.db != nil {
		if err := rm.controller.db.SaveVolume(ctx, &database.Volume{
			ResourceName: resource,
			VolumeName:   volume,
			VolumeID:     newVolNum,
			Pool:         pool,
			SizeGB:       int(sizeGB),
			Device:       drbdDisk,
		}); err != nil {
			rm.controller.logger.Warn("Failed to save added volume to database",
				zap.String("resource", resource),
				zap.String("volume", volume),
				zap.Error(err))
		}
	}

	return nil
}

// SetOptions updates DRBD options on an existing resource. It rewrites the
// shared .res config in place — preserving volumes and node sections — and runs
// `drbdadm adjust` so the changes take effect without recreating the resource.
// Keys use the "section/key" form (e.g. "net/max-buffers", "disk/on-io-error");
// a bare key defaults to the resource-level "options" section.
func (rm *ResourceManager) SetOptions(ctx context.Context, resource string, options map[string]string) error {
	if len(options) == 0 {
		return fmt.Errorf("no options provided")
	}
	if err := rm.rewriteResourceConfig(ctx, resource, func(current string) (string, error) {
		updated, err := applyDrbdOptions(current, options)
		if err != nil {
			return "", fmt.Errorf("failed to apply options: %w", err)
		}
		return updated, nil
	}); err != nil {
		return err
	}
	rm.controller.logger.Info("Updated DRBD options",
		zap.String("resource", resource),
		zap.Any("options", options))
	return nil
}

// RepairResourceConfig brings every participant's copy of a resource's config
// back into agreement and applies it.
//
// What it fixes is a config that disagrees with itself about the diskless
// nodes: a volume the tiebreaker's stanza has no `disk none` override for, and
// a tiebreaker whose copy of the file never heard of that volume. Resources
// that gained a volume before AddVolume learned to update diskless nodes — every
// gateway on a cluster with a tiebreaker — are in exactly that state, their
// tiebreaker retrying a connection DRBD keeps refusing.
func (rm *ResourceManager) RepairResourceConfig(ctx context.Context, resource string) error {
	return rm.rewriteResourceConfig(ctx, resource, func(current string) (string, error) {
		return current, nil
	})
}

// rewriteResourceConfig reads a resource's config from a diskful node, applies
// change, reconciles the diskless nodes' volume overrides, and — when anything
// differs from what the diskless nodes hold — installs the result on every
// participant and adjusts it.
//
// Every participant, not every diskful node: tiebreakers and diskless clients
// hold the same file. Rewriting it on the diskful nodes alone is how their
// copies drifted in the first place.
func (rm *ResourceManager) rewriteResourceConfig(ctx context.Context, resource string, change func(string) (string, error)) error {
	if rm.deployment == nil {
		return fmt.Errorf("deployment client not set")
	}
	hosts, err := rm.resourceHosts(ctx, resource)
	if err != nil {
		return err
	}
	if len(hosts) == 0 {
		return fmt.Errorf("resource %q has no nodes", resource)
	}
	diskless := rm.disklessParticipantHosts(ctx, resource)
	allHosts := append(append([]string(nil), hosts...), diskless...)
	resPath := fmt.Sprintf("/etc/drbd.d/%s.res", resource)

	// Read the current config from a diskful node: it is the one that
	// describes the resource's volumes.
	result, err := rm.deployment.Exec(ctx, []string{hosts[0]}, "cat "+resPath)
	if err != nil {
		return fmt.Errorf("failed to read config: %w", err)
	}
	var current string
	var found bool
	for _, hr := range result.Hosts {
		current, found = hr.Output, hr.Success
		break
	}
	if !found || strings.TrimSpace(current) == "" {
		return fmt.Errorf("resource %q config not found on %s", resource, hosts[0])
	}

	updated, err := change(current)
	if err != nil {
		return err
	}
	updated = reconcileDisklessVolumeOverrides(updated)

	if _, err := rm.deployment.DistributeConfig(ctx, allHosts, updated, resPath); err != nil {
		return fmt.Errorf("failed to distribute updated config: %w", err)
	}

	// Diskless nodes first. When a diskful node learns that a peer's volume
	// is diskless it drops the bitmap it keeps for that peer, and the kernel
	// refuses that ("Can not drop the bitmap when both sides have a disk")
	// until the peer has actually connected as diskless. Adjusting everyone at
	// once lost that race on the first repair of a live gateway.
	adjust := fmt.Sprintf("sudo drbdadm adjust %s", resource)
	if len(diskless) > 0 {
		if err := rm.execAllSuccess(ctx, diskless, adjust, "failed to apply the resource config on the diskless nodes"); err != nil {
			return err
		}
	}
	// The diskless nodes connect asynchronously, so the diskful adjust can
	// still arrive first. Give it a few seconds' grace; with no diskless node
	// there is nothing to wait for and the first answer stands.
	var err2 error
	for attempt := 0; attempt < 5; attempt++ {
		if err2 = rm.execAllSuccess(ctx, hosts, adjust, "failed to apply the resource config"); err2 == nil || len(diskless) == 0 {
			return err2
		}
		select {
		case <-ctx.Done():
			return err2
		case <-time.After(3 * time.Second):
		}
	}
	return err2
}

// DeleteResource deletes a DRBD resource from all nodeAddresses
func (rm *ResourceManager) DeleteResource(ctx context.Context, name string, force bool) error {
	rm.controller.logger.Info("Deleting DRBD resource",
		zap.String("name", name),
		zap.Bool("force", force))

	if rm.deployment == nil {
		return fmt.Errorf("deployment client not set")
	}

	// If this resource has an HA config (a drbd-reactor promoter + VIP), tear it
	// down first — otherwise deleting the resource orphans the HA reactor config
	// and its DB record, which then lingers in the UI referencing a gone
	// resource.
	if rm.controller.db != nil {
		if ha, herr := rm.controller.db.GetHaConfig(ctx, name); herr == nil && ha != nil {
			if err := rm.RemoveHa(ctx, name); err != nil {
				rm.controller.logger.Warn("Failed to remove HA config during resource delete (continuing)",
					zap.String("resource", name), zap.Error(err))
			}
		}
	}

	// If this resource still exports a gateway (NFS/iSCSI/NVMe-oF), tear it
	// down first. The gateway's drbd-reactor promoter holds the DRBD device
	// Primary (keeping its LVs open) via drbd-services@<res>.target; deleting
	// the resource without removing the gateway config would leave that
	// promoter + target ACTIVE and the device UP, orphaning the resource as a
	// live device we could no longer bring down or lvremove. This mirrors the
	// HA cascade above and reuses the exact teardown Server.DeleteGateway runs
	// (Manager.DeleteGateway removes the reactor configs and stops the target;
	// DeleteGatewayByResource drops the DB record).
	if rm.controller.db != nil {
		if gw, gerr := rm.controller.db.GetGatewayByResource(ctx, name); gerr == nil && gw != nil {
			if rm.controller.gateway != nil {
				if err := rm.controller.gateway.DeleteGateway(ctx, name); err != nil {
					rm.controller.logger.Warn("Failed to tear down gateway during resource delete (continuing)",
						zap.String("resource", name), zap.Error(err))
				}
			}
			if err := rm.controller.db.DeleteGatewayByResource(ctx, name); err != nil {
				rm.controller.logger.Warn("Failed to delete gateway record during resource delete (continuing)",
					zap.String("resource", name), zap.Error(err))
			}
		}
	}

	hosts, err := rm.resourceHosts(ctx, name)
	if err != nil {
		return err
	}

	// Diskless quorum tiebreakers carry the config and a kernel resource but no
	// backing volume. They must be torn down too, or the config and minor/port
	// linger in the kernel as an orphan that blocks reusing them later.
	allHosts := append(append([]string(nil), hosts...), rm.disklessHosts(ctx, name)...)

	// Best-effort unmount of the resource's DRBD devices so a mounted resource
	// can be brought down — drbdadm down fails on a busy (mounted) device,
	// which would otherwise leave the mount and block teardown.
	_, _ = rm.deployment.Exec(ctx, allHosts,
		fmt.Sprintf("for d in /dev/drbd/by-res/%s/*; do sudo umount \"$d\" 2>/dev/null; done; true", name))

	// 1. Down resource on all nodes (diskful + diskless tiebreaker)
	downResult, err := rm.deployment.DRBDDown(ctx, allHosts, name)
	if err != nil {
		return fmt.Errorf("failed to bring down resource: %w", err)
	}

	if !downResult.AllSuccess() && !force {
		return fmt.Errorf("resource down failed on hosts: %s", downResult.FailureDetails())
	}

	// 1a. WAN only: tear down the per-resource sds-proxy pair AFTER `drbdadm
	// down` (the proxy must outlive DRBD's connection, mirroring the
	// Provision-before-up ordering). Best-effort: a failure here must not block
	// the delete, matching the state-LV sweep below. Gated behind WANMode.
	if rm.controller.db != nil {
		if dbRes, derr := rm.controller.db.GetResource(ctx, name); derr == nil && dbRes != nil && dbRes.WANMode {
			primaryAddrs, drAddr := rm.wanPrimaryAddrs(dbRes)
			multi := wanproxy.MultiSpec{
				Resource:         name,
				PrimaryNodeAddrs: primaryAddrs,
				DRNodeAddr:       drAddr,
				BaseWANPort:      dbRes.WANPort,
				BaseDRBDPort:     dbRes.Port,
			}
			if derr := wanproxy.DeprovisionMulti(ctx, rm.wanproxyDeployClient(), multi); derr != nil {
				rm.controller.logger.Warn("Best-effort WAN proxy deprovision failed during resource delete",
					zap.String("resource", name), zap.Error(derr))
			} else {
				rm.controller.logger.Info("Deprovisioned WAN replication proxy",
					zap.String("resource", name))
			}
		}
	}

	// 2. Delete config file from all nodes
	err = rm.deployment.DeleteConfig(ctx, allHosts, fmt.Sprintf("/etc/drbd.d/%s.res", name))
	if err != nil {
		return fmt.Errorf("failed to delete config: %w", err)
	}

	// 3. Delete backing volumes. This must happen BEFORE the database
	// records go away — they are the only remaining knowledge of which
	// LVs/zvols belong to this resource. Failures abort unless force is
	// set, so the records survive for a retry instead of leaking storage.
	if rm.controller.db != nil {
		volumes, listErr := rm.controller.db.ListVolumes(ctx, name)
		if listErr != nil {
			rm.controller.logger.Warn("Failed to list volumes for backing cleanup",
				zap.String("resource", name),
				zap.Error(listErr))
		}
		for _, volume := range volumes {
			if err := rm.deleteBackingVolume(ctx, hosts, volume); err != nil {
				if !force {
					return fmt.Errorf("failed to remove backing volume %s/%s (rerun with force to skip): %w",
						volume.Pool, volume.VolumeName, err)
				}
				rm.controller.logger.Warn("Failed to remove backing volume (force: continuing)",
					zap.String("resource", name),
					zap.String("volume", volume.VolumeName),
					zap.Error(err))
			}
		}

		// Sweep any orphaned gateway state-volume LVs. A gateway auto-provisions
		// cluster-private volumes named "<res>_state<N>". If such an add did not
		// finish (or its DB record was lost), the volume above cannot see it and
		// its LV would leak on the diskful nodes. Enumerate each backing pool for
		// leftover "<res>_state*" LVs and remove them. Best-effort: a failure
		// here must not block the delete.
		poolsSwept := make(map[string]bool)
		for _, volume := range volumes {
			if volume.Pool == "" || poolsSwept[volume.Pool] || strings.HasPrefix(volume.Device, "/dev/zvol/") {
				continue
			}
			poolsSwept[volume.Pool] = true
			sweepCmd := fmt.Sprintf(
				"for lv in $(sudo lvs --noheadings -o lv_name %s 2>/dev/null | tr -d ' ' | grep -E '^%s_state[0-9]+$'); do sudo lvremove -f %s/$lv; done; true",
				volume.Pool, name, volume.Pool)
			if _, err := rm.deployment.Exec(ctx, hosts, sweepCmd); err != nil {
				rm.controller.logger.Warn("Best-effort gateway state-volume LV sweep failed",
					zap.String("resource", name),
					zap.String("pool", volume.Pool),
					zap.Error(err))
			}
		}

		for _, volume := range volumes {
			if err := rm.controller.db.DeleteVolume(ctx, name, volume.VolumeName); err != nil {
				rm.controller.logger.Warn("Failed to delete volume from database",
					zap.String("resource", name),
					zap.String("volume", volume.VolumeName),
					zap.Error(err))
			}
		}
		if err := rm.controller.db.DeleteResource(ctx, name); err != nil {
			rm.controller.logger.Warn("Failed to delete resource from database",
				zap.String("name", name),
				zap.Error(err))
		}
	}

	rm.controller.logger.Info("Resource deleted successfully",
		zap.String("name", name))

	return nil
}

// nextGlobalMinor returns the lowest unused DRBD device minor on host,
// derived from every resource config present: minors are a node-global
// namespace and drbdadm rejects configs that reuse one.
// findPortConflict returns the name of an existing DRBD resource on host that
// already binds the given TCP port, or "" if the port is free. The resource
// being created (selfName) is ignored so re-runs don't flag themselves.
func (rm *ResourceManager) findPortConflict(ctx context.Context, host string, port uint32, selfName string) (string, error) {
	cmd := fmt.Sprintf("grep -lE 'address[^;]*:%d;' /etc/drbd.d/*.res 2>/dev/null || true", port)
	result, err := rm.deployment.Exec(ctx, []string{host}, cmd)
	if err != nil {
		return "", err
	}
	for _, hr := range result.Hosts {
		for _, line := range strings.Split(hr.Output, "\n") {
			line = strings.TrimSpace(line)
			if line == "" {
				continue
			}
			base := strings.TrimSuffix(filepath.Base(line), ".res")
			if base != selfName {
				return base, nil
			}
		}
	}
	return "", nil
}

// nextGlobalMinor returns a device minor free on EVERY given host.
//
// Minors are a node-global namespace, and a resource's minor has to be free on
// all of its nodes — not just the first one. Probing a single host picks a
// minor that some *other* participating node already uses for an unrelated
// resource, and drbdadm rejects the whole config at create-md time with
// "conflicting use of device-minor". That is easy to miss while every resource
// spans the same node set, and shows up as soon as one node carries a resource
// the others do not (a WAN/DR pair, a node added later).
//
// Scanning only .res files also misses minors still held by the kernel from a
// previously-removed resource: its config is gone but the /dev/drbdN node
// lingers and the minor stays "configured", so reusing it makes create-md fail
// with "Device 'N' is configured". Take the max over both the configs and the
// live /dev/drbd* device nodes, across all hosts.
// assertMinorsFreeOn checks that every device minor `resource` uses is free on
// `host`, and returns a diagnosable error naming the squatter if not.
//
// Minors are allocated once, at create time, over the hosts the resource had
// *then*. Every later operation that pulls an additional node in — a quorum
// tiebreaker being moved, a diskless client attaching — inherits those minors
// without checking whether the incoming node already uses them for something
// else. When it does, DRBD refuses with "Minor or volume exists already
// (delete it first)" from deep inside a drbdsetup invocation, long after the
// config has been distributed. Failing here instead says which resource is in
// the way, before anything is changed.
func (rm *ResourceManager) assertMinorsFreeOn(ctx context.Context, host, resource string, minors []int) error {
	if len(minors) == 0 {
		return nil
	}
	// List every minor the node already has, with the resource that owns it.
	// drbdsetup covers minors held by the kernel even when no .res mentions
	// them (a removed resource whose device node lingers).
	cmd := "grep -H -E '^[[:space:]]*device[[:space:]]+minor' /etc/drbd.d/*.res 2>/dev/null; " +
		"sudo drbdsetup show --show-defaults 2>/dev/null | grep -E 'volume|device' || true"
	res, err := rm.deployment.Exec(ctx, []string{host}, cmd)
	if err != nil {
		// A node we cannot inspect is a node we cannot vouch for, but refusing
		// the whole operation on a transient SSH hiccup is worse than letting
		// DRBD be the backstop.
		rm.controller.logger.Warn("Could not verify device minors on node; proceeding",
			zap.String("host", host), zap.String("resource", resource), zap.Error(err))
		return nil
	}

	want := make(map[int]bool, len(minors))
	for _, m := range minors {
		want[m] = true
	}

	for _, hr := range res.Hosts {
		for _, line := range strings.Split(hr.Output, "\n") {
			minor, ok := parseAnyDeviceMinor(line)
			if !ok || !want[minor] {
				continue
			}
			// A line from `grep -H` is "<path>:<the device line>"; the path
			// names the owning resource. Our own resource re-appearing is fine
			// (a re-run of the same operation).
			owner := ""
			if idx := strings.Index(line, ".res:"); idx > 0 {
				owner = filepath.Base(line[:idx+4])
				owner = strings.TrimSuffix(owner, ".res")
			}
			if owner == resource {
				continue
			}
			if owner == "" {
				owner = "another resource or a stale device node"
			}
			return fmt.Errorf("device minor %d needed by %q is already used on %s by %s; "+
				"free it there (drbdadm down + remove its .res) or recreate %q on a free minor",
				minor, resource, host, owner, resource)
		}
	}
	return nil
}

// deviceMinorRe finds a `device ... minor N` anywhere in a line.
//
// parseDeviceMinor only matches a line that *starts* with `device`, which is
// true of a generated .res but not of the two forms this code has to read:
// `grep -H` output ("<path>:        device minor 2;") and an inline volume
// stanza ("volume 0 { device minor 3; }").
var deviceMinorRe = regexp.MustCompile(`\bdevice\b[^;{}]*\bminor\s+(\d+)`)

// parseAnyDeviceMinor extracts a device minor from anywhere in a line.
func parseAnyDeviceMinor(line string) (int, bool) {
	m := deviceMinorRe.FindStringSubmatch(line)
	if m == nil {
		return 0, false
	}
	minor, err := strconv.Atoi(m[1])
	if err != nil {
		return 0, false
	}
	return minor, true
}

// resourceMinors reports the device minors a resource's live config uses, in
// file order, deduplicated (every node's `on` stanza repeats the same minors).
func resourceMinors(config string) []int {
	var minors []int
	seen := make(map[int]bool)
	for _, line := range strings.Split(config, "\n") {
		if m, ok := parseAnyDeviceMinor(line); ok && !seen[m] {
			seen[m] = true
			minors = append(minors, m)
		}
	}
	return minors
}

func (rm *ResourceManager) nextGlobalMinor(ctx context.Context, hosts []string) (int, error) {
	if len(hosts) == 0 {
		return 0, fmt.Errorf("no hosts to allocate a device minor on")
	}
	result, err := rm.deployment.Exec(ctx, hosts,
		"cat /etc/drbd.d/*.res 2>/dev/null; ls -1d /dev/drbd[0-9]* 2>/dev/null || true")
	if err != nil {
		return 0, err
	}
	maxMinor := -1
	for _, hr := range result.Hosts {
		for _, line := range strings.Split(hr.Output, "\n") {
			if minor, ok := parseDeviceMinor(line); ok && minor > maxMinor {
				maxMinor = minor
			}
			if minor, ok := parseDevNodeMinor(line); ok && minor > maxMinor {
				maxMinor = minor
			}
		}
	}
	return maxMinor + 1, nil
}

var portLineRe = regexp.MustCompile(`:(\d+);`)

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
	result, err := rm.deployment.Exec(ctx, []string{host}, "cat /etc/drbd.d/*.res 2>/dev/null || true")
	if err != nil {
		return 0, err
	}
	text := ""
	for _, hr := range result.Hosts {
		text += hr.Output
	}
	return lowestFreePort(parsePortsFromResConfigs(text), 7000), nil
}

// parseDevNodeMinor extracts N from a DRBD device node path like
// "/dev/drbd1005", ignoring the symlink tree under /dev/drbd/.
func parseDevNodeMinor(line string) (int, bool) {
	trimmed := strings.TrimSpace(line)
	const prefix = "/dev/drbd"
	if !strings.HasPrefix(trimmed, prefix) {
		return 0, false
	}
	rest := strings.TrimPrefix(trimmed, prefix)
	if rest == "" || !isAllDigits(rest) {
		return 0, false
	}
	n, err := strconv.Atoi(rest)
	if err != nil {
		return 0, false
	}
	return n, true
}

func isAllDigits(s string) bool {
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// parseDeviceMinor extracts N from DRBD config lines like
// "device minor 7;" or "device /dev/drbd7 minor 7;" regardless of spacing.
func parseDeviceMinor(line string) (int, bool) {
	trimmed := strings.TrimSpace(line)
	if !strings.HasPrefix(trimmed, "device") || !strings.Contains(trimmed, "minor") {
		return 0, false
	}
	fields := strings.Fields(trimmed)
	for i, f := range fields {
		if f == "minor" && i+1 < len(fields) {
			if minor, err := strconv.Atoi(strings.TrimSuffix(fields[i+1], ";")); err == nil {
				return minor, true
			}
		}
	}
	return 0, false
}

// autoSelectPool returns the single registered pool name, or an error when
// the choice would be ambiguous (zero or multiple distinct pool names).
func (rm *ResourceManager) autoSelectPool(ctx context.Context) (string, error) {
	if rm.controller.db == nil {
		return "", fmt.Errorf("no pool specified and database not available for auto-selection")
	}
	pools, err := rm.controller.db.ListPools(ctx)
	if err != nil {
		return "", fmt.Errorf("no pool specified and pool lookup failed: %w", err)
	}
	names := make(map[string]bool)
	for _, p := range pools {
		names[p.Name] = true
	}
	if len(names) == 1 {
		for name := range names {
			return name, nil
		}
	}
	if len(names) == 0 {
		return "", fmt.Errorf("no pool specified and no pools are registered; create one with 'pool create'")
	}
	choices := make([]string, 0, len(names))
	for name := range names {
		choices = append(choices, name)
	}
	return "", fmt.Errorf("no pool specified and multiple pools exist (%v); pass --pool", choices)
}

// deleteBackingVolume removes a volume's backing LV or zvol on all hosts.
// The device path recorded at creation time identifies the storage type:
// "/dev/zvol/<pool>/<vol>" is ZFS, "/dev/<pool>/<vol>" is LVM.
func (rm *ResourceManager) deleteBackingVolume(ctx context.Context, hosts []string, volume *database.Volume) error {
	if volume.Pool == "" || volume.VolumeName == "" {
		return fmt.Errorf("volume record incomplete (pool=%q, volume=%q)", volume.Pool, volume.VolumeName)
	}
	// An encrypted volume's container has to go first, for two reasons: an open
	// container holds the LV so lvremove refuses, and the key is the only thing
	// making the ciphertext left in the freed extents unreadable. Destroying it
	// before the storage is released is what turns "deleted" into "gone".
	if luksIsMapperPath(volume.Device) && rm.resourceIsEncrypted(ctx, volume.ResourceName) {
		rm.closeBackingVolumeOn(ctx, hosts, volume.Pool, volume.VolumeName)
	}

	var cmd string
	if strings.HasPrefix(volume.Device, "/dev/zvol/") {
		cmd = fmt.Sprintf("sudo zfs destroy %s/%s", volume.Pool, volume.VolumeName)
	} else {
		// Two things block removal of the origin LV: (1) any LVM snapshot of it
		// (e.g. scheduled snapshots) must go first, or lvremove reports the
		// origin "is used by another device"; (2) a DRBD minor may still hold
		// it, and by this point the config may be gone so drbdadm cannot help —
		// drbdsetup operates on kernel state directly. Remove snapshots, then
		// the origin, releasing the minor on the retry.
		cmd = fmt.Sprintf("for s in $(sudo lvs --noheadings -o lv_name -S origin=%s %s 2>/dev/null); do sudo lvremove -f %s/$s; done; "+
			"sudo lvremove -f %s/%s || { sudo drbdsetup down %s 2>/dev/null; sudo lvremove -f %s/%s; }",
			volume.VolumeName, volume.Pool, volume.Pool,
			volume.Pool, volume.VolumeName, volume.ResourceName, volume.Pool, volume.VolumeName)
	}
	result, err := rm.deployment.Exec(ctx, hosts, cmd)
	if err != nil {
		return err
	}
	if !result.AllSuccess() {
		// Tolerate hosts where the volume is already gone.
		for host, hr := range result.Hosts {
			if !hr.Success &&
				!strings.Contains(hr.Output, "not found") &&
				!strings.Contains(hr.Output, "does not exist") {
				return fmt.Errorf("removal failed on %s: %s", host, strings.TrimSpace(hr.Output))
			}
		}
	}
	return nil
}

// SetPrimary sets a resource to Primary on the specified node
func (rm *ResourceManager) SetPrimary(ctx context.Context, resource, node string, force bool) error {
	// Resolve node name to address
	address := rm.controller.ResolveHost(node)

	rm.controller.logger.Info("Setting resource primary",
		zap.String("resource", resource),
		zap.String("node", node),
		zap.String("address", address),
		zap.Bool("force", force))

	if rm.deployment == nil {
		return fmt.Errorf("deployment client not set")
	}

	result, err := rm.deployment.DRBDPrimary(ctx, address, resource, force)
	if err == nil && result.Success {
		return nil
	}

	// A brand-new resource comes up Inconsistent on every node with NO UpToDate
	// replica anywhere, so a normal `drbdsetup primary` fails with "Need access
	// to UpToDate data" (exit 17). This is the initial-sync case: there is no
	// good data to lose, so force-promote ONCE to establish the first UpToDate
	// copy and kick off the initial sync — after which the resource can be
	// demoted/managed normally. A resource created for a gateway never gets a
	// filesystem step (which is where the CSI path already force-primaries), so
	// without this its promote would always fail. The force is strictly scoped
	// to the no-UpToDate-anywhere case: a normal failover (some replica still
	// UpToDate) is never force-promoted here, preserving the split-brain guards
	// in PromoteForNode.
	if !force {
		needsForce, ferr := rm.resourceNeedsInitialForce(ctx, resource, address)
		if ferr != nil {
			rm.controller.logger.Warn("Could not determine initial-sync state after a failed promote; not forcing",
				zap.String("resource", resource), zap.String("node", node), zap.Error(ferr))
		} else if needsForce {
			rm.controller.logger.Warn("Resource has a volume with no UpToDate copy anywhere (fresh initial sync); force-promoting to establish UpToDate",
				zap.String("resource", resource), zap.String("node", node))
			forced, fErr := rm.deployment.DRBDPrimary(ctx, address, resource, true)
			if fErr != nil {
				return fmt.Errorf("failed to force-promote initial-sync resource on %s: %w", node, fErr)
			}
			if !forced.Success {
				return fmt.Errorf("failed to force-promote initial-sync resource on %s: %s", node, forced.Output)
			}
			return nil
		}
	}

	if err != nil {
		return fmt.Errorf("failed to set primary: %w", err)
	}
	return fmt.Errorf("failed to set primary on %s: %s", node, result.Output)
}

// resourceNeedsInitialForce reports whether a failed non-forced promote should
// be escalated to `drbdadm primary --force`, decided PER VOLUME from
// `drbdsetup status <res> --json` on the given node address. It returns true
// only when the resource is in the fresh initial-sync state — at least one local
// volume is not UpToDate AND has no UpToDate copy on any peer — AND forcing is
// safe for every volume (no volume is locally non-UpToDate while a peer holds a
// real UpToDate copy, which a blanket force would overwrite). Any failure to
// read or parse returns an error so the caller fails closed (never forces on
// uncertainty).
func (rm *ResourceManager) resourceNeedsInitialForce(ctx context.Context, resource, address string) (bool, error) {
	if rm.deployment == nil {
		return false, fmt.Errorf("deployment client not set")
	}
	res, err := rm.deployment.DRBDStatusJSON(ctx, []string{address}, resource)
	if err != nil {
		return false, fmt.Errorf("read drbd status on %s: %w", address, err)
	}
	for _, r := range res.Hosts {
		if !r.Success {
			return false, fmt.Errorf("drbd status on %s failed: %s", address, r.Output)
		}
		return safeToForceInitialSync(r.Output)
	}
	return false, fmt.Errorf("no drbd status result returned for %s", address)
}

// safeToForceInitialSync parses `drbdsetup status <res> --json` (an array of
// resources, each with per-volume devices[] and per-connection peer_devices[])
// and decides, per volume, whether a resource-level force-promote is BOTH
// needed and safe.
//
// The check is per volume — crucially, a single UpToDate volume must NOT mask a
// sibling volume that has no UpToDate data. A gateway auto-adds a state volume
// (volume 1) that becomes UpToDate during its add, while the data volume
// (volume 0) is still Inconsistent everywhere; a resource-level "is any replica
// UpToDate" test is fooled by volume 1 and wrongly refuses the force that
// volume 0 needs. For each LOCAL device:
//   - already UpToDate    -> a force cannot harm it; ignore.
//   - not UpToDate, a peer holds an UpToDate copy of THIS volume -> forcing
//     would overwrite that peer's real data from our stale copy: UNSAFE, so
//     refuse the force entirely (normal failover / resync, not initial sync).
//   - not UpToDate, no peer holds an UpToDate copy of THIS volume -> a fresh
//     unsynced volume with no data to lose: force is needed and safe for it.
//
// Returns true only if at least one volume needs the force and NO volume made it
// unsafe. Empty/unparseable input returns an error so callers fail closed.
func safeToForceInitialSync(output string) (bool, error) {
	trimmed := strings.TrimSpace(output)
	if trimmed == "" {
		return false, fmt.Errorf("empty drbdsetup status json")
	}
	var resources []drbdsetupStatus
	if err := json.Unmarshal([]byte(trimmed), &resources); err != nil {
		return false, fmt.Errorf("decode drbdsetup status json: %w", err)
	}
	if len(resources) == 0 {
		return false, fmt.Errorf("drbdsetup status json contained no resources")
	}
	res := resources[0]
	if len(res.Devices) == 0 {
		return false, fmt.Errorf("drbdsetup status json reported no local devices")
	}

	// Per volume: does any peer hold an UpToDate copy?
	peerUpToDate := make(map[int]bool)
	for _, conn := range res.Connections {
		for _, pd := range conn.PeerDevices {
			if pd.PeerDiskState == "UpToDate" {
				peerUpToDate[pd.Volume] = true
			}
		}
	}

	needForce := false
	for _, dev := range res.Devices {
		if dev.DiskState == "UpToDate" {
			continue
		}
		if peerUpToDate[dev.Volume] {
			// A peer has real UpToDate data for this volume that a blanket
			// force-primary would destroy: refuse to force the whole resource.
			return false, nil
		}
		// This volume has no UpToDate copy anywhere: fresh, nothing to lose.
		needForce = true
	}
	return needForce, nil
}

// establishInitialSync force-promotes a brand-new resource on the given diskful
// node address and immediately demotes it, so all of its volumes reach UpToDate
// and its peers get a sync source. It is only ever called right after a fresh
// create-md + up, where every replica is Inconsistent and forcing loses no data.
// After it returns, the resource is Secondary+UpToDate and can be promoted with
// a plain, non-forced promote.
func (rm *ResourceManager) establishInitialSync(ctx context.Context, resource, address string) error {
	if rm.deployment == nil {
		return fmt.Errorf("deployment client not set")
	}
	rm.controller.logger.Info("Establishing initial UpToDate generation (force-primary then demote)",
		zap.String("resource", resource), zap.String("address", address))

	forced, err := rm.deployment.DRBDPrimary(ctx, address, resource, true)
	if err != nil {
		return fmt.Errorf("force-promote for initial sync: %w", err)
	}
	if !forced.Success {
		return fmt.Errorf("force-promote for initial sync on %s: %s", address, forced.Output)
	}

	demoted, err := rm.deployment.DRBDSecondary(ctx, address, resource)
	if err != nil {
		return fmt.Errorf("demote after initial sync: %w", err)
	}
	if !demoted.Success {
		return fmt.Errorf("demote after initial sync on %s: %s", address, demoted.Output)
	}
	return nil
}

// SetSecondary sets a resource to Secondary on the specified node
func (rm *ResourceManager) SetSecondary(ctx context.Context, resource, node string) error {
	address := rm.controller.ResolveHost(node)

	rm.controller.logger.Info("Setting resource secondary",
		zap.String("resource", resource),
		zap.String("node", node),
		zap.String("address", address))

	if rm.deployment == nil {
		return fmt.Errorf("deployment client not set")
	}

	result, err := rm.deployment.DRBDSecondary(ctx, address, resource)
	if err != nil {
		return fmt.Errorf("failed to set secondary: %w", err)
	}

	if !result.Success {
		return fmt.Errorf("failed to set secondary on %s", node)
	}

	return nil
}

// PromoteForNode performs a SAFE hard-failover promote of a resource on a node.
//
// Why this exists: on a GRACEFUL move the old Primary demotes first, so a plain
// (non-forced) promote on the new node succeeds. On a HARD failure the old
// Primary is gone/uncontactable and was never demoted, so a non-forced promote
// FAILS and the volume never comes up — no automatic failover. Blindly forcing
// would risk a dual-Primary split-brain if the "failed" node is actually alive
// behind a network partition.
//
// The safe rule is DRBD-native: rely on quorum. sds configures resources with
// `quorum majority` + `on-no-quorum io-error` and auto-adds a diskless
// tiebreaker to 2-node resources, giving a 3-way majority. A hard-failed or
// partitioned old Primary that cannot reach the majority LOSES quorum and its
// DRBD blocks all I/O, so it cannot serve stale writes. Therefore it is safe to
// force-promote a surviving Secondary IFF that survivor currently holds quorum.
//
// Algorithm:
//
//	(a) try a normal, non-forced promote — the safe graceful path;
//	(b) if it fails (a peer still holds Primary / is unreachable), read this
//	    node's DRBD quorum flag via `drbdsetup status --json`;
//	(c) only if quorum == true, retry with `drbdadm primary --force`;
//	(d) if quorum == false (or unknown), REFUSE with an error — promoting
//	    without quorum could split-brain.
func (rm *ResourceManager) PromoteForNode(ctx context.Context, resource, node string) error {
	// (a) Safe path: a plain promote succeeds on a graceful move (old Primary
	// already Secondary) and on any node that can already take Primary. This
	// path is unchanged from the previous non-forced behavior.
	if err := rm.SetPrimary(ctx, resource, node, false); err == nil {
		return nil
	} else {
		rm.controller.logger.Warn("Normal promote failed; evaluating quorum before any force-promote",
			zap.String("resource", resource), zap.String("node", node), zap.Error(err))

		// (b) The promote failed — most likely a hard failover where the old
		// Primary was never demoted. Decide whether forcing is safe by checking
		// whether THIS node currently holds DRBD quorum.
		hasQuorum, qErr := rm.nodeHasQuorum(ctx, resource, node)
		if qErr != nil {
			// We cannot prove quorum, so we must not force.
			return fmt.Errorf("refusing to force-promote %s on %s: normal promote failed (%v) and DRBD quorum could not be determined: %w",
				resource, node, err, qErr)
		}
		if !hasQuorum {
			// (d) No quorum -> refuse. Forcing here could create a dual-Primary
			// split-brain if the peer that holds Primary is alive behind a
			// partition. A quorate peer, if any, is the one that should promote.
			return fmt.Errorf("refusing to force-promote %s on %s: node does NOT hold DRBD quorum (majority); forcing could cause split-brain / dual-Primary data corruption (original promote error: %v)",
				resource, node, err)
		}

		// (c) Quorum held -> safe to force. Any old/partitioned Primary that lost
		// quorum is blocked from I/O by on-no-quorum=io-error and cannot serve
		// stale writes, so this node can safely become the sole Primary.
		rm.controller.logger.Warn("Node holds DRBD quorum; force-promoting for hard failover",
			zap.String("resource", resource), zap.String("node", node))
		if fErr := rm.SetPrimary(ctx, resource, node, true); fErr != nil {
			return fmt.Errorf("force-promote %s on %s (quorum held): %w", resource, node, fErr)
		}
		return nil
	}
}

// nodeHasQuorum reports whether the given node currently holds DRBD quorum for
// the resource, read live from `drbdsetup status <res> --json` on that node.
// Any failure to read or parse the status returns an error (never a false
// "has quorum"), so callers guarding a force-promote fail closed.
func (rm *ResourceManager) nodeHasQuorum(ctx context.Context, resource, node string) (bool, error) {
	if rm.deployment == nil {
		return false, fmt.Errorf("deployment client not set")
	}
	address := rm.controller.ResolveHost(node)
	res, err := rm.deployment.DRBDStatusJSON(ctx, []string{address}, resource)
	if err != nil {
		return false, fmt.Errorf("read drbd status on %s: %w", node, err)
	}
	for _, r := range res.Hosts {
		if !r.Success {
			return false, fmt.Errorf("drbd status on %s failed: %s", node, r.Output)
		}
		return localNodeHasQuorum(r.Output)
	}
	return false, fmt.Errorf("no drbd status result returned for %s", node)
}

// localNodeHasQuorum parses `drbdsetup status <res> --json` and reports whether
// the local (queried) node holds quorum. It requires every local device to
// explicitly report quorum:true; if any device is missing the field or reports
// false, it returns false so a guarded force-promote fails closed.
func localNodeHasQuorum(output string) (bool, error) {
	trimmed := strings.TrimSpace(output)
	if trimmed == "" {
		return false, fmt.Errorf("empty drbdsetup status json")
	}
	var resources []drbdsetupStatus
	if err := json.Unmarshal([]byte(trimmed), &resources); err != nil {
		return false, fmt.Errorf("decode drbdsetup status json: %w", err)
	}
	if len(resources) == 0 {
		return false, fmt.Errorf("drbdsetup status json contained no resources")
	}
	res := resources[0]
	if len(res.Devices) == 0 {
		return false, fmt.Errorf("drbdsetup status json reported no local devices")
	}
	for _, dev := range res.Devices {
		if dev.Quorum == nil || !*dev.Quorum {
			return false, nil
		}
	}
	return true, nil
}

// RemoveVolume removes a volume from a DRBD resource
func (rm *ResourceManager) RemoveVolume(ctx context.Context, resource string, volumeID uint32) error {
	rm.controller.logger.Info("Removing volume from resource",
		zap.String("resource", resource),
		zap.Uint32("volume_id", volumeID))

	if rm.deployment == nil {
		return fmt.Errorf("deployment client not set")
	}

	if volumeID == 0 {
		return fmt.Errorf("removing volume 0 is not supported")
	}

	hosts, err := rm.resourceHosts(ctx, resource)
	if err != nil {
		return err
	}
	// The diskless participants' copies of the config lose the volume too.
	allHosts := append(append([]string(nil), hosts...), rm.disklessParticipantHosts(ctx, resource)...)

	result, err := rm.deployment.Exec(ctx, []string{hosts[0]}, fmt.Sprintf("cat /etc/drbd.d/%s.res", resource))
	if err != nil {
		return fmt.Errorf("failed to read config: %w", err)
	}

	var hostResult *deployment.HostResult
	for _, r := range result.Hosts {
		hostResult = r
		break
	}
	if hostResult == nil || !hostResult.Success {
		return fmt.Errorf("failed to get config")
	}

	configContent := hostResult.Output
	volumes := parseResourceConfigVolumes(configContent)
	var target *resourceConfigVolume
	for i := range volumes {
		if volumes[i].VolumeID == int(volumeID) {
			target = &volumes[i]
			break
		}
	}
	if target == nil {
		return fmt.Errorf("volume %d not found", volumeID)
	}

	lines := strings.Split(configContent, "\n")
	start := target.StartLine
	if start > 0 && strings.TrimSpace(lines[start-1]) == "" {
		start--
	}
	updatedLines := append([]string{}, lines[:start]...)
	updatedLines = append(updatedLines, lines[target.EndLine+1:]...)
	newConfig := removeDisklessVolumeOverrides(strings.Join(updatedLines, "\n"), int(volumeID))

	if _, err := rm.deployment.DistributeConfig(ctx, allHosts, newConfig, fmt.Sprintf("/etc/drbd.d/%s.res", resource)); err != nil {
		return fmt.Errorf("failed to distribute updated config: %w", err)
	}

	adjustRes, err := rm.deployment.Exec(ctx, allHosts, fmt.Sprintf("sudo drbdadm adjust %s", resource))
	if err != nil {
		return fmt.Errorf("failed to adjust resource after config update: %w", err)
	}
	if !adjustRes.AllSuccess() {
		return fmt.Errorf("drbdadm adjust failed after removing volume %d: %s", volumeID, adjustRes.FailureDetails())
	}

	if strings.HasPrefix(target.DiskPath, "/dev/zvol/") {
		dataset := strings.TrimPrefix(target.DiskPath, "/dev/zvol/")
		zfsRes, err := rm.deployment.ZFSDestroyDataset(ctx, hosts, dataset)
		if err != nil {
			return fmt.Errorf("failed to delete ZFS backing volume: %w", err)
		}
		if !zfsRes.AllSuccess() {
			return fmt.Errorf("ZFS backing volume removal failed: %s", zfsRes.FailureDetails())
		}
	} else {
		// An encrypted volume is addressed as /dev/mapper/... in the config but
		// LVM only understands the LV beneath it, so resolve one to the other
		// and close the container first — it holds the LV open.
		lvPath, cryptPool, cryptVolume, rerr := rm.backingLVFor(ctx, resource, volumeID, target.DiskPath)
		if rerr != nil {
			return rerr
		}
		if cryptPool != "" {
			rm.closeBackingVolumeOn(ctx, hosts, cryptPool, cryptVolume)
		}

		// A failed lvremove on any node leaves an orphan and a lopsided DRBD
		// resource, so surface per-host failures instead of only transport
		// errors. Detaching the just-removed volume's minor releases the LV if
		// the kernel still holds it after the adjust.
		removeCmd := fmt.Sprintf("sudo lvremove -f %s || { sudo drbdsetup detach %s/%d 2>/dev/null; sudo lvremove -f %s; }",
			lvPath, resource, volumeID, lvPath)
		rmRes, err := rm.deployment.Exec(ctx, hosts, removeCmd)
		if err != nil {
			return fmt.Errorf("failed to delete LVM backing volume: %w", err)
		}
		if !rmRes.AllSuccess() {
			return fmt.Errorf("LVM backing volume removal failed: %s", rmRes.FailureDetails())
		}
	}

	if rm.controller.db != nil {
		dbVolumes, err := rm.controller.db.ListVolumes(ctx, resource)
		if err == nil {
			if volume := findVolumeRecord(dbVolumes, volumeID); volume != nil {
				if err := rm.controller.db.DeleteVolume(ctx, resource, volume.VolumeName); err != nil {
					rm.controller.logger.Warn("Failed to delete volume metadata",
						zap.String("resource", resource),
						zap.String("volume", volume.VolumeName),
						zap.Error(err))
				}
			}
		}
	}

	return nil
}

// ResizeVolume resizes a DRBD volume
// hostsBelowLVSize returns the subset of hosts whose logical volume at lvPath is
// SMALLER than wantGB (or whose size could not be read). It distinguishes
// "lvresize refused because the volume is already this big" — harmless, and the
// normal state when retrying a resize whose DRBD step failed — from a real
// failure, without parsing LVM's (localised) error text.
func (rm *ResourceManager) hostsBelowLVSize(ctx context.Context, hosts []string, lvPath string, wantGB uint64) ([]string, error) {
	if len(hosts) == 0 {
		return nil, nil
	}

	cmd := fmt.Sprintf("sudo lvs --noheadings --nosuffix --units b -o lv_size %s", lvPath)
	res, err := rm.deployment.Exec(ctx, hosts, cmd)
	if err != nil {
		return nil, err
	}

	want := wantGB * 1024 * 1024 * 1024
	var short []string
	for host, hr := range res.Hosts {
		if hr == nil || !hr.Success {
			short = append(short, host)
			continue
		}
		got, perr := strconv.ParseUint(strings.TrimSpace(hr.Output), 10, 64)
		if perr != nil || got < want {
			short = append(short, host)
		}
	}
	sort.Strings(short)
	return short, nil
}

// firstFailureOutput returns the output of one failed host, for error messages
// that would otherwise carry only a list of addresses.
func firstFailureOutput(res *deployment.ExecResult) string {
	if res == nil {
		return ""
	}
	for _, host := range res.FailedHosts() {
		if hr := res.Hosts[host]; hr != nil {
			if out := strings.TrimSpace(hr.Output); out != "" {
				return out
			}
		}
	}
	return "no output"
}

func (rm *ResourceManager) ResizeVolume(ctx context.Context, resource string, volumeID uint32, newSizeGB uint64) error {
	rm.controller.logger.Info("Resizing volume",
		zap.String("resource", resource),
		zap.Uint32("volume_id", volumeID),
		zap.Uint64("new_size_gb", newSizeGB))

	if rm.deployment == nil {
		return fmt.Errorf("deployment client not set")
	}

	hosts, err := rm.resourceHosts(ctx, resource)
	if err != nil {
		return err
	}

	result, err := rm.deployment.Exec(ctx, []string{hosts[0]}, fmt.Sprintf("cat /etc/drbd.d/%s.res", resource))
	if err != nil {
		return fmt.Errorf("failed to read config: %w", err)
	}

	var hostResult *deployment.HostResult
	for _, r := range result.Hosts {
		hostResult = r
		break
	}
	if hostResult == nil || !hostResult.Success {
		return fmt.Errorf("failed to get config")
	}

	var target *resourceConfigVolume
	for _, volume := range parseResourceConfigVolumes(hostResult.Output) {
		if volume.VolumeID == int(volumeID) {
			v := volume
			target = &v
			break
		}
	}
	if target == nil {
		return fmt.Errorf("volume %d not found", volumeID)
	}

	sizeArg := fmt.Sprintf("%dG", newSizeGB)
	if strings.HasPrefix(target.DiskPath, "/dev/zvol/") {
		volumePath := strings.TrimPrefix(target.DiskPath, "/dev/zvol/")
		zfsRes, err := rm.deployment.ZFSResizeVolume(ctx, hosts, volumePath, sizeArg)
		if err != nil {
			return fmt.Errorf("failed to resize ZFS backing volume: %w", err)
		}
		if !zfsRes.AllSuccess() {
			return fmt.Errorf("ZFS backing volume resize failed: %s", zfsRes.FailureDetails())
		}
	} else {
		// With a crypt layer the LV and the mapping are two different devices
		// and both have to grow: lvresize addresses the LV, and the mapping —
		// which is what DRBD measures — stays at its old size until cryptsetup
		// is told, so skipping that step makes `drbdadm resize` find nothing
		// new and report success on a resize that did not happen.
		lvPath, cryptPool, cryptVolume, rerr := rm.backingLVFor(ctx, resource, volumeID, target.DiskPath)
		if rerr != nil {
			return rerr
		}
		if cryptPool != "" {
			// The crypt header lives at the front of the LV and is not part of
			// the mapping, so the LV must be grown by that much more for the
			// DRBD device to reach the requested size.
			sizeArg = fmt.Sprintf("%dB", newSizeGB*1024*1024*1024+luksHeaderBytes)
		}
		resizeCmd := fmt.Sprintf("sudo lvresize -L %s -y %s", sizeArg, lvPath)
		lvRes, err := rm.deployment.Exec(ctx, hosts, resizeCmd)
		if err != nil {
			return fmt.Errorf("failed to resize LVM backing volume: %w", err)
		}
		if !lvRes.AllSuccess() {
			// lvresize EXITS NON-ZERO when the LV is already the requested size.
			// That matters because this operation is not atomic: if the DRBD
			// step below fails (e.g. the volume is still doing its initial sync,
			// where DRBD refuses to resize), the LVs are already grown. Without
			// this check every later retry would fail here forever and the
			// volume could never be resized again.
			short, verr := rm.hostsBelowLVSize(ctx, lvRes.FailedHosts(), lvPath, newSizeGB)
			if verr != nil {
				return fmt.Errorf("LVM backing volume resize failed on %v (size could not be verified: %w)",
					lvRes.FailedHosts(), verr)
			}
			if len(short) > 0 {
				return fmt.Errorf("LVM backing volume resize failed on %v: %s",
					short, firstFailureOutput(lvRes))
			}
			rm.controller.logger.Info("LVM backing volume was already at the requested size",
				zap.String("resource", resource),
				zap.Uint64("size_gb", newSizeGB),
				zap.Strings("hosts", lvRes.FailedHosts()))
		}

		// Grow the mapping onto the extents the LV just gained. Idempotent, so
		// it is safe on the retry path the LV branch above exists to allow.
		if cryptPool != "" {
			cmd, cerr := luksResizeCmd(cryptPool, cryptVolume)
			if cerr != nil {
				return cerr
			}
			if cerr := execFailure(rm.deployment.Exec(ctx, hosts, cmd)); cerr != nil {
				return fmt.Errorf("grow the LUKS container of %s/%d onto the new extents: %w",
					resource, volumeID, cerr)
			}
		}
	}

	drbdRes, err := rm.deployment.Exec(ctx, []string{hosts[0]}, fmt.Sprintf("sudo drbdadm resize %s/%d", resource, volumeID))
	if err != nil {
		return fmt.Errorf("failed to resize DRBD volume: %w", err)
	}
	if !drbdRes.AllSuccess() {
		// Include the command output: the usual cause is that the volume is not
		// UpToDate everywhere yet (DRBD refuses to resize mid-resync), and the
		// bare host list gives the operator no way to know that waiting fixes
		// it. The backing LVs are already grown at this point, so a retry once
		// the resync finishes completes the resize.
		return fmt.Errorf("DRBD volume resize failed on %v: %s",
			drbdRes.FailedHosts(), firstFailureOutput(drbdRes))
	}

	if rm.controller.db != nil {
		dbVolumes, err := rm.controller.db.ListVolumes(ctx, resource)
		if err == nil {
			if volume := findVolumeRecord(dbVolumes, volumeID); volume != nil {
				volume.SizeGB = int(newSizeGB)
				if err := rm.controller.db.SaveVolume(ctx, volume); err != nil {
					rm.controller.logger.Warn("Failed to update volume metadata",
						zap.String("resource", resource),
						zap.String("volume", volume.VolumeName),
						zap.Error(err))
				}
			}
		}
	}

	return nil
}

// Mount mounts a DRBD device
func (rm *ResourceManager) Mount(ctx context.Context, resource, mountPoint string, volumeID uint32, node, fsType string) error {
	// Resolve node to address; empty means the current Primary.
	address, err := rm.resolveNodeOrPrimary(ctx, resource, node)
	if err != nil {
		return fmt.Errorf("resolve target node: %w", err)
	}

	rm.controller.logger.Info("Mounting resource",
		zap.String("resource", resource),
		zap.String("mount_point", mountPoint),
		zap.Uint32("volume_id", volumeID),
		zap.String("node", node),
		zap.String("address", address),
		zap.String("fstype", fsType))

	if rm.deployment == nil {
		return fmt.Errorf("deployment client not set")
	}

	drbdDevice := fmt.Sprintf("/dev/drbd/by-res/%s/%d", resource, volumeID)

	// Create mount point
	mkdirCmd := fmt.Sprintf("sudo mkdir -p %s", mountPoint)
	_, err = rm.deployment.Exec(ctx, []string{address}, mkdirCmd)
	if err != nil {
		return fmt.Errorf("failed to create mount point: %w", err)
	}

	// Mount
	mountCmd := fmt.Sprintf("sudo mount %s %s", drbdDevice, mountPoint)
	result, err := rm.deployment.Exec(ctx, []string{address}, mountCmd)
	if err != nil {
		return fmt.Errorf("failed to mount: %w", err)
	}
	if !result.AllSuccess() {
		return fmt.Errorf("mount failed on %s: %v", node, result.FailedHosts())
	}

	return nil
}

// Unmount unmounts a DRBD device
func (rm *ResourceManager) Unmount(ctx context.Context, resource string, volumeID uint32, node string) error {
	// Resolve node to address; empty means the current Primary.
	address, err := rm.resolveNodeOrPrimary(ctx, resource, node)
	if err != nil {
		return fmt.Errorf("resolve target node: %w", err)
	}

	rm.controller.logger.Info("Unmounting resource",
		zap.String("resource", resource),
		zap.Uint32("volume_id", volumeID),
		zap.String("node", node),
		zap.String("address", address))

	if rm.deployment == nil {
		return fmt.Errorf("deployment client not set")
	}

	// Unmount by device path is safer if we know volume ID
	drbdDevice := fmt.Sprintf("/dev/drbd/by-res/%s/%d", resource, volumeID)

	umountCmd := fmt.Sprintf("sudo umount %s", drbdDevice)
	result, err := rm.deployment.Exec(ctx, []string{address}, umountCmd)
	if err != nil {
		return fmt.Errorf("failed to unmount: %w", err)
	}
	if !result.AllSuccess() {
		return fmt.Errorf("unmount failed on %s: %v", node, result.FailedHosts())
	}

	return nil
}

// generateSystemdMountUnit generates a systemd mount unit content
func (rm *ResourceManager) generateSystemdMountUnit(resource, mountPoint, fsType string) string {
	device := fmt.Sprintf("/dev/drbd/by-res/%s/0", resource)
	return fmt.Sprintf(`[Unit]
Description=Mount for %s
[Mount]
What=%s
Where=%s
Type=%s
[Install]
WantedBy=multi-user.target
`, resource, device, mountPoint, fsType)
}

// MakeHa creates a drbd-reactor promoter config for HA failover. Any ocfAgents
// are appended, in order, to the promoter start[] list after the built-in
// mount/vip/services entries. Passing nil keeps the historical behavior.
func (rm *ResourceManager) MakeHa(ctx context.Context, resource string, services []string, mountPoint, fsType, vip string, ocfAgents []OcfAgentSpec, startItems []HaStartItem) (string, error) {
	rm.controller.logger.Info("Making resource HA",
		zap.String("resource", resource),
		zap.Strings("services", services),
		zap.String("mount_point", mountPoint),
		zap.String("fstype", fsType),
		zap.String("vip", vip))

	if rm.deployment == nil {
		return "", fmt.Errorf("deployment client not set")
	}
	if rm.controller.db == nil {
		return "", fmt.Errorf("database not available")
	}

	// Get resource info to find nodeAddresses
	dbResource, err := rm.controller.db.GetResource(ctx, resource)
	if err != nil {
		return "", fmt.Errorf("failed to get resource from database: %w", err)
	}

	if dbResource == nil {
		return "", fmt.Errorf("resource not found: %s", resource)
	}

	nodeNames := strings.Split(dbResource.Nodes, ",")
	if len(nodeNames) == 0 {
		return "", fmt.Errorf("no nodes found for resource")
	}

	// Convert node names to addresses for deployment
	nodeAddresses := make([]string, len(nodeNames))
	for i, nodeName := range nodeNames {
		addr := rm.controller.nodes.GetNodeAddressByName(nodeName)
		if addr == "" {
			return "", fmt.Errorf("failed to resolve address for node: %s", nodeName)
		}
		nodeAddresses[i] = addr
	}
	hosts := nodeAddresses

	// A VIP is served by the service-ip@ systemd template, which depends on the
	// service-ip helper. Without it the promoter target fails to start ("Unit
	// service-ip@... not found") and the HA config is silently broken. Check up
	// front on every node — the same guard Self-HA already uses.
	if strings.TrimSpace(vip) != "" {
		if err := rm.execAllSuccess(ctx, hosts, "test -x /usr/local/bin/service-ip",
			"service-ip helper is not installed at /usr/local/bin/service-ip on all nodes (required for the HA VIP)"); err != nil {
			return "", err
		}
	}

	// Step 1: Check DRBD status and ensure resource is up
	rm.controller.logger.Info("Checking DRBD resource status",
		zap.String("resource", resource),
		zap.Strings("nodes", nodeAddresses))

	// First, ensure resource is up on all nodes
	rm.controller.logger.Info("Bringing up DRBD resource on all nodes")
	_, err = rm.deployment.Exec(ctx, nodeAddresses, "sudo drbdadm up "+resource)
	if err != nil {
		rm.controller.logger.Warn("Failed to bring up resource (continuing anyway)", zap.Error(err))
	}
	// Continue anyway - resource might already be up

	statusResult, err := rm.deployment.Exec(ctx, nodeAddresses, "sudo drbdadm status "+resource)
	if err != nil {
		return "", fmt.Errorf("failed to check DRBD status: %w", err)
	}

	// Check if any node is Primary, if not, set first node as Primary
	hasPrimary := false
	for _, r := range statusResult.Hosts {
		if r.Success && strings.Contains(string(r.Output), "role:Primary") {
			hasPrimary = true
			rm.controller.logger.Info("Found existing Primary node",
				zap.String("host", r.Host))
			break
		}
	}

	if !hasPrimary {
		rm.controller.logger.Info("No Primary node found, setting first node as Primary",
			zap.String("node", nodeNames[0]),
			zap.String("address", nodeAddresses[0]))
		if err := rm.SetPrimary(ctx, resource, nodeAddresses[0], true); err != nil {
			return "", fmt.Errorf("failed to set Primary: %w", err)
		}
		rm.controller.logger.Info("Primary set successfully",
			zap.String("node", nodeNames[0]))
	}

	// Step 2: Create filesystem if mount point and fs type are specified
	if mountPoint != "" && fsType != "" {
		rm.controller.logger.Info("Creating filesystem",
			zap.String("resource", resource),
			zap.String("fstype", fsType),
			zap.String("volume", "0"))

		// Check if filesystem already exists by checking if device can be read
		drbdDevice := fmt.Sprintf("/dev/drbd/by-res/%s/0", resource)
		checkFsCmd := fmt.Sprintf("sudo blkid -o value -s TYPE %s 2>/dev/null || echo 'none'", drbdDevice)
		checkResult, err := rm.deployment.Exec(ctx, []string{nodeAddresses[0]}, checkFsCmd)

		needsFs := true
		if err == nil {
			for _, r := range checkResult.Hosts {
				if r.Success {
					fsTypeFound := strings.TrimSpace(string(r.Output))
					if fsTypeFound != "none" && fsTypeFound != "" {
						rm.controller.logger.Info("Filesystem already exists",
							zap.String("existing_fstype", fsTypeFound))
						needsFs = false
						break
					}
				}
			}
		}

		if needsFs {
			if err := rm.CreateFilesystemOnly(ctx, resource, 0, fsType, nodeAddresses[0]); err != nil {
				return "", fmt.Errorf("failed to create filesystem: %w", err)
			}
			rm.controller.logger.Info("Filesystem created successfully")
		}
	}

	// Validate that all services exist on all nodes
	// This prevents failover failures when a service is missing on a standby node
	if len(services) > 0 {
		for _, svc := range services {
			// Check if service unit file exists on all nodes
			// Use systemctl show to check LoadState - "loaded" means unit file exists
			checkCmd := fmt.Sprintf("systemctl show %s -p LoadState 2>/dev/null || echo 'not-found'", svc)
			result, err := rm.deployment.Exec(ctx, nodeAddresses, checkCmd)
			if err != nil {
				return "", fmt.Errorf("failed to check service %s on nodes: %w", svc, err)
			}

			var missingNodes []string
			for node, hr := range result.Hosts {
				output := strings.TrimSpace(hr.Output)
				// Service exists if LoadState is "loaded"
				if !strings.Contains(output, "LoadState=loaded") {
					missingNodes = append(missingNodes, node)
				}
			}

			if len(missingNodes) > 0 {
				return "", fmt.Errorf("service %s not found on nodes: %v. Please install the service on all nodes before configuring HA", svc, missingNodes)
			}

			rm.controller.logger.Info("Service validated on all nodes",
				zap.String("service", svc))
		}

		// Stop and disable services on all nodes before HA takeover
		rm.controller.logger.Info("Stopping and disabling services on all nodes for HA takeover",
			zap.Strings("services", services))

		for _, svc := range services {
			// Stop service
			stopCmd := fmt.Sprintf("systemctl stop %s", svc)
			if _, err := rm.deployment.Exec(ctx, nodeAddresses, stopCmd); err != nil {
				rm.controller.logger.Warn("Failed to stop service", zap.String("service", svc), zap.Error(err))
			}

			// Disable service
			disableCmd := fmt.Sprintf("systemctl disable %s", svc)
			if _, err := rm.deployment.Exec(ctx, nodeAddresses, disableCmd); err != nil {
				rm.controller.logger.Warn("Failed to disable service", zap.String("service", svc), zap.Error(err))
			}
		}

		// Migrate existing data to /tmp before HA takeover
		if mountPoint != "" {
			rm.controller.logger.Info("Backing up existing data before HA takeover",
				zap.String("mount_point", mountPoint))

			backupDir := fmt.Sprintf("/tmp/ha_backup_%s", strings.ReplaceAll(mountPoint, "/", "_"))

			// Use rsync or cp -a to backup all files including hidden ones and subdirectories
			backupCmd := fmt.Sprintf("if [ -d \"%s\" ]; then mkdir -p %s && rsync -a %s/ %s/ 2>/dev/null || cp -a %s/. %s/. 2>/dev/null; fi",
				mountPoint, backupDir, mountPoint, backupDir, mountPoint, backupDir)

			if _, err := rm.deployment.Exec(ctx, nodeAddresses, backupCmd); err != nil {
				rm.controller.logger.Warn("Failed to backup data (continuing anyway)",
					zap.String("mount_point", mountPoint),
					zap.Error(err))
			} else {
				rm.controller.logger.Info("Data backup completed",
					zap.String("backup_dir", backupDir))
			}
		}
	}

	// Handle mount unit creation
	if mountPoint != "" {
		mountUnitName := strings.TrimPrefix(mountPoint, "/")
		mountUnitName = strings.ReplaceAll(mountUnitName, "/", "-")
		mountUnitName = fmt.Sprintf("%s.mount", mountUnitName)

		mountContent := rm.generateSystemdMountUnit(resource, mountPoint, fsType)
		mountPath := fmt.Sprintf("/etc/systemd/system/%s", mountUnitName)

		rm.controller.logger.Info("Distributing mount unit", zap.String("path", mountPath))

		if _, err := rm.deployment.DistributeConfig(ctx, hosts, mountContent, mountPath); err != nil {
			return "", fmt.Errorf("failed to distribute mount unit: %w", err)
		}

		// Reload systemd to pick up new unit
		if _, err := rm.deployment.Exec(ctx, hosts, "systemctl daemon-reload"); err != nil {
			rm.controller.logger.Warn("Failed to reload systemd", zap.Error(err))
		}
	}

	// Generate drbd-reactor promoter config. When the caller supplied an explicit
	// ordered start[] list, honour it verbatim (systemd units and OCF agents are
	// peers in one sequence); otherwise fall back to the legacy bucketed order
	// (mount -> VIP -> services -> OCF agents) for older clients/CLI.
	configPath := fmt.Sprintf("/etc/drbd-reactor.d/sds-ha-%s.toml", resource)
	var configContent string
	if len(startItems) > 0 {
		configContent = rm.generatePromoterConfigOrdered(resource, startItems)
	} else {
		configContent = rm.generatePromoterConfig(resource, services, mountPoint, vip, ocfAgents)
	}

	rm.controller.logger.Debug("Generated promoter config",
		zap.String("config", configContent))

	// Distribute config to all hosts using DistributeConfig
	_, err = rm.deployment.DistributeConfig(ctx, hosts, configContent, configPath)
	if err != nil {
		return "", fmt.Errorf("failed to distribute promoter config: %w", err)
	}

	// Reload drbd-reactor on all hosts
	_, err = rm.deployment.ReactorReload(ctx, hosts)
	if err != nil {
		rm.controller.logger.Warn("Failed to reload drbd-reactor", zap.Error(err))
	}

	// Restore backed up data after drbd-reactor takes over
	if mountPoint != "" {
		rm.controller.logger.Info("Restoring backed up data after HA takeover",
			zap.String("mount_point", mountPoint))

		backupDir := fmt.Sprintf("/tmp/ha_backup_%s", strings.ReplaceAll(mountPoint, "/", "_"))

		// Find the active (primary) node for restoration
		activeNode, err := rm.findActiveNode(ctx, resource, hosts)
		if err != nil {
			rm.controller.logger.Warn("Failed to find active node for data restore",
				zap.Error(err))
		} else {
			rm.controller.logger.Info("Restoring data on active node",
				zap.String("active_node", activeNode),
				zap.String("backup_dir", backupDir))

			// Restore data preserving original permissions with rsync/cp -a
			// Backup is kept at /tmp/ha_backup_* for manual recovery if needed
			restoreCmd := fmt.Sprintf(
				"if [ -d \"%s\" ] && [ \"$(ls -A %s 2>/dev/null)\" ]; then "+
					"mkdir -p %s && "+
					"rsync -a %s/ %s/ 2>/dev/null || cp -a %s/. %s/. && "+
					"echo 'Data restored successfully. Backup retained at %s for manual recovery if needed.'; "+
					"else echo 'No backup found or backup is empty'; fi",
				backupDir, backupDir, mountPoint, backupDir, mountPoint, mountPoint, backupDir, backupDir)

			result, err := rm.deployment.Exec(ctx, []string{activeNode}, restoreCmd)
			if err != nil {
				rm.controller.logger.Warn("Failed to restore data",
					zap.String("active_node", activeNode),
					zap.Error(err))
			} else {
				for host, hr := range result.Hosts {
					if hr.Output != "" {
						rm.controller.logger.Info("Restore result",
							zap.String("node", host),
							zap.String("output", hr.Output))
					}
				}
			}
		}
	}

	// Save HA config to database
	if rm.controller.db != nil {
		haCfg := &database.HaConfig{
			Resource:   resource,
			VIP:        vip,
			MountPoint: mountPoint,
			FsType:     fsType,
			Services:   services,
		}
		if err := rm.controller.db.SaveHaConfig(ctx, haCfg); err != nil {
			rm.controller.logger.Warn("Failed to save HA config to database", zap.Error(err))
		}
	}

	return configPath, nil
}

// ListHaConfigs lists all HA configurations from database
func (rm *ResourceManager) ListHaConfigs(ctx context.Context) ([]*database.HaConfig, error) {
	if rm.controller.db == nil {
		return nil, fmt.Errorf("database not available")
	}
	return rm.controller.db.ListHaConfigs(ctx)
}

// GetHaConfig gets an HA configuration from database
func (rm *ResourceManager) GetHaConfig(ctx context.Context, resource string) (*database.HaConfig, error) {
	if rm.controller.db == nil {
		return nil, fmt.Errorf("database not available")
	}
	return rm.controller.db.GetHaConfig(ctx, resource)
}

// EvictHa evicts the HA resource from the active node
// drbd-reactor will handle the complete failover process:
// 1. Mask the target on active node
// 2. Stop all services (mount, VIP, etc.)
// 3. Demote DRBD to Secondary
// 4. Wait for another node to promote to Primary
func (rm *ResourceManager) EvictHa(ctx context.Context, resource string) error {
	rm.controller.logger.Info("Evicting HA resource",
		zap.String("resource", resource))

	hosts, err := rm.resourceHosts(ctx, resource)
	if err != nil {
		return err
	}

	rm.controller.logger.Info("Hosts configured",
		zap.Strings("hosts", hosts))

	// Find the active (Primary) node
	activeNode, err := rm.findActiveNode(ctx, resource, hosts)
	if err != nil {
		return fmt.Errorf("failed to find active node: %w", err)
	}

	rm.controller.logger.Info("Found active node for eviction",
		zap.String("resource", resource),
		zap.String("active_node", activeNode))

	// The config name for drbd-reactorctl (without .toml extension)
	configName := fmt.Sprintf("sds-ha-%s", resource)

	// Evicting the controller's own metadata resource stops this very
	// process mid-eviction: a synchronous drbd-reactorctl child (local or
	// SSH session) dies with us and aborts the eviction half-way. Launch it
	// detached through systemd-run on the active node and return.
	if resource == SelfHaResource {
		evictCmd := fmt.Sprintf(
			"sudo systemd-run --unit=sds-selfha-evict --collect drbd-reactorctl evict %s", configName)
		if err := rm.execAllSuccess(ctx, []string{rm.controller.ResolveHost(activeNode)}, evictCmd,
			"failed to launch detached self-eviction"); err != nil {
			return err
		}
		rm.controller.logger.Info("Detached self-eviction launched",
			zap.String("resource", resource),
			zap.String("active_node", activeNode))
		return nil
	}

	// Get local hostname to check if active node is local
	hostnameBytes, _ := exec.Command("hostname").Output()
	localHostname := strings.TrimSpace(string(hostnameBytes))

	var errExec error
	var output []byte

	if activeNode == localHostname {
		// Execute locally using os/exec
		rm.controller.logger.Info("Executing evict locally",
			zap.String("hostname", activeNode))
		cmd := exec.Command("drbd-reactorctl", "evict", configName)
		output, errExec = cmd.CombinedOutput()
		if errExec != nil {
			rm.controller.logger.Error("Local evict failed",
				zap.String("output", string(output)),
				zap.Error(errExec))
			return fmt.Errorf("failed to evict HA resource: %w, output: %s", errExec, string(output))
		}
		rm.controller.logger.Info("Local evict output",
			zap.String("output", string(output)))
	} else {
		// Execute on remote node via dispatch
		evictCmd := fmt.Sprintf("sudo drbd-reactorctl evict %s", configName)
		rm.controller.logger.Debug("Executing evict command remotely",
			zap.String("host", activeNode),
			zap.String("command", evictCmd))

		result, err := rm.deployment.Exec(ctx, []string{activeNode}, evictCmd)
		if err != nil {
			return fmt.Errorf("failed to evict HA resource: %w", err)
		}

		// Log result for debugging
		for host, hr := range result.Hosts {
			rm.controller.logger.Debug("Evict command result",
				zap.String("host", host),
				zap.Bool("success", hr.Success),
				zap.String("output", hr.Output),
				zap.Any("error", hr.Error))
		}

		if !result.AllSuccess() {
			return fmt.Errorf("evict failed: %s", result.FailureDetails())
		}
	}

	rm.controller.logger.Info("HA resource evicted successfully",
		zap.String("resource", resource))

	return nil
}

// findActiveNode finds the node where the DRBD resource is currently Primary
func (rm *ResourceManager) findActiveNode(ctx context.Context, resource string, hosts []string) (string, error) {
	rm.controller.logger.Info("findActiveNode called",
		zap.String("resource", resource),
		zap.Int("hosts_count", len(hosts)))

	// Prefer drbd-reactor's structured JSON status for HA resources when available.
	for _, host := range hosts {
		promoter, err := rm.deployment.ReactorPromoterStatusByResource(ctx, host, resource)
		if err != nil || promoter == nil {
			continue
		}
		if promoter.PrimaryOn != "" {
			if primaryHost := rm.getNodeHost(promoter.PrimaryOn); primaryHost != "" {
				rm.controller.logger.Info("Found active node from reactor status",
					zap.String("resource", resource),
					zap.String("primary_on", promoter.PrimaryOn),
					zap.String("resolved_host", primaryHost))
				return primaryHost, nil
			}
			rm.controller.logger.Info("Using reactor primary_on directly",
				zap.String("resource", resource),
				zap.String("primary_on", promoter.PrimaryOn))
			return promoter.PrimaryOn, nil
		}
	}

	var localHostname string

	// First, check local node using os/exec (not dispatch)
	// Get local hostname
	hostnameBytes, err := exec.Command("hostname").Output()
	if err != nil {
		rm.controller.logger.Warn("Failed to get local hostname", zap.Error(err))
		localHostname = ""
	} else {
		localHostname = strings.TrimSpace(string(hostnameBytes))
		rm.controller.logger.Info("Local hostname", zap.String("hostname", localHostname))

		// Check if local node is Primary
		// No need for sudo since sds-controller runs as root
		checkCmd := exec.Command("drbdsetup", "status", resource)
		output, err := checkCmd.Output()
		if err != nil {
			// Only an ExitError carries Stderr; a missing binary yields an
			// *exec.Error, so guard the type assertion (it used to panic when
			// drbdsetup was absent, e.g. in unit tests).
			var stderr string
			if ee, ok := err.(*exec.ExitError); ok {
				stderr = string(ee.Stderr)
			}
			rm.controller.logger.Warn("Failed to check local DRBD status",
				zap.Error(err),
				zap.String("stderr", stderr))
		} else {
			lines := strings.Split(string(output), "\n")
			rm.controller.logger.Info("Local DRBD status",
				zap.String("first_line", lines[0]),
				zap.Int("line_count", len(lines)))
			if len(lines) > 0 && strings.Contains(lines[0], "role:Primary") {
				rm.controller.logger.Info("Local node is Primary",
					zap.String("hostname", localHostname))
				return localHostname, nil
			}
			rm.controller.logger.Info("Local node is not Primary, checking remote hosts")
		}
	}

	rm.controller.logger.Info("Checking remote hosts",
		zap.Strings("hosts", hosts),
		zap.String("local_hostname", localHostname))

	for _, host := range hosts {
		// Skip if this is the local host
		if host == localHostname {
			rm.controller.logger.Info("Skipping local host",
				zap.String("host", host))
			continue
		}

		rm.controller.logger.Info("Checking remote host via dispatch",
			zap.String("host", host),
			zap.String("resource", resource))

		// Get DRBD role - check if this host is Primary
		cmd := fmt.Sprintf("drbdadm status %s", resource)
		result, err := rm.deployment.Exec(ctx, []string{host}, cmd)
		if err != nil {
			rm.controller.logger.Debug("Failed to check DRBD status",
				zap.String("host", host),
				zap.Error(err))
			continue
		}

		// Parse DRBD status output to find the Primary node
		if result != nil && len(result.Hosts) > 0 {
			for _, hr := range result.Hosts {
				if !hr.Success {
					continue
				}
				output := string(hr.Output)
				rm.controller.logger.Debug("Host result",
					zap.String("host", host),
					zap.Bool("success", hr.Success),
					zap.String("output", output))

				// Parse DRBD status to find which node is Primary
				// Output format:
				//   resource_name role:Secondary
				//     nodename1 role:Primary
				//     nodename2 role:Secondary
				lines := strings.Split(output, "\n")
				rm.controller.logger.Debug("Parsing DRBD status",
					zap.String("host", host),
					zap.Int("line_count", len(lines)))
				for i, line := range lines {
					trimmed := strings.TrimSpace(line)
					rm.controller.logger.Debug("Checking DRBD line",
						zap.String("host", host),
						zap.Int("line_index", i),
						zap.String("line", trimmed))
					// Check if this line defines a node's role
					// Format: "nodename role:Role" or "resource role:Role"
					if strings.Contains(trimmed, " role:Primary") {
						parts := strings.SplitN(trimmed, " ", 2)
						rm.controller.logger.Debug("Split result",
							zap.Int("parts_count", len(parts)),
							zap.String("part0", parts[0]),
							zap.String("part1", parts[1]))
						if len(parts) >= 2 && strings.HasPrefix(parts[1], "role:Primary") {
							primaryNode := strings.TrimSpace(parts[0])
							rm.controller.logger.Info("Found Primary node",
								zap.String("primary_node", primaryNode),
								zap.String("original_line", trimmed))

							// Get IP/hostname for the primary node
							primaryHost := rm.getNodeHost(primaryNode)
							if primaryHost != "" {
								rm.controller.logger.Info("Resolved primary node to host",
									zap.String("node", primaryNode),
									zap.String("host", primaryHost))
								return primaryHost, nil
							}
							// If not found in hosts map, return the node name directly
							rm.controller.logger.Info("Using node name directly",
								zap.String("node", primaryNode))
							return primaryNode, nil
						}
					}
				}
			}
		}
	}

	return "", fmt.Errorf("no active (Primary) node found for resource %s", resource)
}

// getNodeHost gets the host address for a node name
// hosts format is "nodename:ip" or just "nodename"
func (rm *ResourceManager) getNodeHost(nodeName string) string {
	if resolved := rm.controller.ResolveHost(nodeName); resolved != nodeName {
		return resolved
	}

	rm.mu.RLock()
	defer rm.mu.RUnlock()

	for _, host := range rm.hosts {
		// Check if host matches nodename:ip format
		if strings.Contains(host, ":") {
			parts := strings.SplitN(host, ":", 2)
			if parts[0] == nodeName {
				return host
			}
		} else if host == nodeName {
			return host
		}
	}
	return ""
}

// RemoveHa removes HA configuration for a resource
func (rm *ResourceManager) RemoveHa(ctx context.Context, resource string) error {
	rm.controller.logger.Info("Removing HA configuration", zap.String("resource", resource))

	if rm.deployment == nil {
		return fmt.Errorf("deployment client not set")
	}
	if rm.controller.db == nil {
		return fmt.Errorf("database not available")
	}

	hosts, err := rm.resourceHosts(ctx, resource)
	if err != nil {
		return err
	}

	// Get HA config to know what to clean up
	haCfg, err := rm.controller.db.GetHaConfig(ctx, resource)
	if err != nil {
		return fmt.Errorf("failed to get HA config: %w", err)
	}

	// 1. Delete promoter config
	configPath := fmt.Sprintf("/etc/drbd-reactor.d/sds-ha-%s.toml", resource)
	if err := rm.deployment.DeleteConfig(ctx, hosts, configPath); err != nil {
		rm.controller.logger.Warn("Failed to delete promoter config", zap.Error(err))
	}

	// 2. Delete mount unit if it exists
	if haCfg.MountPoint != "" {
		mountUnitName := strings.TrimPrefix(haCfg.MountPoint, "/")
		mountUnitName = strings.ReplaceAll(mountUnitName, "/", "-")
		mountUnitName = fmt.Sprintf("%s.mount", mountUnitName)
		mountPath := fmt.Sprintf("/etc/systemd/system/%s", mountUnitName)

		if err := rm.deployment.DeleteConfig(ctx, hosts, mountPath); err != nil {
			rm.controller.logger.Warn("Failed to delete mount unit", zap.Error(err))
		}
	}

	// 3. Reload daemons
	if _, err := rm.deployment.Exec(ctx, hosts, "systemctl daemon-reload && systemctl reload drbd-reactor"); err != nil {
		rm.controller.logger.Warn("Failed to reload daemons", zap.Error(err))
	}

	// 3b. Explicitly bring the VIP down. Removing the promoter config and
	// reloading drbd-reactor does NOT reliably stop the units reactor already
	// started, and the VIP's service-ip@ unit is Type=oneshot with
	// RemainAfterExit=yes, so without an explicit stop the floating IP lingers
	// on whichever node was Primary. Run it on every resource node (we do not
	// know which one held the VIP) after the config is gone so reactor cannot
	// restart it. Idempotent: stopping an inactive/absent template instance is
	// a no-op, and any failure is only a warning so teardown still completes.
	if inst := vipServiceIPInstance(haCfg.VIP); inst != "" {
		stopCmd := fmt.Sprintf("systemctl stop service-ip@%s.service", inst)
		result, err := rm.deployment.Exec(ctx, hosts, stopCmd)
		if err != nil {
			rm.controller.logger.Warn("Failed to stop VIP service-ip unit",
				zap.String("vip", haCfg.VIP), zap.Error(err))
		} else if result != nil && !result.AllSuccess() {
			rm.controller.logger.Warn("VIP service-ip unit may still be up on some nodes",
				zap.String("vip", haCfg.VIP), zap.Strings("failed_hosts", result.FailedHosts()))
		}
	}

	// 4. Remove from database
	if err := rm.controller.db.DeleteHaConfig(ctx, resource); err != nil {
		return fmt.Errorf("failed to delete HA config from database: %w", err)
	}

	return nil
}

// vipServiceIPInstance derives the systemd template instance name for a VIP's
// service-ip@ unit from its (CIDR) string, matching how the promoter config is
// generated so teardown can target the exact same unit:
//
//	"192.168.1.50/24" -> "192.168.1.50-24"   (service-ip@192.168.1.50-24.service)
//	"192.168.1.50"     -> "192.168.1.50-32"   (bare IP defaults to /32)
//
// It returns "" for an empty vip so callers can skip VIP handling. This is the
// single source of truth for the instance name; generatePromoterConfig,
// RemoveHa's VIP-down step and the self-HA disable script all go through it.
func vipServiceIPInstance(vip string) string {
	vip = strings.TrimSpace(vip)
	if vip == "" {
		return ""
	}
	inst := strings.ReplaceAll(vip, "/", "-")
	if !strings.Contains(inst, "-") {
		inst = inst + "-32"
	}
	return inst
}

// generatePromoterConfig generates drbd-reactor promoter TOML config
// generatePromoterConfigOrdered renders the promoter config from an explicit,
// ordered start[] list where systemd/mount units and OCF agents are peers. It
// emits each item in the given order with no bucketing, so a correct stack
// (portblock -> Filesystem -> IPaddr2 -> service -> exportfs -> portunblock) is
// expressible. The TOML wrapper is byte-identical to generatePromoterConfig.
func (rm *ResourceManager) generatePromoterConfigOrdered(resource string, items []HaStartItem) string {
	startActions := make([]string, 0, len(items))
	for _, it := range items {
		entry := renderStartItem(it)
		if entry == "" {
			continue
		}
		startActions = append(startActions, fmt.Sprintf(`  "%s"`, entry))
	}

	return fmt.Sprintf(`# drbd-reactor promoter configuration for HA resource: %s
# Generated by sds-controller

[[promoter]]
[promoter.resources.%s]
runner = "systemd"
start = [
%s
]
on-drbd-demote-failure = "reboot"

`, resource, resource, strings.Join(startActions, ",\n"))
}

func (rm *ResourceManager) generatePromoterConfig(resource string, services []string, mountPoint, vip string, ocfAgents []OcfAgentSpec) string {
	var startActions []string

	// Add mount unit if mount point specified
	if mountPoint != "" {
		// Generate systemd mount unit name from path
		// e.g., /var/lib/sds -> var-lib-sds.mount
		mountUnit := strings.TrimPrefix(mountPoint, "/")
		mountUnit = strings.ReplaceAll(mountUnit, "/", "-")
		mountUnit = fmt.Sprintf("\"%s.mount\"", mountUnit)

		startActions = append(startActions, mountUnit)
	}

	// Add VIP if specified
	if inst := vipServiceIPInstance(vip); inst != "" {
		// Use service-ip systemd unit: service-ip@<IP>-<MASK>.service
		serviceIPUnit := fmt.Sprintf("\"service-ip@%s.service\"", inst)
		startActions = append(startActions, serviceIPUnit)
	}

	// Add systemd services
	for _, svc := range services {
		startActions = append(startActions, fmt.Sprintf(`  "%s"`, svc))
	}

	// Append any extra OCF resource agents, in order, after the built-in
	// mount/vip/services items.
	for _, agent := range ocfAgents {
		entry := renderOcfStartEntry(agent)
		if entry == "" {
			continue
		}
		startActions = append(startActions, fmt.Sprintf(`  "%s"`, entry))
	}

	// Generate TOML config
	toml := fmt.Sprintf(`# drbd-reactor promoter configuration for HA resource: %s
# Generated by sds-controller

[[promoter]]
[promoter.resources.%s]
runner = "systemd"
start = [
%s
]
on-drbd-demote-failure = "reboot"

`, resource, resource, strings.Join(startActions, ",\n"))

	return toml
}

// CreateFilesystemOnly creates a filesystem on a DRBD device
// primaryAddress returns the address of the node currently Primary for the
// resource, from live DRBD status.
func (rm *ResourceManager) primaryAddress(ctx context.Context, resource string) (string, error) {
	info, err := rm.GetResource(ctx, resource)
	if err != nil {
		return "", err
	}
	for addr, st := range info.NodeStates {
		if st.Role == "Primary" {
			return addr, nil
		}
	}
	return "", fmt.Errorf("no Primary node for resource %q; specify a node explicitly", resource)
}

// resolveNodeOrPrimary resolves a node selection to a host address. An empty
// selection ("Auto (Primary)") resolves to the current Primary; if there is no
// Primary that is a hard error rather than a silent no-op on an empty host.
func (rm *ResourceManager) resolveNodeOrPrimary(ctx context.Context, resource, node string) (string, error) {
	if strings.TrimSpace(node) == "" {
		return rm.primaryAddress(ctx, resource)
	}
	addr := rm.controller.ResolveHost(node)
	if strings.TrimSpace(addr) == "" {
		return "", fmt.Errorf("unknown node %q", node)
	}
	return addr, nil
}

func (rm *ResourceManager) CreateFilesystemOnly(ctx context.Context, resource string, volumeID uint32, fsType string, node string) error {
	// Resolve node to address; empty means the current Primary.
	address, err := rm.resolveNodeOrPrimary(ctx, resource, node)
	if err != nil {
		return fmt.Errorf("resolve target node: %w", err)
	}

	rm.controller.logger.Info("Creating filesystem",
		zap.String("resource", resource),
		zap.Uint32("volume_id", volumeID),
		zap.String("fstype", fsType),
		zap.String("node", node),
		zap.String("address", address))

	if rm.deployment == nil {
		return fmt.Errorf("deployment client not set")
	}

	// Determine DRBD device path
	drbdDevice := fmt.Sprintf("/dev/drbd/by-res/%s/%d", resource, volumeID)

	// Create filesystem on the specified node (should be Primary)
	// Note: xfs/btrfs use -f (lowercase), ext4 uses -F (uppercase)
	fsType = strings.ToLower(strings.TrimSpace(fsType))
	forceFlag := "-F"
	if fsType == "xfs" || fsType == "btrfs" {
		forceFlag = "-f"
	}
	mkfsCmd := fmt.Sprintf("sudo mkfs.%s %s %s", fsType, forceFlag, drbdDevice)
	result, err := rm.deployment.Exec(ctx, []string{address}, mkfsCmd)
	if err != nil {
		return fmt.Errorf("failed to create filesystem: %w", err)
	}

	if !result.AllSuccess() {
		var errMsg string
		for host, h := range result.Hosts {
			if !h.Success {
				errMsg = fmt.Sprintf("%s: %s", host, h.Output)
				break
			}
		}
		return fmt.Errorf("filesystem creation failed: %s", errMsg)
	}

	return nil
}

// Helper functions for parsing DRBD status output

type volumeInfo struct {
	id     int
	device string
	sizeGB uint64
}

// isIndentedStatusLine reports whether a raw drbdadm/drbdsetup status line is
// indented. Peer and per-volume detail lines are indented; the local resource
// line is not.
func isIndentedStatusLine(line string) bool {
	return strings.HasPrefix(line, " ") || strings.HasPrefix(line, "\t")
}

// fieldValue returns the value of the first "key:value" field in fields, or ""
// when none carries that key. drbdadm separates fields by spaces and sometimes
// trails them with a comma.
func fieldValue(fields []string, key string) string {
	for _, f := range fields {
		if strings.HasPrefix(f, key) {
			return strings.TrimSuffix(strings.TrimPrefix(f, key), ",")
		}
	}
	return ""
}

// isPeerDiskLine reports whether a status line describes a peer's disk rather
// than the peer itself. "peer-disk:" contains "disk:", so anything matching on
// the latter has to exclude the former first.
func isPeerDiskLine(trimmed string) bool {
	return strings.Contains(trimmed, "peer-disk:")
}

// localStatusLine returns the trimmed local resource line from drbdadm or
// drbdsetup status output. The local line is the first unindented line that
// carries a "role:" field; unindented lines without one (such as the
// "drbdsetup status <res> --verbose" command echo printed by
// "drbdadm status --verbose") are skipped.
func localStatusLine(output string) string {
	for _, line := range strings.Split(output, "\n") {
		if line == "" || isIndentedStatusLine(line) {
			continue
		}
		trimmed := strings.TrimSpace(line)
		if strings.Contains(trimmed, "role:") {
			return trimmed
		}
	}
	return ""
}

func parseRoleFromStatus(output string) string {
	for _, field := range strings.Fields(localStatusLine(output)) {
		if strings.HasPrefix(field, "role:") {
			role := strings.TrimSuffix(strings.TrimPrefix(field, "role:"), ",")
			switch role {
			case "Primary", "Secondary":
				return role
			}
		}
	}
	return "Unknown"
}

// parseNodeStatesFromStatus parses each node's role and disk state from DRBD status output
// Format:
//
//	ha_res role:Primary
//	  disk:UpToDate open:no
//	orange2 role:Secondary
//	  peer-disk:UpToDate
func parseNodeStatesFromStatus(output string, nodeAddresses []string) map[string]*ResourceNodeState {
	nodeStates := make(map[string]*ResourceNodeState)
	lines := strings.Split(output, "\n")

	// Get local node's role (first line with role:)
	localRole := "Unknown"
	localDiskState := "Unknown"

	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		// Local resource line: the first unindented line carrying "role:".
		// Skips the "drbdsetup status <res> --verbose" command echo emitted
		// by "drbdadm status --verbose"; peer role lines are indented.
		if localRole == "Unknown" && !isIndentedStatusLine(line) && strings.Contains(trimmed, "role:") {
			parts := strings.Fields(trimmed)
			for _, p := range parts {
				if strings.HasPrefix(p, "role:") {
					localRole = strings.TrimPrefix(p, "role:")
					localRole = strings.TrimSuffix(localRole, ",")
					break
				}
			}
		}
		// Local disk can be a standalone "disk:UpToDate" line or a verbose
		// volume line such as "volume:0 minor:0 disk:UpToDate ..."
		if !strings.Contains(trimmed, "peer-disk:") && (strings.HasPrefix(trimmed, "disk:") || (strings.Contains(trimmed, "volume:") && strings.Contains(trimmed, "disk:"))) {
			parts := strings.Fields(trimmed)
			for _, p := range parts {
				if strings.HasPrefix(p, "disk:") {
					localDiskState = strings.TrimPrefix(p, "disk:")
					localDiskState = strings.TrimSuffix(localDiskState, ",")
					break
				}
			}
		}
	}

	// Set local node state (first node in list)
	if len(nodeAddresses) > 0 {
		nodeStates[nodeAddresses[0]] = &ResourceNodeState{
			Role:      localRole,
			DiskState: localDiskState,
		}
	}

	// Parse peer nodeAddresses: "  orange2 role:Secondary"
	currentNode := ""
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		parts := strings.Fields(trimmed)

		// Check if this line starts with a node name followed by "role:"
		// This matches "orange2 role:Secondary" pattern
		if len(parts) >= 2 {
			role := ""
			for _, part := range parts[1:] {
				if strings.HasPrefix(part, "role:") {
					role = strings.TrimSuffix(strings.TrimPrefix(part, "role:"), ",")
					break
				}
			}
			if role != "" {
				// Find which node this is
				matched := false
				for _, node := range nodeAddresses {
					if node == nodeAddresses[0] {
						continue // Skip local node
					}
					if parts[0] == node {
						currentNode = node
						matched = true
						if _, exists := nodeStates[currentNode]; !exists {
							nodeStates[currentNode] = &ResourceNodeState{Role: role}
						} else {
							nodeStates[currentNode].Role = role
						}
						break
					}
				}
				// A role-carrying line whose name is not a tracked node is
				// either the local resource line or a peer absent from
				// nodeAddresses (e.g. a diskless quorum tiebreaker). Reset
				// currentNode so its following peer-disk line is not
				// misattributed to the previously matched node.
				if !matched {
					currentNode = ""
				}
			}
		}

		// A peer that is not Connected prints exactly one line and no role:
		//   "  sds-e connection:Connecting"
		// Recording it is the whole point — see ResourceNodeState.Connection.
		// It also resets currentNode, because the line carries no role and the
		// next peer-disk line (if any) is not this peer's.
		if len(parts) >= 2 && !isPeerDiskLine(trimmed) {
			if conn := fieldValue(parts[1:], "connection:"); conn != "" {
				for _, node := range nodeAddresses {
					if node == nodeAddresses[0] || parts[0] != node {
						continue
					}
					if st, exists := nodeStates[node]; exists {
						st.Connection = conn
					} else {
						nodeStates[node] = &ResourceNodeState{Connection: conn}
					}
					break
				}
				if fieldValue(parts[1:], "role:") == "" {
					currentNode = ""
				}
			}
		}

		// Check for peer-disk state (belongs to currentNode)
		if strings.Contains(trimmed, "peer-disk:") && currentNode != "" {
			parts := strings.Fields(trimmed)
			for _, p := range parts {
				if strings.HasPrefix(p, "peer-disk:") {
					diskState := strings.TrimSuffix(strings.TrimPrefix(p, "peer-disk:"), ",")
					if _, exists := nodeStates[currentNode]; !exists {
						nodeStates[currentNode] = &ResourceNodeState{DiskState: diskState}
					} else {
						nodeStates[currentNode].DiskState = diskState
					}
					break
				}
			}
		}
	}

	return nodeStates
}

func parseVolumesFromStatus(output string) []volumeInfo {
	var volumes []volumeInfo
	seen := make(map[int]bool)

	for _, line := range strings.Split(output, "\n") {
		trimmed := strings.TrimSpace(line)
		if !strings.Contains(trimmed, "volume:") || strings.Contains(trimmed, "peer-disk:") {
			continue
		}

		fields := strings.Fields(trimmed)
		volumeID := -1
		minor := -1
		var sizeGB uint64
		for i, field := range fields {
			switch {
			case strings.HasPrefix(field, "volume:"):
				value := strings.TrimPrefix(field, "volume:")
				if value == "" && i+1 < len(fields) {
					value = fields[i+1]
				}
				if parsed, err := strconv.Atoi(strings.TrimSuffix(value, ",")); err == nil {
					volumeID = parsed
				}
			case strings.HasPrefix(field, "minor:"):
				if parsed, err := strconv.Atoi(strings.TrimSuffix(strings.TrimPrefix(field, "minor:"), ",")); err == nil {
					minor = parsed
				}
			case strings.HasPrefix(field, "size:"):
				if parsed, err := strconv.ParseUint(strings.TrimSuffix(strings.TrimPrefix(field, "size:"), ","), 10, 64); err == nil {
					sizeGB = parsed / 1024 / 1024
				}
			}
		}

		if volumeID < 0 || seen[volumeID] {
			continue
		}

		device := ""
		if minor >= 0 {
			device = fmt.Sprintf("/dev/drbd%d", minor)
		}
		volumes = append(volumes, volumeInfo{
			id:     volumeID,
			device: device,
			sizeGB: sizeGB,
		})
		seen[volumeID] = true
	}

	return volumes
}

// drbdsetupStatus mirrors the subset of `drbdsetup status <res> --json` output
// that carries live role, disk and replication/resync state. Fields absent in
// steady state (notably "done") are treated as fully in sync.
type drbdsetupStatus struct {
	Name    string `json:"name"`
	Role    string `json:"role"`
	Devices []struct {
		Volume    int    `json:"volume"`
		DiskState string `json:"disk-state"`
		// Quorum reports whether the local node currently holds DRBD quorum for
		// this device. It is a pointer so a missing field (older drbd, or a
		// diskless view) is distinguishable from an explicit false. A resource
		// configured with `quorum majority` + `on-no-quorum io-error` blocks I/O
		// on any node that has lost quorum, which is what makes a guarded
		// force-promote of a quorate survivor safe.
		Quorum *bool `json:"quorum"`
	} `json:"devices"`
	Connections []struct {
		Name string `json:"name"`
		// ConnectionState is "Connected", "Connecting", "StandAlone", ... It is
		// the only field a peer whose link is down carries any truth in: DRBD
		// leaves peer-role and every peer_device empty for such a peer, and
		// reading those empties as facts is the whole reason this is parsed.
		ConnectionState string `json:"connection-state"`
		PeerRole        string `json:"peer-role"`
		PeerDevices     []struct {
			Volume           int    `json:"volume"`
			ReplicationState string `json:"replication-state"`
			PeerDiskState    string `json:"peer-disk-state"`
			// Done is the resync completion percentage (0..100) drbdsetup emits
			// on a peer_device while resyncing. PercentInSync is accepted as an
			// alias for robustness across drbd versions. Both are absent in
			// steady state, so a nil value means "fully in sync".
			Done          *float64 `json:"done"`
			PercentInSync *float64 `json:"percent-in-sync"`
		} `json:"peer_devices"`
	} `json:"connections"`
}

// parseNodeStatesFromJSON parses `drbdsetup status <res> --json` into per-node
// states keyed by node name. localNode is the name of the queried node (the
// JSON top-level resource); its peers — including any diskless quorum
// tiebreaker — come from connections[]. It returns an error when the JSON is
// empty or cannot be decoded so the caller can fall back to the text parser.
func parseNodeStatesFromJSON(output, localNode string) (map[string]*ResourceNodeState, error) {
	trimmed := strings.TrimSpace(output)
	if trimmed == "" {
		return nil, fmt.Errorf("empty drbdsetup status json")
	}
	var resources []drbdsetupStatus
	if err := json.Unmarshal([]byte(trimmed), &resources); err != nil {
		return nil, fmt.Errorf("decode drbdsetup status json: %w", err)
	}
	if len(resources) == 0 {
		return nil, fmt.Errorf("drbdsetup status json contained no resources")
	}
	res := resources[0]

	states := make(map[string]*ResourceNodeState)

	// Local node: role + disk from the top-level resource. A node has no
	// replication relationship to itself, so it is fully in sync (100).
	local := &ResourceNodeState{Role: res.Role, SyncPercent: 100, SyncPercentKnown: true}
	if len(res.Devices) > 0 {
		local.DiskState = res.Devices[0].DiskState
		// Quorum is only ever the queried node's own verdict; the peers below
		// deliberately leave it nil rather than assume they agree.
		local.Quorum = res.Devices[0].Quorum
	}
	states[localNode] = local

	// Each peer (including a diskless quorum tiebreaker) is one connection.
	for _, conn := range res.Connections {
		if conn.Name == "" {
			continue
		}
		peer := &ResourceNodeState{
			Role:             conn.PeerRole,
			Connection:       conn.ConnectionState,
			SyncPercent:      100,
			SyncPercentKnown: true,
		}
		if len(conn.PeerDevices) > 0 {
			pd := conn.PeerDevices[0]
			peer.DiskState = pd.PeerDiskState
			peer.Replication = pd.ReplicationState
			switch {
			case pd.Done != nil:
				peer.SyncPercent = *pd.Done
			case pd.PercentInSync != nil:
				peer.SyncPercent = *pd.PercentInSync
			}
		}
		states[conn.Name] = peer
	}

	return states, nil
}

// HaPromoterStatus is the controller-side mirror of deployment.ReactorPromoterStatus,
// used to pass structured reactor status over gRPC without importing deployment into proto.
type HaPromoterStatus struct {
	DRBDResource string
	PrimaryOn    string
	Status       string
	Target       HaServiceStatus
	Deps         []HaServiceStatus
}

// HaServiceStatus holds the name and systemd state of one HA dependency.
type HaServiceStatus struct {
	Name   string
	Status string
}

// GetHaStatus queries the reactor promoter status for one or all HA resources.
// It resolves the primary node for each resource from the DRBD layer, then
// SSH-dispatches `drbd-reactorctl status --json` to that node so the status
// reflects the node that is actually running the services.
func (rm *ResourceManager) GetHaStatus(ctx context.Context, resource string) ([]*HaPromoterStatus, error) {
	if rm.controller.db == nil {
		return nil, fmt.Errorf("database not available")
	}

	var configs []*database.HaConfig
	if resource == "" {
		var err error
		configs, err = rm.controller.db.ListHaConfigs(ctx)
		if err != nil {
			return nil, fmt.Errorf("failed to list HA configs: %w", err)
		}
	} else {
		cfg, err := rm.controller.db.GetHaConfig(ctx, resource)
		if err != nil {
			return nil, fmt.Errorf("failed to get HA config: %w", err)
		}
		if cfg != nil {
			configs = append(configs, cfg)
		}
	}

	// Build a map from resource name → host list (resolved addresses).
	type resourceHosts struct {
		name  string
		hosts []string
	}
	var targets []resourceHosts
	for _, cfg := range configs {
		dbRes, err := rm.controller.db.GetResource(ctx, cfg.Resource)
		if err != nil || dbRes == nil {
			continue
		}
		nodes := strings.Split(dbRes.Nodes, ",")
		hosts := make([]string, 0, len(nodes))
		for _, n := range nodes {
			if h := rm.controller.ResolveHost(strings.TrimSpace(n)); h != "" {
				hosts = append(hosts, h)
			}
		}
		if len(hosts) > 0 {
			targets = append(targets, resourceHosts{name: cfg.Resource, hosts: hosts})
		}
	}

	// For each resource, find its primary node and fetch reactor status from there.
	// We cache reactor status per host to avoid redundant SSH calls.
	type reactorKey = string
	reactorCache := make(map[reactorKey]*deployment.ReactorStatus)

	getReactorStatus := func(host string) (*deployment.ReactorStatus, error) {
		if s, ok := reactorCache[host]; ok {
			return s, nil
		}
		s, err := rm.deployment.ReactorStatusJSON(ctx, host)
		if err != nil {
			return nil, err
		}
		reactorCache[host] = s
		return s, nil
	}

	var result []*HaPromoterStatus
	for _, t := range targets {
		// Try each host until we find a reactor that knows the primary.
		// On the primary host, target.status == "active"; on standbys it's "inactive".
		// We want the primary host's perspective.
		var best *deployment.ReactorPromoterStatus
		for _, host := range t.hosts {
			rs, err := getReactorStatus(host)
			if err != nil {
				continue
			}
			for i := range rs.Promoter {
				p := &rs.Promoter[i]
				if p.DRBDResource != t.name {
					continue
				}
				// Prefer the promoter entry that is itself active (i.e. we are on the primary).
				if best == nil || p.Status == "active" {
					best = p
				}
				if p.Status == "active" {
					break
				}
			}
			if best != nil && best.Status == "active" {
				break
			}
		}
		if best == nil {
			continue
		}

		ps := &HaPromoterStatus{
			DRBDResource: best.DRBDResource,
			PrimaryOn:    best.PrimaryOn,
			Status:       best.Status,
			Target: HaServiceStatus{
				Name:   best.Target.Name,
				Status: best.Target.Status,
			},
		}
		for _, d := range best.Dependencies {
			ps.Deps = append(ps.Deps, HaServiceStatus{Name: d.Name, Status: d.Status})
		}
		result = append(result, ps)
	}

	return result, nil
}

// DrainNode moves all Primary DRBD resources off the named node to one of
// their other replica nodes, then marks the node as maintenance in the DB.
// It returns the list of resources that were moved.
func (rm *ResourceManager) DrainNode(ctx context.Context, nodeName string) ([]string, error) {
	if rm.controller.db == nil {
		return nil, fmt.Errorf("database not available")
	}

	// Resolve the node to make sure it exists.
	addr := rm.controller.nodes.GetNodeAddressByName(nodeName)
	if addr == "" {
		return nil, fmt.Errorf("node %q is not registered", nodeName)
	}

	allResources, err := rm.controller.db.ListResources(ctx)
	if err != nil {
		return nil, fmt.Errorf("list resources: %w", err)
	}

	var moved []string
	for _, dbRes := range allResources {
		nodes := strings.Split(dbRes.Nodes, ",")
		isReplica := false
		for _, n := range nodes {
			if strings.TrimSpace(n) == nodeName {
				isReplica = true
				break
			}
		}
		if !isReplica {
			continue
		}

		// Check whether this node is currently Primary via live DRBD status.
		// NodeStates is keyed by address, so compare addr.
		info, err := rm.GetResource(ctx, dbRes.Name)
		if err != nil {
			rm.controller.logger.Warn("drain: failed to get resource info, skipping",
				zap.String("resource", dbRes.Name), zap.Error(err))
			continue
		}
		ns, ok := info.NodeStates[addr]
		if !ok {
			ns, ok = info.NodeStates[nodeName]
		}
		if !ok || ns.Role != "Primary" {
			continue // already Secondary or not connected
		}

		// Find a different replica node to take over as Primary.
		var target string
		for _, n := range nodes {
			n = strings.TrimSpace(n)
			if n != nodeName {
				target = n
				break
			}
		}
		if target == "" {
			return moved, fmt.Errorf("resource %q has no other replica to take over", dbRes.Name)
		}

		rm.controller.logger.Info("drain: moving primary",
			zap.String("resource", dbRes.Name),
			zap.String("from", nodeName), zap.String("to", target))

		if err := rm.SetSecondary(ctx, dbRes.Name, nodeName); err != nil {
			return moved, fmt.Errorf("set secondary for %q on %q: %w", dbRes.Name, nodeName, err)
		}
		if err := rm.SetPrimary(ctx, dbRes.Name, target, false); err != nil {
			return moved, fmt.Errorf("set primary for %q on %q: %w", dbRes.Name, target, err)
		}
		moved = append(moved, dbRes.Name)
	}

	// Mark the node as maintenance in the DB.
	dbNode, err := rm.controller.db.GetNode(ctx, addr)
	if err != nil || dbNode == nil {
		return moved, fmt.Errorf("node %q not found in database", nodeName)
	}
	dbNode.State = string(NodeStateMaintenance)
	if err := rm.controller.db.SaveNode(ctx, dbNode); err != nil {
		return moved, fmt.Errorf("save node state: %w", err)
	}

	return moved, nil
}

// UndrainNode clears the maintenance state on a node, returning it to service.
func (rm *ResourceManager) UndrainNode(ctx context.Context, nodeName string) error {
	if rm.controller.db == nil {
		return fmt.Errorf("database not available")
	}
	addr := rm.controller.nodes.GetNodeAddressByName(nodeName)
	if addr == "" {
		return fmt.Errorf("node %q is not registered", nodeName)
	}
	dbNode, err := rm.controller.db.GetNode(ctx, addr)
	if err != nil || dbNode == nil {
		return fmt.Errorf("node %q not found in database", nodeName)
	}
	dbNode.State = string(NodeStateOnline)
	return rm.controller.db.SaveNode(ctx, dbNode)
}

// GetResourceStatusList adapts ResourceManager to alert.ResourceLister by returning
// status info for all resources managed by the controller.
func (rm *ResourceManager) GetResourceStatusList(ctx context.Context) ([]alert.ResourceStatusInfo, error) {
	if rm.controller.db == nil {
		return nil, fmt.Errorf("database not available")
	}
	dbResources, err := rm.controller.db.ListResources(ctx)
	if err != nil {
		return nil, err
	}

	var result []alert.ResourceStatusInfo
	for _, dbRes := range dbResources {
		info, err := rm.GetResource(ctx, dbRes.Name)
		if err != nil || info == nil {
			continue
		}
		// Nodes that hold no local copy on purpose: quorum tiebreakers and
		// diskless clients. They report Diskless as their normal state, so the
		// monitor has to be told, or it alerts on every one of them forever.
		diskless := disklessByDesign(rm.controller, dbRes)

		item := alert.ResourceStatusInfo{
			Name:       dbRes.Name,
			NodeStates: make(map[string]alert.NodeStateInfo, len(info.NodeStates)),
		}
		for node, st := range info.NodeStates {
			state := alert.NodeStateInfo{
				DiskState:        st.DiskState,
				ReplicationState: st.Replication,
				Role:             st.Role,
				ExpectedDiskless: diskless[node] || diskless[rm.controller.ResolveHost(node)],
				Quorum:           st.Quorum,
				Connection:       st.Connection,
			}
			// Only forward completion the status source actually reported. A
			// text-parsed state has none, and passing its zero on would export
			// "0% synced" for every replica of a cluster that is fully in sync.
			if st.SyncPercentKnown {
				percent := st.SyncPercent
				state.SyncPercent = &percent
			}
			item.NodeStates[node] = state
		}

		// For WAN resources, fold the sds-proxy pair's health into the status so
		// the alert monitor can surface a broken cross-site link.
		if dbRes.WANMode {
			item.WANEnabled = true
			st, err := wanproxy.StatusMulti(ctx, rm.wanproxyDeployClient(), rm.wanMultiSpecFor(dbRes))
			switch {
			case err != nil:
				item.WANHealthy = false
				item.WANMessage = err.Error()
			default:
				item.WANHealthy = st.Healthy()
				item.WANMessage = wanStatusMessage(st)
			}
		}

		result = append(result, item)
	}
	return result, nil
}

// disklessByDesign returns the set of nodes that are meant to carry no local
// replica of the resource — quorum tiebreakers (DisklessNodes) and diskless
// data clients (DisklessClients).
//
// Entries are recorded by node name, while live DRBD status can key a node by
// either name or address depending on how it was reported, so each name is
// indexed under both.
func disklessByDesign(c *Controller, dbRes *database.Resource) map[string]bool {
	out := map[string]bool{}
	for _, list := range []string{dbRes.DisklessNodes, dbRes.DisklessClients} {
		for _, n := range splitCSV(list) {
			if n == "" {
				continue
			}
			out[n] = true
			if addr := c.ResolveHost(n); addr != "" {
				out[addr] = true
			}
		}
	}
	return out
}

// wanProxySpecFor rebuilds the sds-proxy spec for a stored WAN resource so its
// live status can be queried. The primary is the resource node that is not the
// DR node.
// wanMultiSpecFor rebuilds the full multi-leg sds-proxy spec for a stored WAN
// resource so its live status can be queried.
//
// It must mirror what provisioning built, because leg names and ports are
// derived from it: one leg per primary-site node, in the stored node order
// (Legs assigns WAN and DRBD ports by index), with the DR node excluded.
//
// This replaced a version that collapsed the resource to a single ProxySpec
// named after the resource with an arbitrary node as "the primary". Once a WAN
// resource has more than one primary-site replica, the real units are
// sds-proxy@<resource>_<node>, so that spec named a unit present on no node and
// reported a perfectly healthy WAN as entirely down.
func (rm *ResourceManager) wanMultiSpecFor(dbRes *database.Resource) wanproxy.MultiSpec {
	var primaries, primaryNames []string
	for _, n := range splitCSV(dbRes.Nodes) {
		if n == "" || n == dbRes.DRNode {
			continue
		}
		primaries = append(primaries, rm.controller.ResolveHost(n))
		primaryNames = append(primaryNames, n)
	}
	return wanproxy.MultiSpec{
		Resource:          dbRes.Name,
		PrimaryNodeAddrs:  primaries,
		PrimaryNodeKeys:   primaryNames,
		DRNodeAddr:        rm.controller.ResolveHost(dbRes.DRNode),
		DRPublicEndpoint:  dbRes.DREndpoint,
		PrimaryEgressAddr: dbRes.WANEgressAddress,
		BaseWANPort:       int(dbRes.WANPort),
		BaseDRBDPort:      int(dbRes.Port),
	}
}

// wanStatusMessage renders a short human description of an unhealthy WAN proxy
// pair; it returns "" when the pair is healthy.
func wanStatusMessage(st *wanproxy.MultiStatus) string {
	if st == nil {
		return "WAN status unavailable"
	}
	if len(st.Legs) == 0 {
		return "no WAN legs configured"
	}
	var problems []string
	// Name the node on each broken leg. "primary proxy inactive" on a
	// multi-replica resource does not say which replica lost its tunnel, which
	// is the only thing the operator needs in order to act.
	for _, l := range st.Unhealthy() {
		switch {
		case !l.PrimaryActive && !l.DRActive:
			problems = append(problems, fmt.Sprintf("leg %s down on both ends", l.PrimaryHost))
		case !l.PrimaryActive:
			problems = append(problems, fmt.Sprintf("proxy inactive on %s", l.PrimaryHost))
		default:
			problems = append(problems, fmt.Sprintf("DR proxy inactive for leg %s", l.PrimaryHost))
		}
	}
	if !st.WANReachable {
		problems = append(problems, "DR WAN endpoint unreachable")
	}
	return strings.Join(problems, "; ")
}

// drbdStatusFromAnyHost returns the first usable `drbdadm status` answer.
//
// A host is skipped both when it cannot be reached and when the command it ran
// failed: a node that answers "no resources defined" is no more informative
// than one that answers nothing. The last error is returned when none work, so
// the caller can still render the resource with unknown state rather than
// failing outright — the UI has to draw something either way.
func (rm *ResourceManager) drbdStatusFromAnyHost(ctx context.Context, hosts []string, resource string) (*deployment.ExecResult, int, error) {
	var lastErr error
	for i, host := range hosts {
		result, err := rm.deployment.DRBDStatus(ctx, []string{host}, resource)
		if err != nil {
			lastErr = err
			continue
		}
		if result == nil || !result.AllSuccess() {
			lastErr = fmt.Errorf("drbdadm status for %s on %s did not succeed", resource, host)
			continue
		}
		return result, i, nil
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("no hosts to ask for %s", resource)
	}
	return nil, -1, lastErr
}

// nodesLocalFirst returns nodes reordered so the one at idx comes first.
//
// parseNodeStatesFromStatus treats the head of the list as the node the status
// was read from, which held while only hosts[0] was ever asked. Once any
// reachable host can answer, the caller has to say which one did.
func nodesLocalFirst(nodes []string, idx int) []string {
	if idx <= 0 || idx >= len(nodes) {
		return nodes
	}
	out := make([]string, 0, len(nodes))
	out = append(out, nodes[idx])
	for i, n := range nodes {
		if i != idx {
			out = append(out, n)
		}
	}
	return out
}
