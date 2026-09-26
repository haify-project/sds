// Package wanproxy orchestrates the per-resource sds-proxy pair that carries a
// DRBD resource's replication across the internet (primary site <-> DR site).
//
// It is the controller-side implementation of the opt-in WAN replication
// feature described in docs/2026-07-05-wan-replication-design.md. It is only
// invoked when a resource is created with WAN mode; the default LAN path never
// touches this package.
//
// # What it does
//
// For one WAN resource it renders a dialer config (primary site) and an acceptor
// config (DR site), then pushes the shared PKI, the sds-proxy binary and those
// configs to the two nodes and installs+enables a per-resource systemd unit
// (sds-proxy@<resource>). Deprovision reverses the per-resource state.
//
// # On-node layout
//
//	/usr/local/bin/sds-proxy            the proxy binary (shared, pushed once per node)
//	/etc/sds-proxy/ca.pem               shared CA cert     (shared)
//	/etc/sds-proxy/cert.pem             shared leaf cert   (shared)
//	/etc/sds-proxy/key.pem              shared leaf key    (shared)
//	/etc/sds-proxy/<resource>.toml      per-resource proxy config
//	/etc/systemd/system/sds-proxy@.service   the unit template (shared)
//	sds-proxy@<resource>                the per-resource systemd instance
//
// # Ordering contract
//
// The proxy MUST be running before DRBD is brought up, because in WAN mode the
// primary's DRBD connects to 127.0.0.1:P (the local dialer) and the DR's local
// acceptor dials the DR's DRBD at 127.0.0.1:P. If DRBD comes up first it finds
// nothing listening on the loopback proxy port. The controller therefore calls
// Provision BEFORE `drbdadm up`, and Deprovision AFTER `drbdadm down`. This
// ordering is enforced by the controller's call sequence, not by this package.
//
// # Firewall / reachability (best effort)
//
// The DR endpoint's WAN port (spec.WANPort) must be reachable over TCP from the
// primary's egress; UDP is not used. This package cannot open cloud security
// groups, so reachability is the operator's responsibility. Provision only
// documents/validates this at the spec level (a valid host:port); it does not
// probe the remote port.
package wanproxy

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"
)

// Reachability-probe tuning. These are package vars (not consts) so tests can
// shrink the retry window; production keeps the defaults.
var (
	// reachAttempts is how many times VerifyReachability retries before failing,
	// absorbing the acceptor's bind race after enable --now.
	reachAttempts = 5
	// reachRetryDelay is the wait between reachability attempts.
	reachRetryDelay = 2 * time.Second
	// reachTimeoutSecs bounds each individual TCP connect attempt on the node.
	reachTimeoutSecs = 5
)

// On-node filesystem layout. These are the canonical paths the sds-proxy binary,
// its shared PKI and per-resource configs live at on every WAN node.
const (
	// NodeBinaryPath is where the sds-proxy binary is installed on each node.
	NodeBinaryPath = "/usr/local/bin/sds-proxy"

	// NodeConfigDir holds the shared certs and every per-resource config.
	NodeConfigDir = "/etc/sds-proxy"

	// NodeCAPath, NodeCertPath and NodeKeyPath are the shared mTLS material.
	NodeCAPath   = NodeConfigDir + "/ca.pem"
	NodeCertPath = NodeConfigDir + "/cert.pem"
	NodeKeyPath  = NodeConfigDir + "/key.pem"

	// UnitTemplateName is the templated systemd unit; the instance part (%i) is
	// the resource name.
	UnitTemplateName = "sds-proxy@.service"

	// UnitTemplatePath is where the unit template is installed on each node.
	UnitTemplatePath = "/etc/systemd/system/" + UnitTemplateName

	// NodeMetricsDir holds the per-resource JSON snapshots sds-proxy publishes.
	// It lives under /run because these are volatile runtime state: a reboot
	// should not leave a stale backlog figure behind for the controller to read
	// and report as current.
	NodeMetricsDir = "/run/sds-proxy"
)

// PKIDir is the controller-side cache for the shared CA + leaf. EnsurePKI
// generates the material once and reuses it thereafter. It is a package var so
// it can be overridden (e.g. in tests or by configuration).
var PKIDir = "/var/lib/sds/wanproxy-pki"

// NodeConfigPath returns the on-node path of a resource's proxy config.
func NodeConfigPath(resource string) string {
	return NodeConfigDir + "/" + resource + ".toml"
}

// UnitInstance returns the systemd instance name for a resource's proxy.
func UnitInstance(resource string) string {
	return "sds-proxy@" + resource
}

// LegID names one primary-site node's WAN leg to the DR site.
//
// A DRBD 9 resource is a full mesh: the DR peers with EVERY primary-site node,
// not just whichever one is currently Primary — otherwise a failover inside the
// primary site would move the workload to a node that has no path to the DR and
// replication would silently stop. Each of those legs needs its own tunnel, so
// the proxy is instanced per (resource, primary node) rather than per resource.
//
// A single-replica resource keeps the historic per-resource names, so existing
// deployments are untouched.
func LegID(resource, primaryNode string, single bool) string {
	if single {
		return resource
	}
	return resource + "_" + sanitizeLeg(primaryNode)
}

// sanitizeLeg makes a node reference safe for a systemd instance name and a
// filename: systemd treats "/" specially and "@" separates unit from instance.
func sanitizeLeg(node string) string {
	repl := strings.NewReplacer("/", "-", "@", "-", ":", "-", " ", "-", ".", "-")
	return repl.Replace(strings.TrimSpace(node))
}

// NodeMetricsPath returns the on-node path of a resource's metrics snapshot.
func NodeMetricsPath(resource string) string {
	return NodeMetricsDir + "/" + resource + ".json"
}

// unitTemplate is the per-resource systemd unit. It mirrors the templated-unit
// shape the project already uses elsewhere: Type=simple, an instance-specific
// config path (%i.toml) and Restart so a transient crash self-heals. It is
// ordered Before=drbd.service so, on a reboot, the loopback proxy is listening
// before DRBD tries to connect to it (the same ordering Provision enforces at
// create time).
const unitTemplate = `[Unit]
Description=SDS WAN replication proxy for %i
Documentation=https://github.com/liliang-cn/sds
After=network-online.target
Wants=network-online.target
Before=drbd.service

[Service]
Type=simple
ExecStart=` + NodeBinaryPath + ` ` + NodeConfigDir + `/%i.toml
Restart=always
RestartSec=5

[Install]
WantedBy=multi-user.target
`

