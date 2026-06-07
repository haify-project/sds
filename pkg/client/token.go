package client

import (
	"os"
	"path/filepath"
	"strings"
)

// SystemTokenPath is the host-wide API token location, written during
// cluster setup so every local tool can authenticate without flags.
const SystemTokenPath = "/etc/sds/token"

// ResolveToken returns the API token for controller requests.
// Resolution order, highest priority first:
//
//  1. explicit value (e.g. a --token flag)
//  2. SDS_TOKEN environment variable
//  3. ~/.sds/token (per-user)
//  4. /etc/sds/token (host-wide)
//
// An empty result means no token is sent, which only works against
// controllers with [auth] disabled.
func ResolveToken(explicit string) string {
	if explicit != "" {
		return explicit
	}
	if env := os.Getenv("SDS_TOKEN"); env != "" {
		return env
	}
	if home, err := os.UserHomeDir(); err == nil {
		if token := readTokenFile(filepath.Join(home, ".sds", "token")); token != "" {
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
