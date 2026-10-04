package controller

import (
	"crypto/tls"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"

	"github.com/haify-project/sds/pkg/config"
)

// The REST gateway carries the same bearer token as gRPC, and used to carry
// it in the clear whatever [tls] said. [tls] rest serves it over TLS.

// serveREST runs an HTTPS listener with the setup's REST config, answering
// every request with "rest".
func serveREST(t *testing.T, setup *tlsSetup) int {
	t.Helper()
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "rest")
	})}
	go func() { _ = srv.Serve(tls.NewListener(lis, setup.restServer)) }()
	t.Cleanup(func() { _ = srv.Close() })
	return lis.Addr().(*net.TCPAddr).Port
}

func TestRESTOverTLSIsOptIn(t *testing.T) {
	p := newTestPKI(t)
	setup, err := newTLSSetup(config.TLSConfig{Enabled: true, CertFile: p.certFile, KeyFile: p.keyFile})
	require.NoError(t, err)
	assert.Nil(t, setup.restServer, "REST clients written against plain HTTP keep working unless asked")
	assert.Nil(t, setup.restLoopback)

	err = config.TLSConfig{REST: true}.Validate()
	require.Error(t, err, "rest without enabled has no certificate to serve")
	assert.Contains(t, err.Error(), "tls.enabled")
}

// The web UI proxies to REST over 127.0.0.1, which the certificate does not
// name; the proxy pins the certificate instead, and still gets through.
func TestUIProxyReachesRESTOverTLS(t *testing.T) {
	p := newTestPKI(t)
	setup, err := newTLSSetup(config.TLSConfig{Enabled: true, CertFile: p.certFile, KeyFile: p.keyFile, REST: true})
	require.NoError(t, err)
	port := serveREST(t, setup)

	ui, err := NewUIServer(zap.NewNop(), "127.0.0.1", 0, port, freePort(t), setup.restLoopback)
	require.NoError(t, err)
	rec := httptest.NewRecorder()
	ui.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v1/nodes", nil))
	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "rest", rec.Body.String())

	// A plaintext proxy cannot talk to it: the gateway really is TLS.
	plain, err := NewUIServer(zap.NewNop(), "127.0.0.1", 0, port, freePort(t), nil)
	require.NoError(t, err)
	rec = httptest.NewRecorder()
	plain.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v1/nodes", nil))
	assert.NotEqual(t, "rest", rec.Body.String())
}

// A client verifying against the CA connects; one with the wrong trust does
// not. Under mutual TLS REST still asks for no client certificate: its callers
// authenticate with a bearer token, and a browser has none to present.
func TestRESTOverTLSVerifiesLikeAnyServer(t *testing.T) {
	p := newTestPKI(t)
	setup, err := newTLSSetup(config.TLSConfig{Enabled: true, CertFile: p.certFile, KeyFile: p.keyFile,
		ClientCAFile: p.caFile, REST: true})
	require.NoError(t, err)
	port := serveREST(t, setup)
	url := "https://127.0.0.1:" + strconv.Itoa(port) + "/v1/nodes"

	trusting := &http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{
		RootCAs: p.rootPool(t), ServerName: "sds-controller.test", MinVersion: tls.VersionTLS12}}}
	resp, err := trusting.Get(url)
	require.NoError(t, err)
	_ = resp.Body.Close()
	assert.Equal(t, http.StatusOK, resp.StatusCode)

	other := newTestPKI(t)
	untrusting := &http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{
		RootCAs: other.rootPool(t), ServerName: "sds-controller.test", MinVersion: tls.VersionTLS12}}}
	_, err = untrusting.Get(url)
	require.Error(t, err)
}