// UnitTemplate returns the content of the sds-proxy@.service systemd unit
// template installed on each node.
func UnitTemplate() string { return unitTemplate }

// HostResult is the per-host outcome of a deploy/exec operation.
type HostResult struct {
	Host    string
	Output  string
	Success bool
	Err     error
}

// Result aggregates per-host outcomes. It mirrors the AllSuccess/FailedHosts
// contract of pkg/deployment so orchestration here checks multi-host results the
// same way pkg/controller does, without importing the deployment package's
// concrete types (keeping the interface trivially mockable).
type Result struct {
	Hosts map[string]*HostResult
}

// AllSuccess reports whether every host succeeded.
func (r *Result) AllSuccess() bool {
	for _, h := range r.Hosts {
		if h == nil || !h.Success {
			return false
		}
	}
	return true
}

// FailedHosts lists the hosts that did not succeed.
func (r *Result) FailedHosts() []string {
	var failed []string
	for host, h := range r.Hosts {
		if h == nil || !h.Success {
			failed = append(failed, host)
		}
	}
	return failed
}

// FailureDetails renders the failed hosts with the reason each one gave, so a
// WAN provisioning error names the problem instead of only the addresses. See
// deployment.ExecResult.FailureDetails.
func (r *Result) FailureDetails() string {
	failed := r.FailedHosts()
	if len(failed) == 0 {
		return ""
	}
	sort.Strings(failed)

	parts := make([]string, 0, len(failed))
	for _, host := range failed {
		reason := ""
		if h := r.Hosts[host]; h != nil {
			reason = strings.TrimSpace(h.Output)
			if reason == "" && h.Err != nil {
				reason = strings.TrimSpace(h.Err.Error())
			}
		}
		if reason == "" {
			reason = "no output"
		}
		parts = append(parts, fmt.Sprintf("%s: %s", host, strings.Join(strings.Fields(reason), " ")))
	}
	return strings.Join(parts, "; ")
}

// DeploymentClient is the minimal SSH/file-distribution surface wanproxy needs.
// It intentionally mirrors the isolation pattern pkg/gateway uses: the package
// depends on a small interface, not on the concrete *deployment.Client, so it is
// decoupled from the transport and unit-testable with a fake. The controller
// supplies an adapter over *deployment.Client in the wiring phase.
//
// Both methods distribute to / execute on privileged paths, so implementations
// are expected to run with the necessary privilege (the real adapter uses sudo,
// as the rest of the deployment layer does).
type DeploymentClient interface {
	// DistributeConfig writes content to remotePath on every host.
	DistributeConfig(ctx context.Context, hosts []string, content, remotePath string) (*Result, error)
	// Exec runs cmd on every host.
	Exec(ctx context.Context, hosts []string, cmd string) (*Result, error)
}

// ProxySpec fully describes the sds-proxy pair for one WAN resource. It is the
// public input to Provision; the controller builds it from the resource's WAN
// fields (see database.Resource / the CreateResource WAN branch).
type ProxySpec struct {
	// Resource is the DRBD resource name; it is also the systemd instance name
	// and the per-resource config filename stem.
	Resource string

	// PrimaryNodeAddr is the reachable address of the primary-site node (runs
	// the dialer). DRNodeAddr is the DR-site node (runs the acceptor).
	PrimaryNodeAddr string
	DRNodeAddr      string

	// DRPublicEndpoint is the DR site's public WAN address that the primary
	// dials (host or IP, no port). The primary may be behind NAT and dials out.
	DRPublicEndpoint string

	// WANPort is the mTLS WAN port the acceptor binds and the dialer connects
	// to (one TCP port; must be reachable from the primary's egress).
	WANPort int

	// PrimaryEgressAddr optionally pins the source address the dialer binds
	// before connecting out, so WAN replication leaves the primary over a chosen
	// interface (e.g. a leased line rather than the management link) and arrives
	// at the DR firewall from a predictable source IP. Empty lets the primary's
	// routing table decide.
	PrimaryEgressAddr string

	// DRBDPort is the resource's DRBD port P. The dialer binds 127.0.0.1:P and
	// accepts the primary's DRBD; the acceptor dials 127.0.0.1:P to reach the
	// DR's DRBD. (The nodes' own DRBD binds are P and P+9 respectively; see
	// generateDrbdConfig's WAN branch.)
	DRBDPort int

	// BinaryPath is the controller-local path to the sds-proxy binary to push to
	// both nodes (installed at NodeBinaryPath). When empty the binary push is
	// skipped, assuming it was pre-staged on the nodes.
	//
	// It is only consulted when BinaryFor is nil. A fleet whose nodes do not all
	// share the controller's architecture must use BinaryFor instead: one file
	// pushed everywhere is, for some of those nodes, a binary that cannot run.
	BinaryPath string

	// BinaryFor resolves the controller-local binary to push to one node, by
	// address. Returning "" skips the push for that node alone, so a fleet can
	// be part pre-staged and part pushed. Takes precedence over BinaryPath.
	BinaryFor func(nodeAddr string) string

	// SkipReachabilityCheck disables the post-start preflight that confirms the
	// DR WAN port is reachable from the primary. Default false: the check runs,
	// so a blocked firewall/security group fails Provision fast instead of
	// surfacing later as a DRBD resource that silently never syncs. Set true only
	// when the DR endpoint is legitimately not reachable at provision time.
	SkipReachabilityCheck bool
}

// Validate checks the spec is internally consistent before any node is touched.
func (s ProxySpec) Validate() error {
	if strings.TrimSpace(s.Resource) == "" {
		return fmt.Errorf("wanproxy: resource name is required")
	}
	if strings.TrimSpace(s.PrimaryNodeAddr) == "" {
		return fmt.Errorf("wanproxy: primary node address is required")
	}
	if strings.TrimSpace(s.DRNodeAddr) == "" {
		return fmt.Errorf("wanproxy: DR node address is required")
	}
	if strings.TrimSpace(s.DRPublicEndpoint) == "" {
		return fmt.Errorf("wanproxy: DR public endpoint is required")
	}
	if s.WANPort <= 0 || s.WANPort > 65535 {
		return fmt.Errorf("wanproxy: WAN port %d out of range", s.WANPort)
	}
	if s.DRBDPort <= 0 || s.DRBDPort > 65535 {
		return fmt.Errorf("wanproxy: DRBD port %d out of range", s.DRBDPort)
	}
	return nil
}

