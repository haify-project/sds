package mcpserver

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/auth"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/modelcontextprotocol/go-sdk/oauthex"
	"go.uber.org/zap"

	"github.com/liliang-cn/sds/pkg/mcpauth"
)

// HTTPOptions configures the remote server.
type HTTPOptions struct {
	// Listen is the address to bind, e.g. ":43871".
	Listen string
	// PublicURL is where clients reach the server, e.g. https://mcp.example.com.
	// With it the server also runs the OAuth flow ChatGPT and claude.ai need;
	// without it only bearer tokens work, which is what Claude Code uses.
	PublicURL string
	// Tokens is the credential store.
	Tokens *mcpauth.Store
	// MaxRole caps every token: a deployment can decide that nothing reaching
	// it over the network may delete, whatever the token says. Empty is admin.
	MaxRole mcpauth.Role
	// TLSCert and TLSKey serve HTTPS directly. Leave both empty behind a
	// reverse proxy that terminates TLS.
	TLSCert, TLSKey string
	// TrustProxy takes the client address from X-Forwarded-For, for the
	// failure limiter. Only correct behind a proxy that sets it.
	TrustProxy bool
}

// ServeHTTP serves MCP over streamable HTTP until ctx ends.
//
// Every request must carry a valid token. What a token may call is decided by
// which tool set the connection gets: a "read" token is given a server on
// which no mutating tool exists, an "operate" token one without the
// destructive ones, an "admin" token all of them. The tools are not filtered
// per call — an absent tool cannot be called, whatever the client sends.
func ServeHTTP(ctx context.Context, c ControllerClient, logger *zap.Logger, base Options, h HTTPOptions) error {
	if h.Tokens == nil {
		return errors.New("a token store is required")
	}
	if h.MaxRole == "" {
		h.MaxRole = mcpauth.RoleAdmin
	}
	servers := map[mcpauth.Role]*mcp.Server{}
	for role, opts := range map[mcpauth.Role]Options{
		mcpauth.RoleRead:    {ReadOnly: true},
		mcpauth.RoleOperate: {NoDestructive: true},
		mcpauth.RoleAdmin:   {},
	} {
		opts.Version = base.Version
		servers[role] = New(c, logger, opts).MCPServer()
	}

	limiter := mcpauth.NewLimiter(10, 5*time.Minute)
	verify := func(_ context.Context, secret string, r *http.Request) (*auth.TokenInfo, error) {
		ip := mcpauth.ClientIP(r, h.TrustProxy)
		if limiter.Blocked("bearer:" + ip) {
			return nil, auth.ErrInvalidToken
		}
		t, err := h.Tokens.Verify(secret)
		if err != nil {
			limiter.Fail("bearer:" + ip)
			return nil, auth.ErrInvalidToken
		}
		role := mcpauth.Min(t.Role, h.MaxRole)
		return &auth.TokenInfo{
			Scopes:     role.Scopes(),
			Expiration: t.Expires,
			UserID:     t.ID,
			Extra:      map[string]any{"name": t.Name, "role": string(role)},
		}, nil
	}

	public := strings.TrimRight(h.PublicURL, "/")
	bearer := auth.RequireBearerToken(verify, &auth.RequireBearerTokenOptions{
		ResourceMetadataURL:    publicOr(public, "/.well-known/oauth-protected-resource"),
		AllowMissingExpiration: true,
	})
	mcpHandler := mcp.NewStreamableHTTPHandler(func(r *http.Request) *mcp.Server {
		info := auth.TokenInfoFromContext(r.Context())
		if info == nil {
			return nil
		}
		return servers[mcpauth.Min(mcpauth.RoleFromScopes(info.Scopes), h.MaxRole)]
	}, nil)

	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		_, _ = w.Write([]byte("ok\n"))
	})
	mux.Handle("/mcp", bearer(mcpHandler))
	if public != "" {
		meta := auth.ProtectedResourceMetadataHandler(&oauthex.ProtectedResourceMetadata{
			Resource:               public + "/mcp",
			AuthorizationServers:   []string{public},
			ScopesSupported:        h.MaxRole.Scopes(),
			BearerMethodsSupported: []string{"header"},
		})
		mux.Handle("/.well-known/oauth-protected-resource", meta)
		mux.Handle("/.well-known/oauth-protected-resource/mcp", meta)
		mcpauth.NewOAuth(mcpauth.OAuthConfig{
			Store: h.Tokens, PublicURL: public, Limiter: limiter, TrustProxy: h.TrustProxy, MaxRole: h.MaxRole,
		}).Mount(mux)
	}

	srv := &http.Server{
		Addr:              h.Listen,
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       2 * time.Minute,
	}
	errc := make(chan error, 1)
	go func() {
		if h.TLSCert != "" {
			errc <- srv.ListenAndServeTLS(h.TLSCert, h.TLSKey)
		} else {
			errc <- srv.ListenAndServe()
		}
	}()
	logger.Info("SDS MCP server listening",
		zap.String("addr", h.Listen), zap.Bool("tls", h.TLSCert != ""),
		zap.Bool("oauth", public != ""), zap.String("max_role", string(h.MaxRole)),
		zap.String("version", base.Version))
	select {
	case err := <-errc:
		return err
	case <-ctx.Done():
		sctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		return srv.Shutdown(sctx)
	}
}

func publicOr(public, path string) string {
	if public == "" {
		return ""
	}
	return fmt.Sprintf("%s%s", public, path)
}
