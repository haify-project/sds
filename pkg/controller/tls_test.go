package controller

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/grpc-ecosystem/grpc-gateway/v2/runtime"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"

	sdspb "github.com/liliang-cn/sds/api/proto/v1"
	"github.com/liliang-cn/sds/pkg/config"
)

// testPKI is a throwaway CA plus the leaves signed by it, generated per test.
// No certificate or key is checked into the repository: a key in git is a key
// that eventually ends up in a deployment.
type testPKI struct {
	dir      string
	caPEM    []byte
	caCert   *x509.Certificate
	caKey    ed25519.PrivateKey
	caFile   string
	certFile string
	keyFile  string
}

func newTestPKI(t *testing.T) *testPKI {
	t.Helper()
	dir := t.TempDir()

	caPub, caKey, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	now := time.Now().Add(-time.Minute)
	caTmpl := &x509.Certificate{
		SerialNumber:          testSerial(t),
		Subject:               pkix.Name{CommonName: "sds-test-ca"},
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

	p := &testPKI{
		dir:    dir,
		caPEM:  pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: caDER}),
		caCert: caCert,
		caKey:  caKey,
	}
	p.caFile = filepath.Join(dir, "ca.crt")
	require.NoError(t, os.WriteFile(p.caFile, p.caPEM, 0o600))

	// The server leaf deliberately names only "sds-controller.test" — NOT
	// 127.0.0.1. A certificate issued for the cluster's service name is the
	// normal case, and it is what breaks a loopback hop that verifies by name.
	certPEM, keyPEM := p.issue(t, "sds-controller.test", []string{"sds-controller.test"}, x509.ExtKeyUsageServerAuth)
	p.certFile = filepath.Join(dir, "server.crt")
	p.keyFile = filepath.Join(dir, "server.key")
	require.NoError(t, os.WriteFile(p.certFile, certPEM, 0o600))
	require.NoError(t, os.WriteFile(p.keyFile, keyPEM, 0o600))
	return p
}

// issue signs a new leaf with the test CA.
func (p *testPKI) issue(t *testing.T, cn string, dnsNames []string, eku x509.ExtKeyUsage) (certPEM, keyPEM []byte) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	now := time.Now().Add(-time.Minute)
	tmpl := &x509.Certificate{
		SerialNumber: testSerial(t),
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

// clientCert writes a client certificate signed by the test CA and returns the
// tls.Certificate ready to present.
func (p *testPKI) clientCert(t *testing.T) tls.Certificate {
	t.Helper()
	certPEM, keyPEM := p.issue(t, "sds-cli-test", nil, x509.ExtKeyUsageClientAuth)
	cert, err := tls.X509KeyPair(certPEM, keyPEM)
	require.NoError(t, err)
	return cert
}

func (p *testPKI) rootPool(t *testing.T) *x509.CertPool {
	t.Helper()
	pool := x509.NewCertPool()
	require.True(t, pool.AppendCertsFromPEM(p.caPEM))
	return pool
}

func testSerial(t *testing.T) *big.Int {
	t.Helper()
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	require.NoError(t, err)
	return serial
}

// startTLSServer runs a real gRPC server with the credentials newTLSSetup
// produced, and returns the setup and the dialable address.
func startTLSServer(t *testing.T, cfg config.TLSConfig) (*tlsSetup, string) {
	t.Helper()
	setup, err := newTLSSetup(cfg)
	require.NoError(t, err)
	require.NotNil(t, setup)

	lis, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)

	srv := grpc.NewServer(grpc.Creds(setup.serverCreds))
	sdspb.RegisterSDSControllerServer(srv, NewServer(&Controller{logger: zap.NewNop()}))
	go func() { _ = srv.Serve(lis) }()
	t.Cleanup(srv.Stop)

	return setup, lis.Addr().String()
}

// callAPI issues one real RPC over the given credentials and returns the
// error, which is what distinguishes "handshake refused" from "connected".
func callAPI(t *testing.T, addr string, creds credentials.TransportCredentials) error {
	t.Helper()
	conn, err := grpc.NewClient(addr, grpc.WithTransportCredentials(creds), grpc.WithNoProxy())
	require.NoError(t, err)
	defer func() { _ = conn.Close() }()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_, err = sdspb.NewSDSControllerClient(conn).ListControllerLogs(ctx, &sdspb.ListControllerLogsRequest{})
	return err
}

// A disabled section must produce no credentials at all, so every caller stays
// on the plaintext path it has always used.
func TestNewTLSSetupDisabledIsNil(t *testing.T) {
	setup, err := newTLSSetup(config.TLSConfig{Enabled: false})
	require.NoError(t, err)
	assert.Nil(t, setup)
}