// Provision brings up the sds-proxy pair for one WAN resource. It is idempotent
// for the shared artifacts (PKI, unit template, certs, binary) and (re)writes
// the per-resource config, then enables+starts sds-proxy@<resource> on both
// nodes.
//
// IMPORTANT ordering: the controller MUST call Provision before `drbdadm up`
// for the resource, so the loopback proxy is listening before DRBD connects.
//
// Steps (all privileged, run on both nodes unless noted):
//  1. ensure the shared CA+leaf PKI on the controller (generate once, cached)
//  2. install the sds-proxy@.service unit template
//  3. distribute the shared ca/cert/key
//  4. push the sds-proxy binary (only when spec.BinaryPath is set)
//  5. write the dialer config to the primary and the acceptor config to the DR
//  6. systemctl daemon-reload
//  7. systemctl enable + restart sds-proxy@<resource> (restart, so a leg
//     that is already running picks up the config and PKI just written)
func Provision(ctx context.Context, deploy DeploymentClient, spec ProxySpec) error {
	if deploy == nil {
		return fmt.Errorf("wanproxy: deployment client is nil")
	}
	if err := spec.Validate(); err != nil {
		return err
	}

	primary := spec.PrimaryNodeAddr
	dr := spec.DRNodeAddr
	both := []string{primary, dr}
	instance := UnitInstance(spec.Resource)

	// 1. Shared PKI (generate once, reuse thereafter).
	pki, err := EnsurePKI(PKIDir)
	if err != nil {
		return fmt.Errorf("wanproxy: ensure PKI: %w", err)
	}

	// 2. Unit template (shared, idempotent).
	if err := distribute(ctx, deploy, both, unitTemplate, UnitTemplatePath, "install unit template"); err != nil {
		return err
	}

	// 3. Shared mTLS material (idempotent).
	if err := distribute(ctx, deploy, both, string(pki.CAPEM), NodeCAPath, "distribute CA"); err != nil {
		return err
	}
	if err := distribute(ctx, deploy, both, string(pki.CertPEM), NodeCertPath, "distribute cert"); err != nil {
		return err
	}
	if err := distribute(ctx, deploy, both, string(pki.KeyPEM), NodeKeyPath, "distribute key"); err != nil {
		return err
	}

	// 4. Binary. Resolved per node, because the two ends of a WAN leg are often
	//    not the same architecture — an off-site node is whatever the cloud
	//    rents, and the primary site is whatever is on the shelf.
	if err := ensureBinaries(ctx, deploy, both, spec); err != nil {
		return err
	}
	if err := requireBinaries(ctx, deploy, both); err != nil {
		return err
	}

	// 5. Per-resource configs: dialer on the primary, acceptor on the DR.
	if err := distribute(ctx, deploy, []string{primary}, RenderDialerConfig(spec), NodeConfigPath(spec.Resource), "distribute dialer config"); err != nil {
		return err
	}
	if err := distribute(ctx, deploy, []string{dr}, RenderAcceptorConfig(spec), NodeConfigPath(spec.Resource), "distribute acceptor config"); err != nil {
		return err
	}

	// 6. Reload systemd so the new unit template is visible.
	if err := run(ctx, deploy, both, "sudo systemctl daemon-reload", "systemd daemon-reload"); err != nil {
		return err
	}

	// 7. Enable, then RESTART the per-resource instance on both nodes.
	//
	// `enable --now` starts a stopped unit and does nothing to a running one, so
	// a leg that was already up keeps whatever it loaded at startup — including
	// its mTLS material. Rotate the PKI and the two ends end up on different
	// certificates while the files on disk agree, which presents as
	// "invalid peer certificate: BadSignature" on the dialer and
	// "received fatal alert: DecryptError" on the acceptor: a failure whose
	// evidence points at the certificates, which are provably identical. The
	// only clue is that one process predates the others.
	//
	// A restart is cheap here — the DR link is asynchronous and reconnects on
	// its own — and far cheaper than replication that is silently dead.
	if err := run(ctx, deploy, both, fmt.Sprintf("sudo systemctl enable %s", instance), "enable proxy"); err != nil {
		return err
	}
	if err := run(ctx, deploy, both, fmt.Sprintf("sudo systemctl restart %s", instance), "restart proxy"); err != nil {
		return err
	}

	// 8. Preflight: confirm the DR WAN port is actually reachable from the
	//    primary now that the acceptor is listening. A blocked firewall/security
	//    group fails fast here, instead of surfacing later as a DRBD resource
	//    that comes up but silently never syncs.
	if !spec.SkipReachabilityCheck {
		if err := VerifyReachability(ctx, deploy, spec); err != nil {
			return err
		}
	}

	return nil
}

// reachCmd builds a node-side command that succeeds (exit 0) iff a TCP
// connection to host:port can be opened within reachTimeoutSecs. It uses bash's
// /dev/tcp pseudo-device (present on all supported distros) guarded by timeout,
// so no extra tooling (nc/ncat) is required on the node.
func reachCmd(host string, port int) string {
	return fmt.Sprintf("timeout %d bash -c 'exec 3<>/dev/tcp/%s/%d'", reachTimeoutSecs, host, port)
}

// VerifyReachability confirms the primary node can open a TCP connection to the
// DR site's public WAN endpoint — i.e. the DR firewall / cloud security group
// actually permits the inbound mTLS port. It retries to absorb the acceptor's
// bind race after enable --now. Returning an error here is the intended
// fail-fast: without this probe a blocked port lets Provision "succeed" while
// the DRBD resource never reaches Connected.
func VerifyReachability(ctx context.Context, deploy DeploymentClient, spec ProxySpec) error {
	if deploy == nil {
		return fmt.Errorf("wanproxy: deployment client is nil")
	}
	if err := spec.Validate(); err != nil {
		return err
	}

	cmd := reachCmd(spec.DRPublicEndpoint, spec.WANPort)
	var last string
	for attempt := 1; attempt <= reachAttempts; attempt++ {
		res, err := deploy.Exec(ctx, []string{spec.PrimaryNodeAddr}, cmd)
		switch {
		case err != nil:
			last = err.Error()
		case res != nil && res.AllSuccess():
			return nil
		default:
			last = "connection refused or timed out"
		}
		if attempt < reachAttempts {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(reachRetryDelay):
			}
		}
	}
	// A closed port is a firewall only if something is listening behind it.
	unit := UnitInstance(spec.Resource)
	if res, err := deploy.Exec(ctx, []string{spec.DRNodeAddr}, "systemctl is-active "+unit); err == nil && res != nil && !res.AllSuccess() {
		state := ""
		for _, h := range res.Hosts {
			if h != nil {
				state = strings.TrimSpace(h.Output)
			}
		}
		return fmt.Errorf("wanproxy: the acceptor %s on the DR node %s is not running (%s), so nothing listens on :%d; see journalctl -u %s there",
			unit, spec.DRNodeAddr, state, spec.WANPort, unit)
	}
	return fmt.Errorf(
		"wanproxy: DR endpoint %s:%d is not reachable over TCP from the primary node %s after %d attempts — open inbound TCP :%d on the DR firewall/security group (last error: %s)",
		spec.DRPublicEndpoint, spec.WANPort, spec.PrimaryNodeAddr, reachAttempts, spec.WANPort, last)
}

