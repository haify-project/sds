package mcpauth

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// The authorization server for clients that cannot be given a token by hand.
//
// ChatGPT and claude.ai add an MCP server by URL and then run OAuth against
// it: they discover the endpoints below, register themselves, and send the
// user to /oauth/authorize. There the operator pastes a token made with
// `sds-mcp token create`, which is how "the human approves this client" is
// expressed without a second kind of credential to look after. The client is
// then given an access token of the same role, or lower if it asked for less,
// that lives an hour and is renewed with a refresh token for as long as the
// approving token stands.
//
// Public clients with PKCE (S256) only: there is no client secret to leak, and
// a stolen authorization code is useless without the verifier.

const (
	authCodeTTL     = 5 * time.Minute
	defaultAccess   = time.Hour
	defaultRefresh  = 30 * 24 * time.Hour
	maxAuthCodes    = 1000
	maxRedirectURIs = 10
)

// OAuthConfig configures the authorization server.
type OAuthConfig struct {
	Store *Store
	// PublicURL is where clients reach this server, without a trailing slash:
	// https://mcp.example.com. It is the issuer, and the base of every URL in
	// the metadata.
	PublicURL string
	// Limiter counts bad tokens pasted into the authorization page.
	Limiter    *Limiter
	TrustProxy bool
	// AccessTTL and RefreshTTL default to an hour and thirty days.
	AccessTTL, RefreshTTL time.Duration
	// MaxRole caps what any client can be granted, whatever the approving
	// token allows.
	MaxRole Role
}

// OAuth serves the endpoints.
type OAuth struct {
	cfg OAuthConfig

	mu    sync.Mutex
	codes map[string]authCode
}

type authCode struct {
	client    string
	redirect  string
	challenge string
	parent    Token
	role      Role
	expires   time.Time
}

// NewOAuth builds the server.
func NewOAuth(cfg OAuthConfig) *OAuth {
	cfg.PublicURL = strings.TrimRight(cfg.PublicURL, "/")
	if cfg.AccessTTL <= 0 {
		cfg.AccessTTL = defaultAccess
	}
	if cfg.RefreshTTL <= 0 {
		cfg.RefreshTTL = defaultRefresh
	}
	if cfg.MaxRole == "" {
		cfg.MaxRole = RoleAdmin
	}
	if cfg.Limiter == nil {
		cfg.Limiter = NewLimiter(10, 5*time.Minute)
	}
	return &OAuth{cfg: cfg, codes: map[string]authCode{}}
}

// Mount registers the OAuth endpoints on mux.
func (o *OAuth) Mount(mux *http.ServeMux) {
	mux.HandleFunc("/.well-known/oauth-authorization-server", o.metadata)
	mux.HandleFunc("/oauth/register", o.register)
	mux.HandleFunc("/oauth/authorize", o.authorize)
	mux.HandleFunc("/oauth/token", o.token)
}

func (o *OAuth) scopes() []string { return o.cfg.MaxRole.Scopes() }

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func oauthError(w http.ResponseWriter, status int, code, desc string) {
	writeJSON(w, status, map[string]string{"error": code, "error_description": desc})
}

func (o *OAuth) metadata(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"issuer":                                o.cfg.PublicURL,
		"authorization_endpoint":                o.cfg.PublicURL + "/oauth/authorize",
		"token_endpoint":                        o.cfg.PublicURL + "/oauth/token",
		"registration_endpoint":                 o.cfg.PublicURL + "/oauth/register",
		"response_types_supported":              []string{"code"},
		"grant_types_supported":                 []string{"authorization_code", "refresh_token"},
		"code_challenge_methods_supported":      []string{"S256"},
		"token_endpoint_auth_methods_supported": []string{"none"},
		"scopes_supported":                      o.scopes(),
	})
}

// validRedirect accepts https, and http only to a loopback address, which is
// where a desktop client listens for the redirect.
func validRedirect(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || u.Fragment != "" {
		return false
	}
	switch u.Scheme {
	case "https":
		return true
	case "http":
		h := u.Hostname()
		return h == "localhost" || h == "127.0.0.1" || h == "::1"
	}
	return false
}