// The regression this whole change exists for: with [tls] enabled the listener
// must actually be TLS. A plaintext client used to be served happily.
func TestTLSServerRejectsPlaintextClients(t *testing.T) {
	pki := newTestPKI(t)
	_, addr := startTLSServer(t, config.TLSConfig{
		Enabled: true, CertFile: pki.certFile, KeyFile: pki.keyFile,
	})

	err := callAPI(t, addr, insecure.NewCredentials())
	require.Error(t, err, "a plaintext client must not reach a TLS listener")
}

// The other half: a client that trusts the right CA connects.
func TestTLSServerAcceptsClientsTrustingTheCA(t *testing.T) {
	pki := newTestPKI(t)
	_, addr := startTLSServer(t, config.TLSConfig{
		Enabled: true, CertFile: pki.certFile, KeyFile: pki.keyFile,
	})

	creds := credentials.NewTLS(&tls.Config{
		RootCAs:    pki.rootPool(t),
		ServerName: "sds-controller.test",
		MinVersion: tls.VersionTLS12,
	})
	require.NoError(t, callAPI(t, addr, creds))
}

// A client trusting an unrelated CA must be rejected — otherwise "TLS enabled"
// would mean encryption without authentication.
func TestTLSServerRejectsAnUntrustedCA(t *testing.T) {
	pki := newTestPKI(t)
	other := newTestPKI(t)
	_, addr := startTLSServer(t, config.TLSConfig{
		Enabled: true, CertFile: pki.certFile, KeyFile: pki.keyFile,
	})

	creds := credentials.NewTLS(&tls.Config{
		RootCAs:    other.rootPool(t),
		ServerName: "sds-controller.test",
		MinVersion: tls.VersionTLS12,
	})
	require.Error(t, callAPI(t, addr, creds))
}

// Mutual TLS: configuring a client CA must actually enforce client
// certificates, not merely record one.
func TestMutualTLSRejectsAClientWithoutACertificate(t *testing.T) {
	pki := newTestPKI(t)
	setup, addr := startTLSServer(t, config.TLSConfig{
		Enabled: true, CertFile: pki.certFile, KeyFile: pki.keyFile, ClientCAFile: pki.caFile,
	})
	assert.True(t, setup.mutual)

	creds := credentials.NewTLS(&tls.Config{
		RootCAs:    pki.rootPool(t),
		ServerName: "sds-controller.test",
		MinVersion: tls.VersionTLS12,
	})
	require.Error(t, callAPI(t, addr, creds), "mutual TLS must refuse a client with no certificate")
}

func TestMutualTLSAcceptsAClientWithACertificate(t *testing.T) {
	pki := newTestPKI(t)
	_, addr := startTLSServer(t, config.TLSConfig{
		Enabled: true, CertFile: pki.certFile, KeyFile: pki.keyFile, ClientCAFile: pki.caFile,
	})

	creds := credentials.NewTLS(&tls.Config{
		RootCAs:      pki.rootPool(t),
		Certificates: []tls.Certificate{pki.clientCert(t)},
		ServerName:   "sds-controller.test",
		MinVersion:   tls.VersionTLS12,
	})
	require.NoError(t, callAPI(t, addr, creds))
}

// A client certificate from an unrelated CA must not pass.
func TestMutualTLSRejectsAForeignClientCertificate(t *testing.T) {
	pki := newTestPKI(t)
	other := newTestPKI(t)
	_, addr := startTLSServer(t, config.TLSConfig{
		Enabled: true, CertFile: pki.certFile, KeyFile: pki.keyFile, ClientCAFile: pki.caFile,
	})

	creds := credentials.NewTLS(&tls.Config{
		RootCAs:      pki.rootPool(t),
		Certificates: []tls.Certificate{other.clientCert(t)},
		ServerName:   "sds-controller.test",
		MinVersion:   tls.VersionTLS12,
	})
	require.Error(t, callAPI(t, addr, creds))
}

// The most regression-prone path in this change. Once the gRPC server speaks
// TLS, the in-process REST gateway keeps dialling 127.0.0.1 — an address the
// certificate above deliberately does not name. If that hop is left plaintext,
// or verified by name, gRPC keeps working perfectly while every REST call and
// the entire web UI die at the handshake, which is exactly the kind of
// half-broken state nobody notices in a smoke test.
func TestRESTLoopbackWorksWithTLS(t *testing.T) {
	for _, mutual := range []bool{false, true} {
		name := "server_tls"
		if mutual {
			name = "mutual_tls"
		}
		t.Run(name, func(t *testing.T) {
			pki := newTestPKI(t)
			cfg := config.TLSConfig{Enabled: true, CertFile: pki.certFile, KeyFile: pki.keyFile}
			if mutual {
				cfg.ClientCAFile = pki.caFile
			}
			setup, addr := startTLSServer(t, cfg)

			mux := runtime.NewServeMux()
			require.NoError(t, sdspb.RegisterSDSControllerHandlerFromEndpoint(
				context.Background(), mux, addr, loopbackDialOptions(setup)))

			rec := httptest.NewRecorder()
			done := make(chan struct{})
			go func() {
				defer close(done)
				mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v1/logs", nil))
			}()
			select {
			case <-done:
			case <-time.After(20 * time.Second):
				t.Fatal("REST gateway request hung — the loopback TLS handshake never completed")
			}
			assert.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
		})
	}
}