// NodeProxyState is one node's view of its sds-proxy instance.
type NodeProxyState struct {
	Host   string
	Active bool // systemd reports the per-resource unit as active
}

// Metrics is the counter snapshot sds-proxy publishes. Field tags mirror the
// JSON that `sds_proxy::metrics::Snapshot` serializes — renaming one on either
// side breaks the contract.
type Metrics struct {
	// BufferUsedBytes is the un-replicated backlog: writes the local DRBD has
	// already acknowledged upstream that have NOT reached the DR site. Under
	// protocol A this IS the data-loss window if the primary is lost now, which
	// is the whole reason these metrics exist.
	BufferUsedBytes   uint64  `json:"buffer_used_bytes"`
	BufferCapBytes    uint64  `json:"buffer_cap_bytes"`
	BufferFillPercent float64 `json:"buffer_fill_percent"`

	DRBDToWANBytes uint64  `json:"drbd_to_wan_bytes"`
	WANWireBytes   uint64  `json:"wan_wire_bytes"`
	WANToDRBDBytes uint64  `json:"wan_to_drbd_bytes"`
	FramesSent     uint64  `json:"frames_sent"`
	CompressionRat float64 `json:"compression_ratio"`

	WANConnected   bool   `json:"wan_connected"`
	Reconnects     uint64 `json:"reconnects"`
	RingFullEvents uint64 `json:"ring_full_events"`
}

// ProxyStatus is the health of one WAN resource's proxy pair, suitable for
// surfacing in `resource status`, the alert monitor and the UI.
type ProxyStatus struct {
	Resource     string
	Primary      NodeProxyState
	DR           NodeProxyState
	WANReachable bool // the primary can currently reach the DR WAN endpoint

	// PrimaryMetrics is the primary side's published counters, or nil when the
	// snapshot could not be read (an older proxy build, a proxy that has not
	// published its first tick yet, or an unreachable node). Callers must treat
	// nil as "unknown", never as zero — reporting a zero backlog when the truth
	// is unknown is exactly the wrong way to be wrong here.
	PrimaryMetrics *Metrics
}

// Healthy reports whether both proxy instances are active and the WAN leg is
// reachable — the condition for the WAN resource to actually replicate.
func (s *ProxyStatus) Healthy() bool {
	return s != nil && s.Primary.Active && s.DR.Active && s.WANReachable
}

// Status reports the live health of a WAN resource's proxy pair: whether the
// sds-proxy@<resource> unit is active on each node, and whether the primary can
// currently reach the DR WAN endpoint. It is read-only (no sudo) and does a
// single reachability attempt so a status query never blocks on retries.
func Status(ctx context.Context, deploy DeploymentClient, spec ProxySpec) (*ProxyStatus, error) {
	if deploy == nil {
		return nil, fmt.Errorf("wanproxy: deployment client is nil")
	}
	if err := spec.Validate(); err != nil {
		return nil, err
	}

	instance := UnitInstance(spec.Resource)
	st := &ProxyStatus{
		Resource: spec.Resource,
		Primary:  NodeProxyState{Host: spec.PrimaryNodeAddr},
		DR:       NodeProxyState{Host: spec.DRNodeAddr},
	}

	res, err := deploy.Exec(ctx, []string{spec.PrimaryNodeAddr, spec.DRNodeAddr},
		fmt.Sprintf("systemctl is-active %s", instance))
	if err != nil {
		return nil, fmt.Errorf("wanproxy: query proxy status: %w", err)
	}
	if res != nil {
		if h := res.Hosts[spec.PrimaryNodeAddr]; h != nil {
			st.Primary.Active = h.Success
		}
		if h := res.Hosts[spec.DRNodeAddr]; h != nil {
			st.DR.Active = h.Success
		}
	}

	// Single-shot reachability: a status query must not block on the retry loop.
	st.WANReachable = Reachable(ctx, deploy, spec)

	// Counters from the primary — the side that holds the backlog. A failure here
	// leaves PrimaryMetrics nil ("unknown") rather than failing the status call:
	// losing observability must not make the resource look broken.
	st.PrimaryMetrics = readMetrics(ctx, deploy, spec.PrimaryNodeAddr, spec.Resource)

	return st, nil
}

// ReadNodeMetrics fetches and parses one node's published snapshot for a leg.
// Returns nil when it cannot be read, which callers must treat as "unknown".
func ReadNodeMetrics(ctx context.Context, deploy DeploymentClient, host, legID string) *Metrics {
	return readMetrics(ctx, deploy, host, legID)
}

// readMetrics fetches and parses a node's published snapshot. Returns nil when
// it cannot be read or parsed — an older proxy, a first tick that has not landed
// yet, or an unreachable node all look the same from here and all mean "unknown".
func readMetrics(ctx context.Context, deploy DeploymentClient, host, resource string) *Metrics {
	if deploy == nil || host == "" {
		return nil
	}
	res, err := deploy.Exec(ctx, []string{host}, fmt.Sprintf("cat %s", NodeMetricsPath(resource)))
	if err != nil || res == nil {
		return nil
	}
	hres := res.Hosts[host]
	if hres == nil || !hres.Success {
		return nil
	}
	return ParseMetrics(hres.Output)
}

// ParseMetrics decodes a published snapshot. Returns nil for anything that is not
// a usable document, so a truncated or garbage read is reported as unknown rather
// than as a resource with no backlog.
func ParseMetrics(s string) *Metrics {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil
	}
	var m Metrics
	if err := json.Unmarshal([]byte(s), &m); err != nil {
		return nil
	}
	return &m
}

// Reachable does a single TCP reachability probe from the primary node to the
// DR WAN endpoint and reports whether it succeeded. Unlike VerifyReachability it
// never retries or blocks, so it is safe on a hot status path.
func Reachable(ctx context.Context, deploy DeploymentClient, spec ProxySpec) bool {
	if deploy == nil {
		return false
	}
	r, err := deploy.Exec(ctx, []string{spec.PrimaryNodeAddr}, reachCmd(spec.DRPublicEndpoint, spec.WANPort))
	return err == nil && r != nil && r.AllSuccess()
}

