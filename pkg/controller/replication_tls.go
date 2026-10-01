package controller

import (
	"context"
	"encoding/base64"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"go.uber.org/zap"

	"github.com/liliang-cn/sds/pkg/deployment"
	"github.com/liliang-cn/sds/pkg/drbdtls"
)

// Encrypted replication. DRBD 9.2+ can run a connection over kernel TLS: the
// kernel asks tlshd (ktls-utils) to do the handshake and then encrypts in
// place. tlshd presents the node's certificate and accepts a peer whose
// certificate chains to a CA in the system trust store — ktls-utils before
// 0.10 has no setting for a private one. So preparing a node means: a key made
// on the node, a certificate from the controller's replication CA, that CA in
// the system trust store, tlshd configured and running, and the tls module
// loaded at boot.
//
// The CA is only trusted for what it signs, and it only signs replication
// certificates, but it does join the system trust store: anything on the node
// that validates against that store will accept a certificate it issued. Its
// key stays with the controller, next to the database.

const (
	nodeTLSDir    = "/etc/sds/drbd-tls"
	trustAnchor   = "sds-drbd-ca.crt"
	tlshdConfPath = "/etc/tlshd.conf"
)

// NodeTLSState is whether one node can carry an encrypted DRBD connection.
type NodeTLSState struct {
	Node    string
	Ready   bool
	Problem string
	Expires time.Time
}

// replicationCA loads, or on first use creates, the replication CA.
func (rm *ResourceManager) replicationCA() (*drbdtls.CA, error) {
	dir := "/var/lib/sds/drbd-tls"
	if rm.controller.config != nil && rm.controller.config.Database.Path != "" {
		dir = filepath.Join(filepath.Dir(rm.controller.config.Database.Path), "drbd-tls")
	}
	return drbdtls.Ensure(dir)
}

// tlsNodePreflight checks what a node needs before it can be given a
// certificate, makes its key if it has none, and prints a certificate request.
const tlsNodePreflight = `set -e
missing() { echo "SDS_MISSING=$1"; exit 3; }
command -v tlshd >/dev/null || missing "tlshd (install ktls-utils)"
command -v openssl >/dev/null || missing "openssl"
sudo modprobe tls 2>/dev/null || missing "the kernel tls module (CONFIG_TLS)"
sudo drbdsetup net-options --help 2>&1 | grep -q -- '--tls=' || missing "DRBD with TLS support (9.2 or later)"
[ -d /usr/local/share/ca-certificates ] || [ -d /etc/pki/ca-trust/source/anchors ] || missing "a system trust store (update-ca-certificates or update-ca-trust)"
sudo install -d -m 700 ` + nodeTLSDir + `
[ -s ` + nodeTLSDir + `/node.key ] || sudo openssl ecparam -name prime256v1 -genkey -noout -out ` + nodeTLSDir + `/node.key
sudo chmod 600 ` + nodeTLSDir + `/node.key
echo SDS_CSR_BEGIN
sudo openssl req -new -key ` + nodeTLSDir + `/node.key -subj "/CN=%s"
echo SDS_CSR_END`

// tlsNodeInstall installs the node certificate and the CA, points tlshd at
// them and restarts it. The packaged tlshd.conf is kept once as
// tlshd.conf.sds-orig. Restarting tlshd does not touch established
// connections: the handshake is long over by then.
const tlsNodeInstall = `set -e
D=` + nodeTLSDir + `
echo %s | base64 -d | sudo tee $D/node.crt >/dev/null
echo %s | base64 -d | sudo tee $D/ca.crt >/dev/null
sudo chmod 644 $D/node.crt $D/ca.crt
if [ -d /usr/local/share/ca-certificates ]; then
  sudo cp $D/ca.crt /usr/local/share/ca-certificates/` + trustAnchor + ` && sudo update-ca-certificates >/dev/null
else
  sudo cp $D/ca.crt /etc/pki/ca-trust/source/anchors/` + trustAnchor + ` && sudo update-ca-trust
fi
[ -e ` + tlshdConfPath + `.sds-orig ] || sudo cp ` + tlshdConfPath + ` ` + tlshdConfPath + `.sds-orig 2>/dev/null || true
printf '[main]\ndebug=0\ntlsdebug=0\nnl_debug=0\n\n[authenticate.client]\nx509.certificate= %%s/node.crt\nx509.private_key= %%s/node.key\n\n[authenticate.server]\nx509.certificate= %%s/node.crt\nx509.private_key= %%s/node.key\n' $D $D $D $D | sudo tee ` + tlshdConfPath + ` >/dev/null
echo tls | sudo tee /etc/modules-load.d/sds-drbd-tls.conf >/dev/null
sudo systemctl enable tlshd >/dev/null 2>&1
sudo systemctl restart tlshd
systemctl is-active tlshd`

