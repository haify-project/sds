package controller

import (
	"bytes"
	"compress/gzip"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

// bigJS is large and repetitive, like a real bundle: it must compress well
// enough that serving it raw is visibly the wrong choice.
var bigJS = []byte(strings.Repeat("export function thing(){return 42}\n", 4000))

func testUIServer() *UIServer {
	return &UIServer{
		logger: zap.NewNop(),
		built:  true,
		gz:     make(map[string][]byte),
		distFS: fstest.MapFS{
			"index.html":                {Data: []byte("<!DOCTYPE html><html><body>SPA</body></html>")},
			"static/js/index.abc123.js": {Data: bigJS},
			"static/img/logo.deadbe.png": {Data: []byte(
				"\x89PNG\r\n\x1a\n" + strings.Repeat("\x00", 200))},
		},
	}
}

func get(srv *UIServer, path, acceptEncoding string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, path, nil)
	if acceptEncoding != "" {
		req.Header.Set("Accept-Encoding", acceptEncoding)
	}
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	return rec
}

// The bundle is over a megabyte of JS and CSS. Uncompressed that is a minute
// of staring at a blank page from the other side of an ocean, which is how
// this was found.
func TestUIServerGzipsTextAssets(t *testing.T) {
	srv := testUIServer()

	rec := get(srv, "/static/js/index.abc123.js", "gzip, deflate, br")
	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, "gzip", rec.Header().Get("Content-Encoding"))

	zr, err := gzip.NewReader(bytes.NewReader(rec.Body.Bytes()))
	require.NoError(t, err)
	got, err := io.ReadAll(zr)
	require.NoError(t, err)
	assert.Equal(t, bigJS, got, "the decompressed body must be the original file")

	assert.Less(t, rec.Body.Len(), len(bigJS)/4, "gzip should be a large win on JS")
	assert.Equal(t, strconv.Itoa(rec.Body.Len()), rec.Header().Get("Content-Length"),
		"Content-Length must describe the encoded bytes, not the original")
	assert.Equal(t, "Accept-Encoding", rec.Header().Get("Vary"))
}

// A client that did not ask for gzip still gets a correct, complete response.
func TestUIServerServesRawWhenGzipNotAccepted(t *testing.T) {
	srv := testUIServer()

	rec := get(srv, "/static/js/index.abc123.js", "")
	require.Equal(t, http.StatusOK, rec.Code)
	assert.Empty(t, rec.Header().Get("Content-Encoding"))
	assert.Equal(t, bigJS, rec.Body.Bytes())
	// Vary must be set here too, or a shared cache would hand these raw bytes
	// to clients that could have had the compressed ones.
	assert.Equal(t, "Accept-Encoding", rec.Header().Get("Vary"))
}

// PNGs are already compressed; re-encoding them burns CPU on a storage node to
// save nothing, and can make the response larger.
func TestUIServerDoesNotGzipImages(t *testing.T) {
	srv := testUIServer()

	rec := get(srv, "/static/img/logo.deadbe.png", "gzip")
	require.Equal(t, http.StatusOK, rec.Code)
	assert.Empty(t, rec.Header().Get("Content-Encoding"))
}

// Repeated requests must keep returning a valid encoding — the memo must not
// hand back a reused or half-written buffer.
func TestUIServerGzipCacheIsStable(t *testing.T) {
	srv := testUIServer()

	first := get(srv, "/static/js/index.abc123.js", "gzip").Body.Bytes()
	second := get(srv, "/static/js/index.abc123.js", "gzip").Body.Bytes()
	assert.Equal(t, first, second)

	zr, err := gzip.NewReader(bytes.NewReader(second))
	require.NoError(t, err)
	got, err := io.ReadAll(zr)
	require.NoError(t, err)
	assert.Equal(t, bigJS, got)
}

// Hashed assets may be cached forever; index.html may not. Caching index.html
// would pin a browser to a bundle whose hashed files no longer exist after an
// upgrade — a permanently broken page that a reload cannot fix.
func TestUIServerCacheHeaders(t *testing.T) {
	srv := testUIServer()

	rec := get(srv, "/static/js/index.abc123.js", "gzip")
	assert.Equal(t, "public, max-age=31536000, immutable", rec.Header().Get("Cache-Control"))

	for _, path := range []string{
		"/",
		"/resources",            // SPA deep link: served as index.html
		"/static/js/missing.js", // unknown asset: also falls back to index.html
	} {
		rec := get(srv, path, "gzip")
		assert.Equal(t, "no-cache, no-store, must-revalidate", rec.Header().Get("Cache-Control"),
			"%s is served as index.html and must never be cached", path)
	}
}