func (o *OAuth) register(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		oauthError(w, http.StatusMethodNotAllowed, "invalid_request", "POST only")
		return
	}
	ip := ClientIP(r, o.cfg.TrustProxy)
	if o.cfg.Limiter.Blocked("register:" + ip) {
		oauthError(w, http.StatusTooManyRequests, "slow_down", "too many registrations")
		return
	}
	var req struct {
		ClientName   string   `json:"client_name"`
		RedirectURIs []string `json:"redirect_uris"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&req); err != nil {
		oauthError(w, http.StatusBadRequest, "invalid_client_metadata", "body is not JSON")
		return
	}
	if len(req.RedirectURIs) == 0 || len(req.RedirectURIs) > maxRedirectURIs {
		oauthError(w, http.StatusBadRequest, "invalid_redirect_uri", "give between 1 and 10 redirect_uris")
		return
	}
	for _, u := range req.RedirectURIs {
		if !validRedirect(u) {
			oauthError(w, http.StatusBadRequest, "invalid_redirect_uri", fmt.Sprintf("%q must be https, or http to localhost", u))
			return
		}
	}
	name := strings.TrimSpace(req.ClientName)
	if name == "" {
		name = "unnamed client"
	}
	if len(name) > 80 {
		name = name[:80]
	}
	// Every registration counts against the address, so one host cannot fill
	// the client list; a normal client registers once.
	o.cfg.Limiter.Fail("register:" + ip)
	c, err := o.cfg.Store.RegisterClient(name, req.RedirectURIs)
	if err != nil {
		oauthError(w, http.StatusInternalServerError, "server_error", "could not store the client")
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{
		"client_id":                  c.ID,
		"client_name":                c.Name,
		"redirect_uris":              c.RedirectURIs,
		"grant_types":                []string{"authorization_code", "refresh_token"},
		"response_types":             []string{"code"},
		"token_endpoint_auth_method": "none",
	})
}

var authorizePage = template.Must(template.New("authorize").Parse(`<!doctype html>
<html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1">
<title>Authorize {{.Client}}</title>
<style>
body{font:16px/1.5 system-ui,sans-serif;max-width:32rem;margin:12vh auto;padding:0 1rem;color:#1c1c1e}
h1{font-size:1.25rem;margin:0 0 .5rem}p{margin:.5rem 0}
input[type=password]{width:100%;box-sizing:border-box;padding:.6rem;font:inherit;border:1px solid #888;border-radius:6px}
button{margin-top:.8rem;padding:.55rem 1.1rem;font:inherit;border:0;border-radius:6px;background:#0a5;color:#fff;cursor:pointer}
.err{color:#b00020}.dim{color:#666;font-size:.9rem}
@media(prefers-color-scheme:dark){body{background:#111;color:#eee}input{background:#222;color:#eee}.dim{color:#aaa}}
</style></head><body>
<h1>Authorize {{.Client}}</h1>
<p>{{.Client}} wants to use the SDS cluster at {{.Host}}.</p>
{{if .Error}}<p class="err">{{.Error}}</p>{{end}}
<form method="post">
{{range $k, $v := .Hidden}}<input type="hidden" name="{{$k}}" value="{{$v}}">{{end}}
<label for="t">Access token</label>
<input id="t" name="token" type="password" autocomplete="off" autofocus required>
<p class="dim">Paste a token made with <code>sds-mcp token create</code>. {{.Client}} gets the same role as that token{{if .Capped}}, at most <b>{{.Capped}}</b>{{end}}, and loses access when the token is revoked.</p>
<button type="submit">Authorize</button>
</form></body></html>`))

func (o *OAuth) page(w http.ResponseWriter, status int, client Client, hidden map[string]string, errMsg string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Frame-Options", "DENY")
	w.Header().Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'; form-action 'self'; frame-ancestors 'none'")
	w.WriteHeader(status)
	host := strings.TrimPrefix(strings.TrimPrefix(o.cfg.PublicURL, "https://"), "http://")
	capped := ""
	if o.cfg.MaxRole != RoleAdmin {
		capped = string(o.cfg.MaxRole)
	}
	_ = authorizePage.Execute(w, map[string]any{
		"Client": client.Name, "Host": host, "Hidden": hidden, "Error": errMsg, "Capped": capped,
	})
}

func plainError(w http.ResponseWriter, status int, msg string) {
	http.Error(w, msg, status)
}

func (o *OAuth) authorize(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		plainError(w, http.StatusBadRequest, "bad request")
		return
	}
	get := func(k string) string { return r.Form.Get(k) }
	client, ok := o.cfg.Store.Client(get("client_id"))
	if !ok {
		plainError(w, http.StatusBadRequest, "unknown client_id")
		return
	}
	redirect := get("redirect_uri")
	match := false
	for _, u := range client.RedirectURIs {
		if u == redirect {
			match = true
		}
	}
	if !match {
		plainError(w, http.StatusBadRequest, "redirect_uri is not one the client registered")
		return
	}
	// From here an error goes back to the client through its redirect.
	fail := func(code, desc string) {
		u, _ := url.Parse(redirect)
		q := u.Query()
		q.Set("error", code)
		q.Set("error_description", desc)
		if s := get("state"); s != "" {
			q.Set("state", s)
		}
		u.RawQuery = q.Encode()
		http.Redirect(w, r, u.String(), http.StatusFound)
	}
	if get("response_type") != "code" {
		fail("unsupported_response_type", "only response_type=code")
		return
	}
	if get("code_challenge") == "" || get("code_challenge_method") != "S256" {
		fail("invalid_request", "PKCE with code_challenge_method=S256 is required")
		return
	}
	hidden := map[string]string{}
	for _, k := range []string{"client_id", "redirect_uri", "response_type", "code_challenge", "code_challenge_method", "state", "scope"} {
		hidden[k] = get(k)
	}
	if r.Method != http.MethodPost {
		o.page(w, http.StatusOK, client, hidden, "")
		return
	}

	ip := ClientIP(r, o.cfg.TrustProxy)
	if o.cfg.Limiter.Blocked("authorize:" + ip) {
		o.page(w, http.StatusTooManyRequests, client, hidden, "Too many attempts. Wait a few minutes.")
		return
	}
	parent, err := o.cfg.Store.Verify(strings.TrimSpace(get("token")))
	if err != nil || parent.Kind != KindStatic {
		o.cfg.Limiter.Fail("authorize:" + ip)
		o.page(w, http.StatusUnauthorized, client, hidden, "That is not a valid access token.")
		return
	}
	role := Min(parent.Role, o.cfg.MaxRole)
	if scope := get("scope"); scope != "" {
		if asked := RoleFromScopes(strings.Fields(scope)); asked.Rank() > 0 {
			role = Min(role, asked)
		}
	}
	code, err := randomCode()
	if err != nil {
		fail("server_error", "could not make a code")
		return
	}
	o.mu.Lock()
	now := time.Now()
	for k, c := range o.codes {
		if now.After(c.expires) {
			delete(o.codes, k)
		}
	}
	if len(o.codes) >= maxAuthCodes {
		o.mu.Unlock()
		fail("temporarily_unavailable", "too many pending authorizations")
		return
	}
	o.codes[code] = authCode{client: client.ID, redirect: redirect, challenge: get("code_challenge"),
		parent: parent, role: role, expires: now.Add(authCodeTTL)}
	o.mu.Unlock()

	u, _ := url.Parse(redirect)
	q := u.Query()
	q.Set("code", code)
	if s := get("state"); s != "" {
		q.Set("state", s)
	}
	u.RawQuery = q.Encode()
	http.Redirect(w, r, u.String(), http.StatusFound)
}

func randomCode() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

func (o *OAuth) token(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		oauthError(w, http.StatusMethodNotAllowed, "invalid_request", "POST only")
		return
	}
	if err := r.ParseForm(); err != nil {
		oauthError(w, http.StatusBadRequest, "invalid_request", "form body expected")
		return
	}
	get := func(k string) string { return r.PostForm.Get(k) }
	client, ok := o.cfg.Store.Client(get("client_id"))
	if !ok {
		oauthError(w, http.StatusUnauthorized, "invalid_client", "unknown client_id")
		return
	}
	switch get("grant_type") {
	case "authorization_code":
		o.mu.Lock()
		c, found := o.codes[get("code")]
		delete(o.codes, get("code")) // single use, whatever happens next
		o.mu.Unlock()
		if !found || time.Now().After(c.expires) || c.client != client.ID || c.redirect != get("redirect_uri") {
			oauthError(w, http.StatusBadRequest, "invalid_grant", "the code is unknown, used, expired, or for another client")
			return
		}
		sum := sha256.Sum256([]byte(get("code_verifier")))
		want := base64.RawURLEncoding.EncodeToString(sum[:])
		if subtle.ConstantTimeCompare([]byte(want), []byte(c.challenge)) != 1 {
			oauthError(w, http.StatusBadRequest, "invalid_grant", "code_verifier does not match the code_challenge")
			return
		}
		access, refresh, exp, err := o.cfg.Store.IssueOAuth(c.parent, client, c.role, o.cfg.AccessTTL, o.cfg.RefreshTTL)
		if err != nil {
			oauthError(w, http.StatusInternalServerError, "server_error", "could not issue a token")
			return
		}
		o.respond(w, access, refresh, exp, c.role)
	case "refresh_token":
		access, refresh, exp, role, err := o.cfg.Store.Refresh(get("refresh_token"), client.ID, o.cfg.AccessTTL, o.cfg.RefreshTTL)
		if errors.Is(err, ErrInvalid) {
			oauthError(w, http.StatusBadRequest, "invalid_grant", "the refresh token is unknown, used, or revoked")
			return
		}
		if err != nil {
			oauthError(w, http.StatusInternalServerError, "server_error", "could not issue a token")
			return
		}
		o.respond(w, access, refresh, exp, role)
	default:
		oauthError(w, http.StatusBadRequest, "unsupported_grant_type", "authorization_code or refresh_token")
	}
}

func (o *OAuth) respond(w http.ResponseWriter, access, refresh string, exp time.Time, role Role) {
	writeJSON(w, http.StatusOK, map[string]any{
		"access_token":  access,
		"token_type":    "Bearer",
		"expires_in":    int(time.Until(exp).Seconds()),
		"refresh_token": refresh,
		"scope":         strings.Join(role.Scopes(), " "),
	})
}
