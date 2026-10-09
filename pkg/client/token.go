package client

import (
	"os"
	"path/filepath"
	"strings"
)

// SystemTokenPath is the host-wide API token location, written during
// cluster setup so every local tool can authenticate without flags.
const SystemTokenPath = "/etc/haify/token"

// ResolveToken returns the API token for controller requests.
// Resolution order, highest priority first:
//
//  1. explicit value (e.g. a --token flag)
//  2. HAIFY_TOKEN environment variable
//  3. ~/.haify/token (per-user)
//  4. /etc/haify/token (host-wide)
//
// An empty result means no token is sent, which only works against
// controllers with [auth] disabled.
func ResolveToken(explicit string) string {
	if explicit != "" {
		return explicit
	}
	if env := os.Getenv("HAIFY_TOKEN"); env != "" {
		return env
	}
	if home, err := os.UserHomeDir(); err == nil {
		if token := readTokenFile(filepath.Join(home, ".haify", "token")); token != "" {
			return token
		}
	}
	return readTokenFile(SystemTokenPath)
}

func readTokenFile(path string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}
