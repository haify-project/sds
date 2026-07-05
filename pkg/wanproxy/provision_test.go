package wanproxy

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// event records one call against the fake deployment client, in order, so a
// test can assert the exact sequence of file-pushes and commands.
type event struct {
	kind    string // "distribute" or "exec"
	hosts   []string
	path    string // distribute: remote path
	content string // distribute: content
	cmd     string // exec: command
}

// fakeDeploy is an in-memory DeploymentClient: no SSH, no network. It records
// every call in order and lets a test inject a failure at a given step.
type fakeDeploy struct {
	events []event

	// failDistributePath / failExecSubstr trigger a per-host failure Result when
	// set and matched, to exercise the error paths.
	failDistributePath string
	failExecSubstr     string
}

func (f *fakeDeploy) DistributeConfig(_ context.Context, hosts []string, content, remotePath string) (*Result, error) {
	f.events = append(f.events, event{kind: "distribute", hosts: append([]string(nil), hosts...), path: remotePath, content: content})
	return f.result(hosts, f.failDistributePath != "" && f.failDistributePath == remotePath), nil
}

func (f *fakeDeploy) Exec(_ context.Context, hosts []string, cmd string) (*Result, error) {
	f.events = append(f.events, event{kind: "exec", hosts: append([]string(nil), hosts...), cmd: cmd})
	return f.result(hosts, f.failExecSubstr != "" && strings.Contains(cmd, f.failExecSubstr)), nil
}

func (f *fakeDeploy) result(hosts []string, fail bool) *Result {
	r := &Result{Hosts: map[string]*HostResult{}}
	for _, h := range hosts {
		r.Hosts[h] = &HostResult{Host: h, Success: !fail}
	}
	return r
}

func newSpecWithTempPKI(t *testing.T) ProxySpec {
	t.Helper()
	// Redirect the package PKI cache to a temp dir so the test never writes to
	// /var/lib/sds.
	prev := PKIDir
	PKIDir = t.TempDir()
	t.Cleanup(func() { PKIDir = prev })
	return sampleSpec()
}

func TestProvisionSequence(t *testing.T) {
	spec := newSpecWithTempPKI(t)
	f := &fakeDeploy{}

	if err := Provision(context.Background(), f, spec); err != nil {
		t.Fatalf("Provision: %v", err)
	}

	primary := spec.PrimaryNodeAddr
	dr := spec.DRNodeAddr
	both := []string{primary, dr}
	cfgPath := NodeConfigPath(spec.Resource)
	instance := UnitInstance(spec.Resource)

	// Expected ordered sequence (binary skipped: BinaryPath empty).
	want := []event{
		{kind: "distribute", hosts: both, path: UnitTemplatePath},
		{kind: "distribute", hosts: both, path: NodeCAPath},
		{kind: "distribute", hosts: both, path: NodeCertPath},
		{kind: "distribute", hosts: both, path: NodeKeyPath},
		{kind: "distribute", hosts: []string{primary}, path: cfgPath}, // dialer
		{kind: "distribute", hosts: []string{dr}, path: cfgPath},      // acceptor
		{kind: "exec", hosts: both, cmd: "sudo systemctl daemon-reload"},
		{kind: "exec", hosts: both, cmd: "sudo systemctl enable --now " + instance},
	}

	if len(f.events) != len(want) {
		t.Fatalf("event count = %d, want %d\ngot: %s", len(f.events), len(want), dumpEvents(f.events))
	}
	for i, w := range want {
		got := f.events[i]
		if got.kind != w.kind {
			t.Fatalf("event %d kind = %q, want %q", i, got.kind, w.kind)
		}
		if !sameHosts(got.hosts, w.hosts) {
			t.Fatalf("event %d hosts = %v, want %v", i, got.hosts, w.hosts)
		}
		if w.kind == "distribute" && got.path != w.path {
			t.Fatalf("event %d path = %q, want %q", i, got.path, w.path)
		}
		if w.kind == "exec" && got.cmd != w.cmd {
			t.Fatalf("event %d cmd = %q, want %q", i, got.cmd, w.cmd)
		}
	}

	// The dialer config went to the primary, the acceptor config to the DR.
	if got := f.events[4].content; got != RenderDialerConfig(spec) {
		t.Fatalf("primary did not receive the dialer config:\n%s", got)
	}
	if got := f.events[5].content; got != RenderAcceptorConfig(spec) {
		t.Fatalf("DR did not receive the acceptor config:\n%s", got)
	}

	// enable --now must come after every config/cert push.
	enableIdx := lastExecIndex(f.events, "enable --now")
	for i, e := range f.events {
		if e.kind == "distribute" && i > enableIdx {
			t.Fatalf("config push at %d happened after enable/start at %d", i, enableIdx)
		}
	}
}

