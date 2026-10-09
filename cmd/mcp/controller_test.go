package main

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/spf13/cobra"

	"github.com/haify-project/haify/pkg/client"
)

// clearTLSEnv keeps a developer's shell profile out of the test.
func clearTLSEnv(t *testing.T) {
	t.Helper()
	for _, k := range []string{"HAIFY_TLS", "HAIFY_TLS_CA", "HAIFY_TLS_CERT", "HAIFY_TLS_KEY", "HAIFY_TLS_SERVER_NAME", "HAIFY_TLS_INSECURE"} {
		t.Setenv(k, "")
	}
}

func writeTestCA(t *testing.T) string {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "haify-mcp test CA"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(time.Hour),
		IsCA:                  true,
		BasicConstraintsValid: true,
		KeyUsage:              x509.KeyUsageCertSign,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "ca.pem")
	if err := os.WriteFile(path, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func parseConn(t *testing.T, prefix string, args ...string) *controllerConn {
	t.Helper()
	var conn controllerConn
	cmd := &cobra.Command{Use: "test"}
	conn.register(cmd, prefix)
	if err := cmd.ParseFlags(args); err != nil {
		t.Fatalf("parse %v: %v", args, err)
	}
	return &conn
}

func TestStdioFlagsMatchTheCLI(t *testing.T) {
	clearTLSEnv(t)
	conn := parseConn(t, "", "--controller", "ctl:3374", "--token", "tok",
		"--tls-ca", "/ca.pem", "--tls-cert", "/c.pem", "--tls-key", "/k.pem",
		"--tls-server-name", "haify.example", "--tls-insecure")

	want := client.TLSOptions{CACert: "/ca.pem", ClientCert: "/c.pem", ClientKey: "/k.pem",
		ServerName: "haify.example", Insecure: true}
	if conn.addr != "ctl:3374" || conn.token != "tok" || conn.tls != want {
		t.Fatalf("parsed %+v", conn)
	}
	if !client.ResolveTLS(conn.tls).Active() {
		t.Fatal("TLS flags must activate TLS")
	}
}

// serve keeps --tls-cert/--tls-key for its own HTTPS listener, so the
// controller side is prefixed and both sets must coexist.
func TestServeFlagsArePrefixed(t *testing.T) {
	clearTLSEnv(t)
	conn := parseConn(t, "controller-", "--controller-tls", "--controller-tls-ca", "/ca.pem",
		"--controller-tls-server-name", "vip", "--controller-token", "tok")
	if !conn.tls.Enabled || conn.tls.CACert != "/ca.pem" || conn.tls.ServerName != "vip" || conn.token != "tok" {
		t.Fatalf("parsed %+v", conn)
	}

	f := serveCmd().Flags()
	for _, name := range []string{"tls-cert", "tls-key", "controller-tls", "controller-tls-ca",
		"controller-tls-cert", "controller-tls-key", "controller-tls-server-name", "controller-tls-insecure", "controller-token"} {
		if f.Lookup(name) == nil {
			t.Errorf("serve has no --%s", name)
		}
	}
}

func TestTLSFromEnvironment(t *testing.T) {
	clearTLSEnv(t)
	t.Setenv("HAIFY_TLS_CA", "/env-ca.pem")
	t.Setenv("HAIFY_TLS_SERVER_NAME", "env-name")

	got := client.ResolveTLS(parseConn(t, "").tls)
	if !got.Active() || got.CACert != "/env-ca.pem" || got.ServerName != "env-name" {
		t.Fatalf("resolved %+v", got)
	}
	// A flag beats the environment.
	got = client.ResolveTLS(parseConn(t, "", "--tls-ca", "/flag-ca.pem").tls)
	if got.CACert != "/flag-ca.pem" {
		t.Fatalf("flag lost to env: %+v", got)
	}
}

// dial is where the options reach the client: a CA that is not PEM can only
// fail there if the TLS credentials are actually being built.
func TestDialBuildsTLSCredentials(t *testing.T) {
	clearTLSEnv(t)

	bogus := filepath.Join(t.TempDir(), "not-a-ca.pem")
	if err := os.WriteFile(bogus, []byte("nope"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := parseConn(t, "", "--tls-ca", bogus).dial()
	if err == nil || !strings.Contains(err.Error(), "no PEM certificate") {
		t.Fatalf("flag CA not used: %v", err)
	}

	t.Setenv("HAIFY_TLS_CA", bogus)
	_, err = parseConn(t, "controller-").dial()
	if err == nil || !strings.Contains(err.Error(), "no PEM certificate") {
		t.Fatalf("env CA not used: %v", err)
	}

	t.Setenv("HAIFY_TLS_CA", "")
	c, err := parseConn(t, "", "--tls-ca", writeTestCA(t)).dial()
	if err != nil {
		t.Fatalf("valid CA: %v", err)
	}
	_ = c.Close()
}
