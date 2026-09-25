package controller

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

// The UI must serve its API under its own origin.
//
// It used to build absolute URLs to :3375 and :7634 from
// window.location.hostname, which works only when the browser can reach the
// node directly. Behind anything terminating TLS on 443 — a tunnel, a VPS, a
// reverse proxy — every call went to https://<public-name>:3375, a port that is
// not published and should not be. The page loaded and nothing on it worked,
// which is a worse failure than not loading at all.
func TestUIServerProxiesAPIOnItsOwnOrigin(t *testing.T) {
	// Free ports: nothing may answer, or a controller running on this machine
	// would serve the request and the proxy would look like a file server.
	srv, err := NewUIServer(zap.NewNop(), "127.0.0.1", 0, freePort(t), freePort(t))
	require.NoError(t, err)

	for _, path := range []string{"/v1/nodes", "/v1/rbac/whoami", "/ai/chat/stream"} {
		rec := httptest.NewRecorder()
		srv.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))

		// The backends are not running in a unit test, so the proxy reports a
		// gateway error. That is the point: the request was proxied rather than
		// answered with the SPA's index.html, which is what a same-origin API
		// call must never receive.
		assert.NotEqual(t, http.StatusOK, rec.Code, "%s should be proxied, not served as a file", path)
		assert.NotContains(t, rec.Body.String(), "<!DOCTYPE html>",
			"%s returned the SPA shell; the browser would try to parse HTML as JSON", path)
	}
}

// Everything that is not an API path still reaches the SPA, including deep
// links, which the router resolves client-side.
func TestUIServerStillServesTheSPA(t *testing.T) {
	srv, err := NewUIServer(zap.NewNop(), "127.0.0.1", 0, 3375, 7634)
	require.NoError(t, err)

	for _, path := range []string{"/", "/resources", "/nodes"} {
		rec := httptest.NewRecorder()
		srv.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		assert.NotEqual(t, http.StatusBadGateway, rec.Code, "%s must not be proxied to the API", path)
	}
}