func TestProvisionPushesBinaryWhenPathSet(t *testing.T) {
	spec := newSpecWithTempPKI(t)

	// A stand-in "binary" file; Provision just distributes its bytes.
	binPath := filepath.Join(t.TempDir(), "sds-proxy")
	if err := os.WriteFile(binPath, []byte("\x7fELF fake binary"), 0o755); err != nil {
		t.Fatalf("write fake binary: %v", err)
	}
	spec.BinaryPath = binPath

	f := &fakeDeploy{}
	if err := Provision(context.Background(), f, spec); err != nil {
		t.Fatalf("Provision: %v", err)
	}

	// The binary must be pushed to NodeBinaryPath on both nodes, then chmod'd.
	var pushed, chmodded bool
	for _, e := range f.events {
		if e.kind == "distribute" && e.path == NodeBinaryPath {
			pushed = true
			if e.content != "\x7fELF fake binary" {
				t.Fatalf("binary content mismatch: %q", e.content)
			}
		}
		if e.kind == "exec" && strings.Contains(e.cmd, "chmod") && strings.Contains(e.cmd, NodeBinaryPath) {
			chmodded = true
		}
	}
	if !pushed {
		t.Fatalf("binary was not pushed to %s", NodeBinaryPath)
	}
	if !chmodded {
		t.Fatalf("binary was not made executable")
	}
}

func TestProvisionSurfacesDistributeFailure(t *testing.T) {
	spec := newSpecWithTempPKI(t)
	f := &fakeDeploy{failDistributePath: NodeCAPath}

	err := Provision(context.Background(), f, spec)
	if err == nil {
		t.Fatal("expected Provision to fail when a distribute step fails")
	}
	if !strings.Contains(err.Error(), "distribute CA") {
		t.Fatalf("error = %v, want it to mention the failing step", err)
	}
}

func TestProvisionSurfacesExecFailure(t *testing.T) {
	spec := newSpecWithTempPKI(t)
	f := &fakeDeploy{failExecSubstr: "enable --now"}

	err := Provision(context.Background(), f, spec)
	if err == nil {
		t.Fatal("expected Provision to fail when enable/start fails")
	}
	if !strings.Contains(err.Error(), "enable/start proxy") {
		t.Fatalf("error = %v, want it to mention enable/start", err)
	}
}

func TestProvisionRejectsInvalidSpec(t *testing.T) {
	spec := newSpecWithTempPKI(t)
	spec.WANPort = 0
	f := &fakeDeploy{}
	if err := Provision(context.Background(), f, spec); err == nil {
		t.Fatal("expected Provision to reject an invalid spec")
	}
	if len(f.events) != 0 {
		t.Fatalf("no node should be touched for an invalid spec, got %d events", len(f.events))
	}
}

func TestDeprovisionSequence(t *testing.T) {
	f := &fakeDeploy{}
	resource := "data"
	primary := "10.0.0.1"
	dr := "10.0.0.2"

	if err := Deprovision(context.Background(), f, resource, primary, dr); err != nil {
		t.Fatalf("Deprovision: %v", err)
	}

	both := []string{primary, dr}
	instance := UnitInstance(resource)
	want := []event{
		{kind: "exec", hosts: both, cmd: "sudo systemctl disable --now " + instance + " 2>/dev/null || true"},
		{kind: "exec", hosts: both, cmd: "sudo rm -f " + NodeConfigPath(resource)},
	}
	if len(f.events) != len(want) {
		t.Fatalf("event count = %d, want %d\ngot: %s", len(f.events), len(want), dumpEvents(f.events))
	}
	for i, w := range want {
		got := f.events[i]
		if got.kind != w.kind || got.cmd != w.cmd || !sameHosts(got.hosts, w.hosts) {
			t.Fatalf("event %d = %+v, want %+v", i, got, w)
		}
	}

	// Deprovision must NOT remove shared material (binary, certs, unit template).
	for _, e := range f.events {
		for _, shared := range []string{NodeBinaryPath, NodeCAPath, NodeCertPath, NodeKeyPath, UnitTemplatePath} {
			if strings.Contains(e.cmd, shared) {
				t.Fatalf("Deprovision must not touch shared %s (cmd=%q)", shared, e.cmd)
			}
		}
	}
}

func sameHosts(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func lastExecIndex(events []event, substr string) int {
	idx := -1
	for i, e := range events {
		if e.kind == "exec" && strings.Contains(e.cmd, substr) {
			idx = i
		}
	}
	return idx
}

func dumpEvents(events []event) string {
	var b strings.Builder
	for i, e := range events {
		if e.kind == "distribute" {
			b.WriteString("  [" + itoa(i) + "] distribute " + e.path + " -> " + strings.Join(e.hosts, ",") + "\n")
		} else {
			b.WriteString("  [" + itoa(i) + "] exec " + e.cmd + " -> " + strings.Join(e.hosts, ",") + "\n")
		}
	}
	return b.String()
}

func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	var digits []byte
	for i > 0 {
		digits = append([]byte{byte('0' + i%10)}, digits...)
		i /= 10
	}
	return string(digits)
}
