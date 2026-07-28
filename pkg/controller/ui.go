package controller

import (
	"context"
	"fmt"
	"io/fs"
	"net"
	"net/http"
	"strings"

	"github.com/liliang-cn/sds/ui"
	"go.uber.org/zap"
)

// uiNotBuiltPage is served when the binary was built without a real web UI.
// ui/dist is a build artifact: on a fresh checkout it holds only the tracked
// .gitkeep, so a plain `go build` produces a controller with no UI assets. Say
// so plainly instead of answering every request with a bare 404.
const uiNotBuiltPage = `<!DOCTYPE html>
<html><head><title>SDS — UI not built</title></head>
<body style="font-family:sans-serif;max-width:40em;margin:4em auto">
<h1>Web UI not built</h1>
<p>This <code>sds-controller</code> was compiled without the web UI assets.
The API and <code>sds-cli</code> are unaffected.</p>
<p>To include the UI, build with <code>make build</code> (which compiles
<code>web-ui/</code> and embeds it), then restart the controller.</p>
</body></html>
`

// UIServer serves the embedded web UI
type UIServer struct {
	logger *zap.Logger
	server *http.Server
	distFS fs.FS
	// built is false when the embedded dist holds no index.html, i.e. the
	// binary carries the placeholder rather than a real UI.
	built bool
}

// NewUIServer creates a new UI server
func NewUIServer(logger *zap.Logger, listenAddress string, port int) (*UIServer, error) {
	// Get the subdirectory from the embed
	distFS, err := fs.Sub(ui.FS, "dist")
	if err != nil {
		return nil, fmt.Errorf("failed to get UI filesystem: %w", err)
	}

	built := true
	if _, err := fs.Stat(distFS, "index.html"); err != nil {
		built = false
		logger.Warn("Web UI assets are not embedded in this binary; serving a placeholder page. Build with `make build` to include the UI.")
	}

	uiServer := &UIServer{
		logger: logger,
		distFS: distFS,
		built:  built,
	}

	uiAddr := fmt.Sprintf("%s:%d", listenAddress, port)
	uiServer.server = &http.Server{
		Addr:    uiAddr,
		Handler: uiServer,
	}

	return uiServer, nil
}

func (s *UIServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// Add CORS headers
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, OPTIONS")
	w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization")

	if r.Method == "OPTIONS" {
		w.WriteHeader(http.StatusNoContent)
		return
	}

	// No caching
	w.Header().Set("Cache-Control", "no-cache, no-store, must-revalidate")

	// Nothing to serve: explain why rather than 404 on every path.
	if !s.built {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte(uiNotBuiltPage))
		return
	}

	// Remove leading slash from path for filesystem lookup
	reqPath := strings.TrimPrefix(r.URL.Path, "/")

	// Determine which file to serve
	var fileToServe string
	if reqPath == "" || reqPath == "/" {
		fileToServe = "index.html"
	} else {
		// Try to open the requested file
		f, err := s.distFS.Open(reqPath)
		if err != nil {
			// File not found, serve index.html for SPA routing
			fileToServe = "index.html"
		} else {
			f.Close()
			stat, _ := f.Stat()
			if stat.IsDir() {
				// For directories, try to serve index.html inside
				fileToServe = reqPath + "/index.html"
				// Check if that exists
				if _, err := s.distFS.Open(fileToServe); err != nil {
					fileToServe = "index.html"
				}
			} else {
				fileToServe = reqPath
			}
		}
	}

	// Read and serve the file
	data, err := fs.ReadFile(s.distFS, fileToServe)
	if err != nil {
		http.Error(w, "File not found", http.StatusNotFound)
		return
	}

	// Set content type based on file extension
	contentType := "text/html; charset=utf-8"
	if strings.HasSuffix(fileToServe, ".js") {
		contentType = "application/javascript; charset=utf-8"
	} else if strings.HasSuffix(fileToServe, ".css") {
		contentType = "text/css; charset=utf-8"
	} else if strings.HasSuffix(fileToServe, ".json") {
		contentType = "application/json; charset=utf-8"
	} else if strings.HasSuffix(fileToServe, ".png") {
		contentType = "image/png"
	} else if strings.HasSuffix(fileToServe, ".jpg") || strings.HasSuffix(fileToServe, ".jpeg") {
		contentType = "image/jpeg"
	} else if strings.HasSuffix(fileToServe, ".svg") {
		contentType = "image/svg+xml"
	}
	w.Header().Set("Content-Type", contentType)
	_, _ = w.Write(data)
}

// Start starts the UI server in a goroutine
func (s *UIServer) Start() error {
	listener, err := net.Listen("tcp", s.server.Addr)
	if err != nil {
		return err
	}

	s.logger.Info("UI server listening", zap.String("address", s.server.Addr))
	go func() {
		if err := s.server.Serve(listener); err != nil && err != http.ErrServerClosed {
			s.logger.Error("UI server error", zap.Error(err))
		}
	}()

	return nil
}

// Shutdown stops the UI server
func (s *UIServer) Shutdown(ctx context.Context) error {
	s.logger.Info("Stopping UI server")
	return s.server.Shutdown(ctx)
}