// tlsNodeStatus reports what a node has; the controller judges it.
const tlsNodeStatus = `echo "SDS_TLSHD=$(systemctl is-active tlshd 2>/dev/null)"
grep -q "` + nodeTLSDir + `/node.crt" ` + tlshdConfPath + ` 2>/dev/null && echo SDS_CONF=yes
lsmod 2>/dev/null | grep -q '^tls ' && echo SDS_MODULE=yes
for f in /usr/local/share/ca-certificates/` + trustAnchor + ` /etc/pki/ca-trust/source/anchors/` + trustAnchor + `; do [ -s "$f" ] && echo "SDS_TRUST=$(base64 -w0 "$f")"; done
[ -s ` + nodeTLSDir + `/node.crt ] && echo "SDS_CERT=$(base64 -w0 ` + nodeTLSDir + `/node.crt)"
true`

// SetupReplicationTLS prepares nodes (all registered ones when nodes is empty)
// to carry encrypted DRBD connections. Each node is reported on its own; one
// that cannot be prepared does not stop the others.
func (rm *ResourceManager) SetupReplicationTLS(ctx context.Context, nodes []string) ([]NodeTLSState, error) {
	ca, err := rm.replicationCA()
	if err != nil {
		return nil, err
	}
	targets, err := rm.tlsTargets(ctx, nodes)
	if err != nil {
		return nil, err
	}
	out := make([]NodeTLSState, 0, len(targets))
	for _, n := range targets {
		st := NodeTLSState{Node: n.Name}
		if err := rm.setupNodeTLS(ctx, ca, n); err != nil {
			st.Problem = err.Error()
			rm.controller.logger.Warn("Replication TLS setup failed", zap.String("node", n.Name), zap.Error(err))
		}
		out = append(out, st)
	}
	status, err := rm.ReplicationTLSStatus(ctx, nodes)
	if err != nil {
		return out, nil
	}
	for i := range out {
		for _, s := range status {
			if s.Node == out[i].Node && out[i].Problem == "" {
				out[i] = s
			}
		}
	}
	return out, nil
}

func (rm *ResourceManager) setupNodeTLS(ctx context.Context, ca *drbdtls.CA, n *NodeInfo) error {
	host := rm.controller.ResolveHost(n.Name)
	res, err := rm.deployment.Exec(ctx, []string{host}, "bash -c "+shellSingleQuote(fmt.Sprintf(tlsNodePreflight, n.Name)))
	if err != nil {
		return err
	}
	output := hostOutput(res, host)
	if v, ok := tlsField(output, "SDS_MISSING"); ok {
		return fmt.Errorf("missing %s", v)
	}
	if !res.AllSuccess() {
		return fmt.Errorf("preparing the key failed: %s", res.FailureDetails())
	}
	_, rest, _ := strings.Cut(output, "SDS_CSR_BEGIN")
	csr, _, _ := strings.Cut(rest, "SDS_CSR_END")
	cert, err := ca.Sign([]byte(strings.TrimSpace(csr)), n.Name, []string{n.Address})
	if err != nil {
		return err
	}
	install := fmt.Sprintf(tlsNodeInstall,
		base64.StdEncoding.EncodeToString(cert), base64.StdEncoding.EncodeToString(ca.CertPEM))
	res, err = rm.deployment.Exec(ctx, []string{host}, "bash -c "+shellSingleQuote(install))
	if err != nil {
		return err
	}
	if !res.AllSuccess() {
		return fmt.Errorf("installing the certificate failed: %s", res.FailureDetails())
	}
	return nil
}

