package main

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/liliang-cn/sds/pkg/client"
)

// API token resolution, highest priority first:
//
//  1. --token flag
//  2. SDS_TOKEN environment variable
//  3. ~/.sds/token (per-user)
//  4. /etc/sds/token (host-wide, e.g. written during cluster setup)
//
// An empty result means no token is sent, which only works against
// controllers with [auth] disabled.
const systemTokenPath = "/etc/sds/token"

func resolveToken() string {
	if tokenFlag != "" {
		return tokenFlag
	}
	if env := os.Getenv("SDS_TOKEN"); env != "" {
		return env
	}
	if home, err := os.UserHomeDir(); err == nil {
		if token := readTokenFile(filepath.Join(home, ".sds", "token")); token != "" {
			return token
		}
	}
	return readTokenFile(systemTokenPath)
}

func readTokenFile(path string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}

// newSDSClient builds the controller client with the resolved API token.
// Every command must use this instead of client.NewSDSClient directly.
func newSDSClient() (*client.SDSClient, error) {
	opts := []client.Option{}
	if token := resolveToken(); token != "" {
		opts = append(opts, client.WithToken(token))
	}
	return client.NewSDSClient(controllerAddr, opts...)
}
