// Package ui carries the web UI as an embedded filesystem.
//
// ui/dist holds the built UI and is a gitignored build artifact, so on a fresh
// clone the only thing in it is the tracked .gitkeep. The `all:` prefix is what
// makes that work: without it, go:embed skips dotfiles, finds the directory
// empty, and fails the build with "contains no embeddable files" — which used
// to break a plain `go build ./...` / `go test ./...` on any checkout where the
// UI had not been built yet.
//
// Run `make build` (or `make ui-ensure`) to populate dist with the real UI;
// the controller serves a "UI not built" page when it is still just the
// placeholder.
package ui

import "embed"

//go:embed all:dist
var FS embed.FS