// The loopback hop must not degrade into "accept anything": it pins the exact
// leaf, so a different server behind the same address is refused even when
// that server's certificate is signed by the very same CA.
func TestLoopbackCredentialsPinTheExactCertificate(t *testing.T) {
	pki := newTestPKI(t)
	setup, err := newTLSSetup(config.TLSConfig{
		Enabled: true, CertFile: pki.certFile, KeyFile: pki.keyFile,
	})
	require.NoError(t, err)

	// An impostor with a different leaf from the same CA.
	impostorPEM, impostorKeyPEM := pki.issue(t, "impostor.test", []string{"impostor.test"}, x509.ExtKeyUsageServerAuth)
	impostor, err := tls.X509KeyPair(impostorPEM, impostorKeyPEM)
	require.NoError(t, err)

	lis, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	srv := grpc.NewServer(grpc.Creds(credentials.NewTLS(&tls.Config{
		Certificates: []tls.Certificate{impostor},
		MinVersion:   tls.VersionTLS12,
	})))
	sdspb.RegisterSDSControllerServer(srv, NewServer(&Controller{logger: zap.NewNop()}))
	go func() { _ = srv.Serve(lis) }()
	defer srv.Stop()

	err = callAPI(t, lis.Addr().String(), setup.loopbackCreds)
	require.Error(t, err, "the loopback hop must refuse a certificate that is not the configured one")
}

// A nil setup keeps the historic plaintext dial options, so a controller
// without [tls] behaves exactly as before.
func TestLoopbackDialOptionsStayPlaintextWithoutTLS(t *testing.T) {
	pki := newTestPKI(t)
	_, addr := startTLSServer(t, config.TLSConfig{
		Enabled: true, CertFile: pki.certFile, KeyFile: pki.keyFile,
	})

	mux := runtime.NewServeMux()
	require.NoError(t, sdspb.RegisterSDSControllerHandlerFromEndpoint(
		context.Background(), mux, addr, loopbackDialOptions(nil)))

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v1/logs", nil))
	assert.NotEqual(t, http.StatusOK, rec.Code,
		"a plaintext loopback against a TLS server must fail; if this passes, the server was not really TLS")
}

// newTLSSetup reads the files itself, so a certificate that disappeared or was
// swapped for a mismatched key after config load is still an error rather than
// a server that starts and fails every handshake.
func TestNewTLSSetupRejectsUnusableMaterial(t *testing.T) {
	pki := newTestPKI(t)
	other := newTestPKI(t)

	_, err := newTLSSetup(config.TLSConfig{
		Enabled: true, CertFile: filepath.Join(pki.dir, "gone.crt"), KeyFile: pki.keyFile,
	})
	require.Error(t, err)

	_, err = newTLSSetup(config.TLSConfig{
		Enabled: true, CertFile: pki.certFile, KeyFile: other.keyFile,
	})
	require.Error(t, err, "a key belonging to another certificate must not load")

	empty := filepath.Join(pki.dir, "empty-ca.pem")
	require.NoError(t, os.WriteFile(empty, []byte("nothing here\n"), 0o600))
	_, err = newTLSSetup(config.TLSConfig{
		Enabled: true, CertFile: pki.certFile, KeyFile: pki.keyFile, ClientCAFile: empty,
	})
	require.ErrorContains(t, err, "no PEM certificate",
		"an empty ClientCAs pool would reject every client instead of failing to start")
}

// The ephemeral loopback identity is minted fresh per process and never
// touches disk, so it cannot be lifted from the host and replayed.
func TestLoopbackIdentityIsEphemeral(t *testing.T) {
	pki := newTestPKI(t)
	cfg := config.TLSConfig{
		Enabled: true, CertFile: pki.certFile, KeyFile: pki.keyFile, ClientCAFile: pki.caFile,
	}
	first, err := newTLSSetup(cfg)
	require.NoError(t, err)
	second, err := newTLSSetup(cfg)
	require.NoError(t, err)

	// Different objects with independently generated keys; the only shared
	// state is the operator's own files.
	require.NotSame(t, first, second)

	entries, err := os.ReadDir(pki.dir)
	require.NoError(t, err)
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	assert.ElementsMatch(t, []string{"ca.crt", "server.crt", "server.key"}, names,
		fmt.Sprintf("the loopback identity must not be written to disk, found %v", names))
}
