package client

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"

	sdspb "github.com/haify-project/sds/api/proto/v1"
)

// clientTestPKI is a throwaway CA and the leaves it signs, generated per test
// rather than checked in.
type clientTestPKI struct {
	dir      string
	caPEM    []byte
	caCert   *x509.Certificate
	caKey    ed25519.PrivateKey
	caFile   string
	server   tls.Certificate
	certFile string
	keyFile  string
}

func newClientTestPKI(t *testing.T) *clientTestPKI {
	t.Helper()
	dir := t.TempDir()

	caPub, caKey, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	now := time.Now().Add(-time.Minute)
	caTmpl := &x509.Certificate{
		SerialNumber:          clientTestSerial(t),
		Subject:               pkix.Name{CommonName: "sdsent-test-ca"},
		NotBefore:             now,
		NotAfter:              now.Add(time.Hour),
		IsCA:                  true,
		KeyUsage:              x509.KeyUsageCertSign,
		BasicConstraintsValid: true,
	}
	caDER, err := x509.CreateCertificate(rand.Reader, caTmpl, caTmpl, caPub, caKey)
	require.NoError(t, err)
	caCert, err := x509.ParseCertificate(caDER)
	require.NoError(t, err)

	p := &clientTestPKI{
		dir:    dir,
		caPEM:  pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: caDER}),
		caCert: caCert,
		caKey:  caKey,
	}
	p.caFile = filepath.Join(dir, "ca.crt")
	require.NoError(t, os.WriteFile(p.caFile, p.caPEM, 0o600))

	certPEM, keyPEM := p.issue(t, "sds-controller.test", []string{"sds-controller.test"}, x509.ExtKeyUsageServerAuth)
	p.server, err = tls.X509KeyPair(certPEM, keyPEM)
	require.NoError(t, err)

	clientCertPEM, clientKeyPEM := p.issue(t, "sds", nil, x509.ExtKeyUsageClientAuth)
	p.certFile = filepath.Join(dir, "client.crt")
	p.keyFile = filepath.Join(dir, "client.key")
	require.NoError(t, os.WriteFile(p.certFile, clientCertPEM, 0o600))
	require.NoError(t, os.WriteFile(p.keyFile, clientKeyPEM, 0o600))
	return p
}

func (p *clientTestPKI) issue(t *testing.T, cn string, dnsNames []string, eku x509.ExtKeyUsage) (certPEM, keyPEM []byte) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	now := time.Now().Add(-time.Minute)
	tmpl := &x509.Certificate{
		SerialNumber: clientTestSerial(t),
		Subject:      pkix.Name{CommonName: cn},
		NotBefore:    now,
		NotAfter:     now.Add(time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{eku},
		DNSNames:     dnsNames,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, p.caCert, pub, p.caKey)
	require.NoError(t, err)
	keyDER, err := x509.MarshalPKCS8PrivateKey(priv)
	require.NoError(t, err)
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}),
		pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER})
}

func clientTestSerial(t *testing.T) *big.Int {
	t.Helper()
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	require.NoError(t, err)
	return serial
}

// startTLSControllerStub serves the real gRPC service over TLS, optionally
// requiring a client certificate.
func startTLSControllerStub(t *testing.T, p *clientTestPKI, requireClientCert bool) string {
	t.Helper()
	conf := &tls.Config{
		Certificates: []tls.Certificate{p.server},
		MinVersion:   tls.VersionTLS12,
	}
	if requireClientCert {
		pool := x509.NewCertPool()
		require.True(t, pool.AppendCertsFromPEM(p.caPEM))
		conf.ClientCAs = pool
		conf.ClientAuth = tls.RequireAndVerifyClientCert
	}

	lis, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	srv := grpc.NewServer(grpc.Creds(credentials.NewTLS(conf)))
	sdspb.RegisterSDSControllerServer(srv, &sdspb.UnimplementedSDSControllerServer{})
	go func() { _ = srv.Serve(lis) }()
	t.Cleanup(srv.Stop)
	return lis.Addr().String()
}

// reach makes one RPC and reports the transport-level outcome. A TLS failure
// never gets as far as the handler, so an Unimplemented reply means the
// handshake succeeded.
func reach(t *testing.T, c *SDSClient) error {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_, err := c.client.ListPools(ctx, &sdspb.ListPoolsRequest{})
	return err
}

// Without TLS options the client stays plaintext, exactly as every existing
// deployment expects.
func TestClientStaysPlaintextByDefault(t *testing.T) {
	assert.False(t, TLSOptions{}.Active())

	pki := newClientTestPKI(t)
	addr := startTLSControllerStub(t, pki, false)

	c, err := NewSDSClient(addr)
	require.NoError(t, err)
	defer func() { _ = c.Close() }()

	require.Error(t, reach(t, c), "a plaintext client must not reach a TLS controller")
}

// The CA the controller's certificate chains to is all a client needs.
func TestClientConnectsWithTheCA(t *testing.T) {
	pki := newClientTestPKI(t)
	addr := startTLSControllerStub(t, pki, false)

	c, err := NewSDSClient(addr, WithTLS(TLSOptions{
		CACert:     pki.caFile,
		ServerName: "sds-controller.test",
	}))
	require.NoError(t, err)
	defer func() { _ = c.Close() }()

	err = reach(t, c)
	require.Error(t, err, "the stub implements nothing")
	assert.Contains(t, err.Error(), "Unimplemented",
		"anything else means the handshake failed rather than the handler: %v", err)
}