// ReplicationTLSStatus reports, per node, whether it can carry an encrypted
// connection today, and if not, what is missing.
func (rm *ResourceManager) ReplicationTLSStatus(ctx context.Context, nodes []string) ([]NodeTLSState, error) {
	ca, err := rm.replicationCA()
	if err != nil {
		return nil, err
	}
	targets, err := rm.tlsTargets(ctx, nodes)
	if err != nil {
		return nil, err
	}
	hosts := make([]string, 0, len(targets))
	for _, n := range targets {
		hosts = append(hosts, rm.controller.ResolveHost(n.Name))
	}
	res, err := rm.deployment.Exec(ctx, hosts, "bash -c "+shellSingleQuote(tlsNodeStatus))
	if err != nil {
		return nil, err
	}
	out := make([]NodeTLSState, 0, len(targets))
	for i, n := range targets {
		out = append(out, judgeNodeTLS(ca, n.Name, hostOutput(res, hosts[i]), time.Now()))
	}
	return out, nil
}

// judgeNodeTLS turns what a node reported into a verdict, naming the first
// thing that stops it.
func judgeNodeTLS(ca *drbdtls.CA, node, output string, now time.Time) NodeTLSState {
	st := NodeTLSState{Node: node}
	certB64, hasCert := tlsField(output, "SDS_CERT")
	trustB64, hasTrust := tlsField(output, "SDS_TRUST")
	if tlshd, _ := tlsField(output, "SDS_TLSHD"); !hasCert {
		st.Problem = "no replication certificate; run `sds-cli replication-tls setup`"
		return st
	} else if tlshd != "active" {
		st.Problem = "tlshd is " + orUnknown(tlshd)
		return st
	}
	if _, ok := tlsField(output, "SDS_CONF"); !ok {
		st.Problem = "tlshd.conf does not point at the replication certificate"
		return st
	}
	if _, ok := tlsField(output, "SDS_MODULE"); !ok {
		st.Problem = "the kernel tls module is not loaded"
		return st
	}
	trust, _ := base64.StdEncoding.DecodeString(trustB64)
	if !hasTrust || strings.TrimSpace(string(trust)) != strings.TrimSpace(string(ca.CertPEM)) {
		st.Problem = "the system trust store does not hold this controller's replication CA"
		return st
	}
	cert, err := base64.StdEncoding.DecodeString(certB64)
	if err != nil {
		st.Problem = "unreadable certificate"
		return st
	}
	st.Expires, err = ca.Check(cert, node, now)
	if err != nil {
		st.Problem = "certificate not accepted: " + err.Error()
		return st
	}
	if st.Expires.Sub(now) < 30*24*time.Hour {
		st.Problem = "certificate expires " + st.Expires.Format("2006-01-02") + "; run setup again to renew it"
	}
	st.Ready = st.Problem == ""
	return st
}

// tlsTargets resolves the named nodes, or every registered node.
func (rm *ResourceManager) tlsTargets(ctx context.Context, names []string) ([]*NodeInfo, error) {
	all, err := rm.controller.nodes.ListNodes(ctx)
	if err != nil {
		return nil, err
	}
	byName := make(map[string]*NodeInfo, len(all))
	for _, n := range all {
		if n != nil {
			byName[n.Name] = n
		}
	}
	if len(names) == 0 {
		for name := range byName {
			names = append(names, name)
		}
		sort.Strings(names)
	}
	out := make([]*NodeInfo, 0, len(names))
	for _, name := range names {
		n, ok := byName[name]
		if !ok {
			return nil, fmt.Errorf("node %q is not registered", name)
		}
		out = append(out, n)
	}
	return out, nil
}

func tlsField(output, key string) (string, bool) {
	for _, line := range strings.Split(output, "\n") {
		if v, ok := strings.CutPrefix(strings.TrimSpace(line), key+"="); ok {
			return strings.TrimSpace(v), true
		}
	}
	return "", false
}

func orUnknown(s string) string {
	if s == "" {
		return "not installed"
	}
	return s
}

// hostOutput is what one host printed, or "" when it did not answer.
func hostOutput(res *deployment.ExecResult, host string) string {
	if res == nil {
		return ""
	}
	if hr, ok := res.Hosts[host]; ok && hr != nil {
		return hr.Output
	}
	return ""
}
