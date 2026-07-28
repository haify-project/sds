package controller

import (
	"io/fs"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"

	"go.uber.org/zap"
)

// ui/dist is a gitignored build artifact holding only a tracked .gitkeep on a
// fresh clone, so `go build` yields a controller with no UI assets. It must say
// so instead of answering every path with a bare "File not found" 404.
func TestUIServerServesPlaceholderWhenNotBuilt(t *testing.T) {
	s := &UIServer{logger: zap.NewNop(), distFS: fstest.MapFS{}, built: false}

	for _, path := range []string{"/", "/index.html", "/assets/app.js", "/some/spa/route"} {
		rec := httptest.NewRecorder()
		s.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))

		if rec.Code != http.StatusServiceUnavailable {
			t.Errorf("%s: got status %d, want %d", path, rec.Code, http.StatusServiceUnavailable)
		}
		if !strings.Contains(rec.Body.String(), "not built") {
			t.Errorf("%s: body should explain the UI is not built, got %q", path, rec.Body.String())
		}
		if !strings.Contains(rec.Body.String(), "make build") {
			t.Errorf("%s: body should tell the operator how to fix it", path)
		}
	}
}

// A real UI must still be served normally.
func TestUIServerServesRealUIWhenBuilt(t *testing.T) {
	s := &UIServer{
		logger: zap.NewNop(),
		built:  true,
		distFS: fstest.MapFS{
			"index.html":    &fstest.MapFile{Data: []byte("<html>real ui</html>")},
			"assets/app.js": &fstest.MapFile{Data: []byte("console.log(1)")},
		},
	}

	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "real ui") {
		t.Fatalf("index: status %d body %q", rec.Code, rec.Body.String())
	}

	rec = httptest.NewRecorder()
	s.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/assets/app.js", nil))
	if rec.Code != http.StatusOK || rec.Header().Get("Content-Type") != "application/javascript; charset=utf-8" {
		t.Fatalf("asset: status %d ct %q", rec.Code, rec.Header().Get("Content-Type"))
	}
}

// The detection itself: an embedded dist without index.html means "not built".
func TestUIBuiltDetection(t *testing.T) {
	empty := fstest.MapFS{}
	if _, err := fs.Stat(empty, "index.html"); err == nil {
		t.Fatal("empty FS must not report an index.html")
	}
	withUI := fstest.MapFS{"index.html": &fstest.MapFile{Data: []byte("x")}}
	if _, err := fs.Stat(withUI, "index.html"); err != nil {
		t.Fatalf("populated FS must report index.html: %v", err)
	}
}