// Trusting the wrong CA must fail; TLS without verification is not TLS.
func TestClientRejectsAnUntrustedController(t *testing.T) {
	pki := newClientTestPKI(t)
	other := newClientTestPKI(t)
	addr := startTLSControllerStub(t, pki, false)

	c, err := NewSDSClient(addr, WithTLS(TLSOptions{
		CACert:     other.caFile,
		ServerName: "sds-controller.test",
	}))
	require.NoError(t, err)
	defer func() { _ = c.Close() }()

	err = reach(t, c)
	require.Error(t, err)
	assert.NotContains(t, err.Error(), "Unimplemented")
}

// Mutual TLS: the controller refuses a client with no certificate and accepts
// one whose certificate its CA signed.
func TestClientMutualTLS(t *testing.T) {
	pki := newClientTestPKI(t)
	addr := startTLSControllerStub(t, pki, true)

	withoutCert, err := NewSDSClient(addr, WithTLS(TLSOptions{
		CACert:     pki.caFile,
		ServerName: "sds-controller.test",
	}))
	require.NoError(t, err)
	defer func() { _ = withoutCert.Close() }()
	err = reach(t, withoutCert)
	require.Error(t, err)
	assert.NotContains(t, err.Error(), "Unimplemented",
		"a controller requiring client certificates must not serve a client without one")

	withCert, err := NewSDSClient(addr, WithTLS(TLSOptions{
		CACert:     pki.caFile,
		ClientCert: pki.certFile,
		ClientKey:  pki.keyFile,
		ServerName: "sds-controller.test",
	}))
	require.NoError(t, err)
	defer func() { _ = withCert.Close() }()
	err = reach(t, withCert)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "Unimplemented", "the client certificate should have been accepted: %v", err)
}

// Half a key pair is a configuration error, not a silent fall back to
// server-only TLS — which would fail later, at the handshake, looking like the
// controller's fault.
func TestClientCredentialsRejectHalfAKeyPair(t *testing.T) {
	pki := newClientTestPKI(t)

	_, err := TLSOptions{ClientCert: pki.certFile}.Credentials()
	require.ErrorContains(t, err, "must be given together")

	_, err = TLSOptions{ClientKey: pki.keyFile}.Credentials()
	require.ErrorContains(t, err, "must be given together")

	_, err = TLSOptions{CACert: filepath.Join(pki.dir, "absent.crt")}.Credentials()
	require.ErrorContains(t, err, "read TLS CA")

	garbage := filepath.Join(pki.dir, "garbage.pem")
	require.NoError(t, os.WriteFile(garbage, []byte("not a certificate\n"), 0o600))
	_, err = TLSOptions{CACert: garbage}.Credentials()
	require.ErrorContains(t, err, "no PEM certificate")
}

// A bad path must surface when the client is built, not on the first call.
func TestNewSDSClientFailsOnUnusableTLSMaterial(t *testing.T) {
	_, err := NewSDSClient("127.0.0.1:34871", WithTLS(TLSOptions{CACert: "/nonexistent/ca.crt"}))
	require.ErrorContains(t, err, "read TLS CA")
}

// Any TLS field implies TLS, so `--tls-ca ...` alone cannot leave the caller
// talking plaintext to a TLS controller.
func TestTLSOptionsActive(t *testing.T) {
	assert.False(t, TLSOptions{}.Active())
	assert.True(t, TLSOptions{Enabled: true}.Active())
	assert.True(t, TLSOptions{CACert: "ca.crt"}.Active())
	assert.True(t, TLSOptions{ClientCert: "c.crt"}.Active())
	assert.True(t, TLSOptions{ClientKey: "c.key"}.Active())
	assert.True(t, TLSOptions{Insecure: true}.Active())
}

func TestResolveTLSPrefersExplicitOverEnvironment(t *testing.T) {
	t.Setenv(envTLSCACert, "/env/ca.crt")
	t.Setenv(envTLSClientCert, "/env/client.crt")
	t.Setenv(envTLSClientKey, "/env/client.key")
	t.Setenv(envTLSServerName, "env.example")
	t.Setenv(envTLS, "true")

	got := ResolveTLS(TLSOptions{CACert: "/flag/ca.crt"})
	assert.Equal(t, "/flag/ca.crt", got.CACert)
	assert.Equal(t, "/env/client.crt", got.ClientCert)
	assert.Equal(t, "/env/client.key", got.ClientKey)
	assert.Equal(t, "env.example", got.ServerName)
	assert.True(t, got.Enabled)
}

// SDS_TLS=no must not read as "any non-empty value means on".
func TestResolveTLSIgnoresANegativeEnvironmentValue(t *testing.T) {
	t.Setenv(envTLS, "no")
	t.Setenv(envTLSInsecure, "banana")

	got := ResolveTLS(TLSOptions{})
	assert.False(t, got.Enabled)
	assert.False(t, got.Insecure)
	assert.False(t, got.Active())
}
