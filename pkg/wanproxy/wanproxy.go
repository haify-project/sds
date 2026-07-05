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
	"fmt"
	"os"
	"strings"
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

	// DRBDPort is the resource's DRBD port P. The dialer binds 127.0.0.1:P and
	// accepts the primary's DRBD; the acceptor dials 127.0.0.1:P to reach the
	// DR's DRBD. (The nodes' own DRBD binds are P and P+9 respectively; see
	// generateDrbdConfig's WAN branch.)
	DRBDPort int

	// BinaryPath is the controller-local path to the sds-proxy binary to push to
	// both nodes (installed at NodeBinaryPath). When empty the binary push is
	// skipped, assuming it was pre-staged on the nodes.
	BinaryPath string
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
		return fmt.Errorf("wanproxy: %s failed on hosts: %v", desc, res.FailedHosts())
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
		return fmt.Errorf("wanproxy: %s failed on hosts: %v", desc, res.FailedHosts())
	}
	return nil
}