// Deprovision tears down the per-resource proxy: it stops+disables
// sds-proxy@<resource> and removes its config on both nodes. The shared binary,
// certs and unit template are intentionally left in place for other resources.
//
// It is best-effort/idempotent at the shell level (missing units/files are not
// errors), so a partially-provisioned resource can still be cleaned up. The
// controller calls Deprovision AFTER `drbdadm down`.
func Deprovision(ctx context.Context, deploy DeploymentClient, resource, primaryAddr, drAddr string) error {
	if deploy == nil {
		return fmt.Errorf("wanproxy: deployment client is nil")
	}
	if strings.TrimSpace(resource) == "" {
		return fmt.Errorf("wanproxy: resource name is required")
	}
	both := []string{primaryAddr, drAddr}
	instance := UnitInstance(resource)

	// Stop + disable the instance (tolerate a not-installed unit).
	if err := run(ctx, deploy, both,
		fmt.Sprintf("sudo systemctl disable --now %s 2>/dev/null || true", instance),
		"disable/stop proxy"); err != nil {
		return err
	}

	// Remove the per-resource config (leave shared certs/binary/unit template).
	if err := run(ctx, deploy, both,
		fmt.Sprintf("sudo rm -f %s", NodeConfigPath(resource)),
		"remove proxy config"); err != nil {
		return err
	}

	return nil
}

// ensureBinary reads the controller-local sds-proxy binary and installs it at
// NodeBinaryPath on every host, then marks it executable. Distributing the same
// bytes is idempotent, so re-provisioning simply overwrites with identical
// content.
// ensureBinaries pushes the right binary to each host.
//
// Pushing one file to every node is correct only while the fleet is uniform.
// The moment it is not — an arm64 box at home and an x86_64 rental off-site —
// the same push installs an unrunnable file on half of them, and the failure
// surfaces far from the cause: systemd reports 203/EXEC on the node, while the
// operator sees a DRBD connection that never forms.
func ensureBinaries(ctx context.Context, deploy DeploymentClient, hosts []string, spec ProxySpec) error {
	// Group hosts by the local file they need, so a uniform fleet still gets a
	// single push rather than one per node.
	byPath := map[string][]string{}
	for _, h := range hosts {
		path := spec.BinaryPath
		if spec.BinaryFor != nil {
			path = spec.BinaryFor(h)
		}
		if path == "" {
			continue // pre-staged on this node
		}
		byPath[path] = append(byPath[path], h)
	}
	for path, group := range byPath {
		if err := ensureBinary(ctx, deploy, group, path); err != nil {
			return err
		}
	}
	return nil
}

func ensureBinary(ctx context.Context, deploy DeploymentClient, hosts []string, localPath string) error {
	data, err := os.ReadFile(localPath)
	if err != nil {
		return fmt.Errorf("wanproxy: read sds-proxy binary %q: %w", localPath, err)
	}
	if err := distribute(ctx, deploy, hosts, string(data), NodeBinaryPath, "distribute binary"); err != nil {
		return err
	}
	return run(ctx, deploy, hosts, fmt.Sprintf("sudo chmod 0755 %s", NodeBinaryPath), "chmod binary")
}

// distribute pushes content to remotePath on hosts and surfaces per-host
// failures, matching pkg/controller's error style.
func distribute(ctx context.Context, deploy DeploymentClient, hosts []string, content, remotePath, desc string) error {
	res, err := deploy.DistributeConfig(ctx, hosts, content, remotePath)
	if err != nil {
		return fmt.Errorf("wanproxy: %s: %w", desc, err)
	}
	if res != nil && !res.AllSuccess() {
		return fmt.Errorf("wanproxy: %s failed: %s", desc, res.FailureDetails())
	}
	return nil
}

// requireBinaries fails unless every host now has a runnable sds-proxy, pushed
// by ensureBinaries or staged beforehand. Without one the unit crash-loops with
// 203/EXEC on the node, and the only symptom anywhere else is a WAN port that
// never opens — which the reachability probe used to report as a firewall.
func requireBinaries(ctx context.Context, deploy DeploymentClient, hosts []string) error {
	res, err := deploy.Exec(ctx, hosts, "test -x "+NodeBinaryPath)
	if err != nil {
		return fmt.Errorf("wanproxy: check for %s: %w", NodeBinaryPath, err)
	}
	if res != nil && !res.AllSuccess() {
		missing := res.FailedHosts()
		sort.Strings(missing)
		return fmt.Errorf("wanproxy: no executable %s on %s, and the controller has none for that node's architecture "+
			"(it looks for %s-<arch>, or %s for its own); install sds-proxy there",
			NodeBinaryPath, strings.Join(missing, ", "), NodeBinaryPath, NodeBinaryPath)
	}
	return nil
}

// run executes cmd on hosts and surfaces per-host failures.
func run(ctx context.Context, deploy DeploymentClient, hosts []string, cmd, desc string) error {
	res, err := deploy.Exec(ctx, hosts, cmd)
	if err != nil {
		return fmt.Errorf("wanproxy: %s: %w", desc, err)
	}
	if res != nil && !res.AllSuccess() {
		return fmt.Errorf("wanproxy: %s failed: %s", desc, res.FailureDetails())
	}
	return nil
}

// MultiSpec describes WAN replication from a multi-replica primary site to a
// single DR node — the "两地三中心" shape: synchronous replicas in the
// production site, one asynchronous copy far away.
//
// The primary site keeps its normal LAN mesh (real IPs, protocol C). Only the
// legs that cross the WAN go through sds-proxy, one per primary-site node, each
// with its own WAN port and its own loopback pair so the tunnels do not collide
// on either end.
type MultiSpec struct {
	Resource string

	// PrimaryNodeAddrs are the reachable addresses of every primary-site node.
	// Each gets a dialer; each gets a matching acceptor on the DR.
	PrimaryNodeAddrs []string

	// PrimaryNodeKeys are the stable identities of those nodes — their
	// registered names — in the same order as PrimaryNodeAddrs. Leg names are
	// derived from these.
	//
	// Naming a leg after a node's address bakes a mutable fact into a systemd
	// instance name, a config filename and a metrics path. When the node is
	// renumbered (a DHCP lease that moves, a VM that returns on a different
	// address) every one of those names still refers to the old address, so the
	// running tunnel is orphaned: it keeps replicating, but nothing that
	// recomputes the name can find it, and deprovisioning silently misses it.
	//
	// A node's registered name does not change when its address does, which is
	// the property the name needs. Empty entries fall back to the address, so a
	// caller that has no names still gets the historic behaviour.
	PrimaryNodeKeys []string

	// DRNodeAddr is the DR-site node (runs every acceptor).
	DRNodeAddr string

	// DRPublicEndpoint is the DR's public WAN address the dialers connect to.
	DRPublicEndpoint string

	// BaseWANPort is the first mTLS WAN port; leg i uses BaseWANPort+i.
	BaseWANPort int

	// BaseDRBDPort is the first loopback DRBD port. Leg i uses BaseDRBDPort+i
	// on BOTH ends: the primary's dialer listens on it (the primary's DRBD
	// connects there) and the DR's acceptor dials it (the DR's DRBD binds
	// there). The two are on different machines, so they do not collide.
	BaseDRBDPort int

	PrimaryEgressAddr string
	BinaryPath        string

	// BinaryFor resolves the binary per node address; see ProxySpec.BinaryFor.
	// A fleet whose nodes do not all share one architecture must set this.
	BinaryFor func(nodeAddr string) string

	SkipReachabilityCheck bool
}

