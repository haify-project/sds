// Package wanproxy orchestrates the per-resource sds-proxy pair that carries a
// DRBD resource's replication across the internet (primary site <-> DR site).
//
// It is the controller-side implementation of the opt-in WAN replication
// feature described in docs/design/wan-replication.md. It is only
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
Description=Haify WAN replication proxy for %i
Documentation=https://github.com/haify-project/sds
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
