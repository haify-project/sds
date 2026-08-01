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
	BinaryPath string

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
//  7. systemctl enable --now sds-proxy@<resource>
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

	// 4. Binary (shared; skipped when pre-staged).
	if spec.BinaryPath != "" {
		if err := ensureBinary(ctx, deploy, both, spec.BinaryPath); err != nil {
			return err
		}
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

	// 7. Enable + start the per-resource instance on both nodes.
	if err := run(ctx, deploy, both, fmt.Sprintf("sudo systemctl enable --now %s", instance), "enable/start proxy"); err != nil {
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