// Legs expands a MultiSpec into the per-leg ProxySpecs, in node order.
func (m MultiSpec) Legs() []ProxySpec {
	single := len(m.PrimaryNodeAddrs) == 1
	specs := make([]ProxySpec, 0, len(m.PrimaryNodeAddrs))
	for i, primary := range m.PrimaryNodeAddrs {
		specs = append(specs, ProxySpec{
			Resource:              LegID(m.Resource, m.legKey(i), single),
			PrimaryNodeAddr:       primary,
			DRNodeAddr:            m.DRNodeAddr,
			DRPublicEndpoint:      m.DRPublicEndpoint,
			WANPort:               m.BaseWANPort + i,
			DRBDPort:              m.BaseDRBDPort + i,
			PrimaryEgressAddr:     m.PrimaryEgressAddr,
			BinaryPath:            m.BinaryPath,
			BinaryFor:             m.BinaryFor,
			SkipReachabilityCheck: m.SkipReachabilityCheck,
		})
	}
	return specs
}

// legKey returns the stable identity used to name leg i, falling back to the
// node's address when no name was supplied.
func (m MultiSpec) legKey(i int) string {
	if i < len(m.PrimaryNodeKeys) {
		if k := strings.TrimSpace(m.PrimaryNodeKeys[i]); k != "" {
			return k
		}
	}
	return m.PrimaryNodeAddrs[i]
}

// Validate checks the multi-replica spec before anything is provisioned.
func (m MultiSpec) Validate() error {
	if strings.TrimSpace(m.Resource) == "" {
		return fmt.Errorf("wanproxy: resource name is required")
	}
	if len(m.PrimaryNodeAddrs) == 0 {
		return fmt.Errorf("wanproxy: at least one primary-site node is required")
	}
	if strings.TrimSpace(m.DRNodeAddr) == "" {
		return fmt.Errorf("wanproxy: DR node address is required")
	}
	seen := make(map[string]bool, len(m.PrimaryNodeAddrs))
	for _, p := range m.PrimaryNodeAddrs {
		if strings.TrimSpace(p) == "" {
			return fmt.Errorf("wanproxy: empty primary-site node address")
		}
		if p == m.DRNodeAddr {
			return fmt.Errorf("wanproxy: node %q cannot be both a primary-site replica and the DR site", p)
		}
		if seen[p] {
			return fmt.Errorf("wanproxy: duplicate primary-site node %q", p)
		}
		seen[p] = true
	}
	// Each leg consumes one WAN port and two loopback ports; make sure the
	// ranges fit and cannot overlap.
	last := m.BaseWANPort + len(m.PrimaryNodeAddrs) - 1
	if m.BaseWANPort <= 0 || last > 65535 {
		return fmt.Errorf("wanproxy: WAN port range %d-%d out of range", m.BaseWANPort, last)
	}
	lastDRBD := m.BaseDRBDPort + len(m.PrimaryNodeAddrs) - 1
	if m.BaseDRBDPort <= 0 || lastDRBD > 65535 {
		return fmt.Errorf("wanproxy: DRBD loopback port range %d-%d out of range", m.BaseDRBDPort, lastDRBD)
	}
	return nil
}

// ProvisionMulti sets up every WAN leg of a multi-replica primary site.
//
// Shared material (PKI, binary, unit template) is pushed once per node; the
// per-leg configs and systemd instances are then created leg by leg. A failure
// part-way leaves the already-provisioned legs running — Deprovision/
// DeprovisionMulti is the way back.
func ProvisionMulti(ctx context.Context, deploy DeploymentClient, spec MultiSpec) error {
	if deploy == nil {
		return fmt.Errorf("wanproxy: deployment client is nil")
	}
	if err := spec.Validate(); err != nil {
		return err
	}

	legs := spec.Legs()
	allNodes := append(append([]string{}, spec.PrimaryNodeAddrs...), spec.DRNodeAddr)

	// Shared, idempotent material first — pushing it per leg would repeat the
	// same bytes N times to the DR node.
	pki, err := EnsurePKI(PKIDir)
	if err != nil {
		return fmt.Errorf("wanproxy: ensure PKI: %w", err)
	}
	if err := distribute(ctx, deploy, allNodes, unitTemplate, UnitTemplatePath, "install unit template"); err != nil {
		return err
	}
	if err := distribute(ctx, deploy, allNodes, string(pki.CAPEM), NodeCAPath, "distribute CA"); err != nil {
		return err
	}
	if err := distribute(ctx, deploy, allNodes, string(pki.CertPEM), NodeCertPath, "distribute cert"); err != nil {
		return err
	}
	if err := distribute(ctx, deploy, allNodes, string(pki.KeyPEM), NodeKeyPath, "distribute key"); err != nil {
		return err
	}
	// Per node, like Provision: BinaryFor used to be ignored here, so a
	// controller holding the right binary for each architecture pushed nothing.
	if err := ensureBinaries(ctx, deploy, allNodes,
		ProxySpec{BinaryPath: spec.BinaryPath, BinaryFor: spec.BinaryFor}); err != nil {
		return err
	}
	if err := requireBinaries(ctx, deploy, allNodes); err != nil {
		return err
	}
	if err := run(ctx, deploy, allNodes, "sudo systemctl daemon-reload", "systemd daemon-reload"); err != nil {
		return err
	}

	for _, leg := range legs {
		// Both ends of a leg use the same leg port on their own loopback: the
		// dialer listens on it (the primary's DRBD connects there) and the
		// acceptor dials it (the DR's DRBD binds there). No offset — the two
		// numbers live on different machines.
		acceptor := leg

		if err := distribute(ctx, deploy, []string{leg.PrimaryNodeAddr},
			RenderDialerConfig(leg), NodeConfigPath(leg.Resource), "distribute dialer config"); err != nil {
			return err
		}
		if err := distribute(ctx, deploy, []string{spec.DRNodeAddr},
			RenderAcceptorConfig(acceptor), NodeConfigPath(leg.Resource), "distribute acceptor config"); err != nil {
			return err
		}
		both := []string{leg.PrimaryNodeAddr, spec.DRNodeAddr}
		// Restart, not `enable --now`: a running leg ignores the config and PKI
		// just written for it. See the same step in Provision for what that
		// looks like when it goes wrong.
		if err := run(ctx, deploy, both,
			fmt.Sprintf("sudo systemctl enable %s", UnitInstance(leg.Resource)), "enable proxy"); err != nil {
			return err
		}
		if err := run(ctx, deploy, both,
			fmt.Sprintf("sudo systemctl restart %s", UnitInstance(leg.Resource)), "restart proxy"); err != nil {
			return err
		}
		if !spec.SkipReachabilityCheck {
			if err := VerifyReachability(ctx, deploy, leg); err != nil {
				return err
			}
		}
	}
	return nil
}

