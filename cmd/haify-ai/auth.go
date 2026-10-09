package main

import (
	"crypto/subtle"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

// systemTokenPath is where cluster setup writes the host-wide API token. It is
// duplicated from pkg/client rather than imported: haify-ai is a separate Go
// module on purpose, so that the AI dependency tree never reaches the
// controller, and importing the controller's module for one constant would undo
// that.
const systemTokenPath = "/etc/haify/token"

// resolveToken finds the token haify-ai should require, in the same order the
// rest of the tooling uses so an operator does not learn a second convention:
// HAIFY_AI_TOKEN, then HAIFY_TOKEN, then ~/.haify/token, then /etc/haify/token.
//
// An empty result means no token is configured at all, which is the case
// requireAuth refuses to serve on a public address.
func resolveToken() string {
	for _, env := range []string{"HAIFY_AI_TOKEN", "HAIFY_TOKEN"} {
		if v := strings.TrimSpace(os.Getenv(env)); v != "" {
			return v
		}
	}
	if home, err := os.UserHomeDir(); err == nil {
		if t := readTokenFile(filepath.Join(home, ".haify", "token")); t != "" {
			return t
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

// isLoopback reports whether a listen address reaches only this machine.
//
// An empty host — ":7634" — is every interface, which is what made this
// service reachable from the whole network. It is deliberately NOT treated as
// loopback.
func isLoopback(addr string) bool {
	host := addr
	if i := strings.LastIndex(addr, ":"); i >= 0 {
		host = addr[:i]
	}
	host = strings.Trim(host, "[]")
	switch host {
	case "127.0.0.1", "::1", "localhost":
		return true
	}
	return strings.HasPrefix(host, "127.")
}

// requireAuth guards every route with a bearer token.
//
// haify-ai had no authentication of any kind while listening on every interface,
// and the web UI proxies /ai/* straight through from its own public port — so
// binding to loopback alone would not have closed anything. Anyone who could
// reach either port could rewrite the knowledge base the Copilot answers from,
// erase it, or spend the cluster's LLM budget. Poisoning is the worst of the
// three: it produces confident answers with a Sources footer, delivered to an
// operator who is diagnosing an outage.
//
// /ai/health is left open on purpose. It reports liveness and MCP connectivity,
// carries nothing an attacker can use, and is what a probe or a load balancer
// calls without credentials.
func requireAuth(token string, next http.Handler) http.Handler {
	want := []byte(token)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/ai/health" || r.Method == http.MethodOptions {
			next.ServeHTTP(w, r)
			return
		}
		got := strings.TrimSpace(strings.TrimPrefix(bearer(r), "Bearer "))
		// Constant time: the comparison is against a secret, and a length or
		// prefix leak is enough to make guessing tractable.
		if subtle.ConstantTimeCompare([]byte(got), want) != 1 {
			w.Header().Set("WWW-Authenticate", `Bearer realm="haify-ai"`)
			http.Error(w, "unauthorized: send Authorization: Bearer <token>", http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func bearer(r *http.Request) string {
	if h := r.Header.Get("Authorization"); h != "" {
		return h
	}
	// EventSource and a few proxies cannot set headers; accept the same token
	// as a query parameter for those callers only.
	if q := r.URL.Query().Get("token"); q != "" {
		return "Bearer " + q
	}
	return ""
}

// guard wires authentication for the configured listen address, refusing the
// one combination that cannot be made safe.
//
// A public address with no token is not served. That is a deliberate
// hard failure rather than a warning: the previous behaviour was exactly this
// configuration, it looked like it was working, and nothing about it suggested
// the knowledge base was writable by the network. An operator who wants the
// old reach sets a token; one who wants no token binds to loopback, where the
// UI's own proxy still reaches it.
func guard(addr string, next http.Handler) (http.Handler, error) {
	token := resolveToken()
	switch {
	case token != "":
		log.Printf("haify-ai: bearer token required on every route except /ai/health")
		return requireAuth(token, next), nil
	case isLoopback(addr):
		log.Printf("haify-ai: no token configured; serving unauthenticated on %s, which only this host can reach", addr)
		return next, nil
	default:
		return nil, &configError{addr: addr}
	}
}

type configError struct{ addr string }

func (e *configError) Error() string {
	return "refusing to serve on " + e.addr + " with no token: the knowledge base and chat " +
		"endpoints would be writable by anyone who can reach this address, and the web UI " +
		"proxies them through its own public port. Set HAIFY_AI_TOKEN (or write /etc/haify/token), " +
		"or set HAIFY_AI_ADDR=127.0.0.1:7634 to serve only this host"
}