// DeprovisionMulti tears down every WAN leg of a multi-replica primary site.
// It is best-effort per leg: one unreachable node must not strand the rest.
func DeprovisionMulti(ctx context.Context, deploy DeploymentClient, spec MultiSpec) error {
	if deploy == nil {
		return fmt.Errorf("wanproxy: deployment client is nil")
	}
	var firstErr error
	for _, leg := range spec.Legs() {
		if err := Deprovision(ctx, deploy, leg.Resource, leg.PrimaryNodeAddr, leg.DRNodeAddr); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

// LegStatus is one primary-site node's WAN leg: the dialer on that node and the
// matching acceptor on the DR.
type LegStatus struct {
	// LegID is the systemd instance name, e.g. "openclaw_192-168-123-227".
	LegID string
	// PrimaryHost is the primary-site node this leg belongs to.
	PrimaryHost   string
	PrimaryActive bool
	DRActive      bool
}

// Healthy reports whether both ends of the leg are running.
func (l LegStatus) Healthy() bool { return l.PrimaryActive && l.DRActive }

// MultiStatus is the health of every WAN leg of one resource.
type MultiStatus struct {
	Resource string
	DRHost   string
	Legs     []LegStatus
	// WANReachable is whether the DR's public endpoint answered a probe. It is
	// reported per resource rather than per leg: the legs share one endpoint and
	// differ only by port, and probing every one of them on a 30s status path
	// costs a cross-WAN round trip per replica.
	WANReachable bool
}

// Healthy reports whether every leg is up and the DR endpoint answered.
func (s *MultiStatus) Healthy() bool {
	if s == nil || len(s.Legs) == 0 {
		return false
	}
	for _, l := range s.Legs {
		if !l.Healthy() {
			return false
		}
	}
	return s.WANReachable
}

// Unhealthy returns the legs that are not fully up, in spec order.
func (s *MultiStatus) Unhealthy() []LegStatus {
	if s == nil {
		return nil
	}
	var out []LegStatus
	for _, l := range s.Legs {
		if !l.Healthy() {
			out = append(out, l)
		}
	}
	return out
}

// StatusMulti reports the live health of every leg of a multi-replica WAN
// resource.
//
// A resource with N primary-site replicas has N independent tunnels, each a
// separately-named systemd instance (see LegID). Checking a single
// "sds-proxy@<resource>" unit — as a per-resource status query would — names a
// unit that exists on no node once N > 1, so a perfectly healthy WAN reads as
// completely down.
//
// Every host is queried in one Exec with one command: the per-leg unit names
// differ by host, so instead of asking each host about a specific unit, each
// host lists the instances it has for this resource and the result is matched
// up here.
func StatusMulti(ctx context.Context, deploy DeploymentClient, m MultiSpec) (*MultiStatus, error) {
	if deploy == nil {
		return nil, fmt.Errorf("wanproxy: deployment client is nil")
	}
	if err := m.Validate(); err != nil {
		return nil, err
	}

	legs := m.Legs()
	hosts := append(append([]string{}, m.PrimaryNodeAddrs...), m.DRNodeAddr)

	// `systemctl is-active` takes one unit; listing is what lets a single
	// command serve hosts that each run different instances. --plain drops the
	// tree glyphs and --no-legend the trailing prose, leaving parseable rows.
	cmd := fmt.Sprintf(
		"systemctl list-units --all --plain --no-legend --no-pager '%s*' 2>/dev/null || true",
		UnitInstance(m.Resource))
	res, err := deploy.Exec(ctx, hosts, cmd)
	if err != nil {
		return nil, fmt.Errorf("wanproxy: query proxy status: %w", err)
	}

	// active[host][unit] — absent means the host does not have that instance,
	// which is indistinguishable from it being stopped and means the same thing.
	active := make(map[string]map[string]bool, len(hosts))
	if res != nil {
		for host, h := range res.Hosts {
			if h == nil || !h.Success {
				continue
			}
			active[host] = parseActiveUnits(h.Output)
		}
	}

	st := &MultiStatus{Resource: m.Resource, DRHost: m.DRNodeAddr}
	for _, leg := range legs {
		unit := UnitInstance(leg.Resource) + ".service"
		legID := leg.Resource

		// A leg's name encodes the address the node had when its tunnel was
		// provisioned. Renumber the node — a DHCP lease that moves, a VM that
		// comes back on a different address — and the computed name stops
		// matching the instance that is actually running and replicating fine.
		//
		// A node runs at most one leg per resource, so when the computed name is
		// absent but the host has exactly one instance for this resource, that
		// instance IS this node's leg. Trusting it reports the tunnel's real
		// state instead of a phantom outage, and keeps the DR side keyed to the
		// same name. A host with no instance still reports down, under the name
		// that was expected, which is what an operator needs to go looking for.
		if !active[leg.PrimaryNodeAddr][unit] {
			if found := soleUnitFor(active[leg.PrimaryNodeAddr]); found != "" {
				unit = found
				legID = strings.TrimSuffix(strings.TrimPrefix(found, "sds-proxy@"), ".service")
			}
		}

		st.Legs = append(st.Legs, LegStatus{
			LegID:         legID,
			PrimaryHost:   leg.PrimaryNodeAddr,
			PrimaryActive: active[leg.PrimaryNodeAddr][unit],
			DRActive:      active[m.DRNodeAddr][unit],
		})
	}

	// One probe, from the first primary-site node that has a live dialer — that
	// node is known to be up, so a failure here is about the WAN path rather
	// than about the node being down.
	if probe := reachProbeSpec(m, legs, st); probe != nil {
		st.WANReachable = Reachable(ctx, deploy, *probe)
	}
	return st, nil
}

// reachProbeSpec picks the leg to probe the DR endpoint from: the first whose
// primary dialer is running, falling back to the first leg. Probing from a node
// that is itself down reports "WAN unreachable" for what is really a dead node,
// which sends people looking at the wrong end of the link.
func reachProbeSpec(m MultiSpec, legs []ProxySpec, st *MultiStatus) *ProxySpec {
	if len(legs) == 0 {
		return nil
	}
	for i, l := range st.Legs {
		if l.PrimaryActive {
			return &legs[i]
		}
	}
	return &legs[0]
}

// soleUnitFor returns the host's only proxy instance, or "" when it has none or
// more than one. Ambiguity is left to the caller's computed name rather than
// guessed at: picking arbitrarily among several would report some other
// resource's leg as this one's.
func soleUnitFor(units map[string]bool) string {
	if len(units) != 1 {
		return ""
	}
	for u := range units {
		return u
	}
	return ""
}

// parseActiveUnits reads `systemctl list-units --plain --no-legend` output into
// unit -> active. Rows are "UNIT LOAD ACTIVE SUB DESCRIPTION...".
func parseActiveUnits(out string) map[string]bool {
	units := map[string]bool{}
	for _, line := range strings.Split(out, "\n") {
		fields := strings.Fields(strings.TrimSpace(line))
		if len(fields) < 3 {
			continue
		}
		units[fields[0]] = fields[2] == "active"
	}
	return units
}

// StaleLeg is a proxy instance found on a node that the current spec does not
// account for — typically a leg left behind after the node was renumbered, or
// after a replica was removed from the resource.
type StaleLeg struct {
	Host  string
	LegID string
}

// FindStaleLegs reports proxy instances for this resource that the spec does
// not expect: on a primary-site node, anything that is not that node's own leg;
// on the DR node, anything that is not one of the legs.
//
// Matching is deliberately narrow. A resource's instances are exactly
// "<resource>" and "<resource>_<key>", so a resource named "foo" must not sweep
// up "foobar"'s instances. It is still possible to construct a collision by
// naming one resource "<other>_<something>"; such a name is rejected at
// creation (see ValidateResourceNameForWAN) precisely so this stays safe.
func FindStaleLegs(ctx context.Context, deploy DeploymentClient, m MultiSpec) ([]StaleLeg, error) {
	if deploy == nil {
		return nil, fmt.Errorf("wanproxy: deployment client is nil")
	}
	if err := m.Validate(); err != nil {
		return nil, err
	}

	legs := m.Legs()
	hosts := append(append([]string{}, m.PrimaryNodeAddrs...), m.DRNodeAddr)

	res, err := deploy.Exec(ctx, hosts, fmt.Sprintf(
		"systemctl list-units --all --plain --no-legend --no-pager '%s*' 2>/dev/null || true",
		UnitInstance(m.Resource)))
	if err != nil {
		return nil, fmt.Errorf("wanproxy: list proxy instances: %w", err)
	}

	// expected[host] is the set of leg ids that host is supposed to run.
	expected := make(map[string]map[string]bool, len(hosts))
	for _, h := range hosts {
		expected[h] = map[string]bool{}
	}
	for _, leg := range legs {
		expected[leg.PrimaryNodeAddr][leg.Resource] = true
		expected[m.DRNodeAddr][leg.Resource] = true
	}

	var stale []StaleLeg
	if res != nil {
		for _, host := range hosts {
			h := res.Hosts[host]
			if h == nil || !h.Success {
				// A node that cannot be reached is not evidence of a stale leg.
				continue
			}
			for unit := range parseActiveUnits(h.Output) {
				legID := strings.TrimSuffix(strings.TrimPrefix(unit, "sds-proxy@"), ".service")
				if !belongsToResource(legID, m.Resource) || expected[host][legID] {
					continue
				}
				stale = append(stale, StaleLeg{Host: host, LegID: legID})
			}
		}
	}
	sort.Slice(stale, func(i, j int) bool {
		if stale[i].Host != stale[j].Host {
			return stale[i].Host < stale[j].Host
		}
		return stale[i].LegID < stale[j].LegID
	})
	return stale, nil
}

// belongsToResource reports whether a leg id is one of this resource's, i.e.
// exactly the resource (single-replica naming) or "<resource>_<key>".
func belongsToResource(legID, resource string) bool {
	return legID == resource || strings.HasPrefix(legID, resource+"_")
}

// RemoveStaleLegs stops, disables and removes the config of each stale leg.
//
// It is best-effort per leg so one unreachable node cannot strand the rest, and
// it only ever touches instances FindStaleLegs identified — a leg the spec does
// expect is never removed, so calling this on a healthy resource is a no-op.
func RemoveStaleLegs(ctx context.Context, deploy DeploymentClient, stale []StaleLeg) error {
	if deploy == nil {
		return fmt.Errorf("wanproxy: deployment client is nil")
	}
	var firstErr error
	for _, s := range stale {
		if err := run(ctx, deploy, []string{s.Host},
			fmt.Sprintf("sudo systemctl disable --now %s 2>/dev/null || true", UnitInstance(s.LegID)),
			"disable stale proxy leg"); err != nil && firstErr == nil {
			firstErr = err
		}
		if err := run(ctx, deploy, []string{s.Host},
			fmt.Sprintf("sudo rm -f %s %s", NodeConfigPath(s.LegID), NodeMetricsPath(s.LegID)),
			"remove stale proxy config"); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

// ValidateResourceNameForWAN rejects a WAN resource name that could be mistaken
// for another resource's leg. Leg instances are named "<resource>_<key>", so a
// resource literally named like one of those would make the two indistinguish-
// able — and FindStaleLegs would consider one resource's legs stale for the
// other. Refusing the name at creation is cheaper than disambiguating forever.
func ValidateResourceNameForWAN(name string, existing []string) error {
	for _, other := range existing {
		if other == name {
			continue
		}
		if strings.HasPrefix(name, other+"_") {
			return fmt.Errorf(
				"wanproxy: resource name %q collides with WAN leg names of resource %q; choose a name that is not %q_<suffix>",
				name, other, other)
		}
	}
	return nil
}
