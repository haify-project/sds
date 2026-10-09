package mcpauth

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

type oauthEnv struct {
	t      *testing.T
	store  *Store
	server *httptest.Server
	secret string
}

func newOAuthEnv(t *testing.T, role Role, max Role) *oauthEnv {
	t.Helper()
	s, _ := openTest(t)
	secret, _, _ := s.Create("operator", role, 0)
	mux := http.NewServeMux()
	o := NewOAuth(OAuthConfig{Store: s, PublicURL: "https://mcp.example.test", MaxRole: max})
	o.Mount(mux)
	return &oauthEnv{t: t, store: s, server: httptest.NewServer(mux), secret: secret}
}

func (e *oauthEnv) post(path string, form url.Values) (*http.Response, string) {
	e.t.Helper()
	resp, err := (&http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}).
		PostForm(e.server.URL+path, form)
	if err != nil {
		e.t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	var b strings.Builder
	buf := make([]byte, 4096)
	for {
		n, err := resp.Body.Read(buf)
		b.Write(buf[:n])
		if err != nil {
			break
		}
	}
	return resp, b.String()
}

func (e *oauthEnv) register(redirect string) string {
	e.t.Helper()
	body := `{"client_name":"ChatGPT","redirect_uris":["` + redirect + `"]}`
	resp, err := http.Post(e.server.URL+"/oauth/register", "application/json", strings.NewReader(body))
	if err != nil {
		e.t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	var out struct {
		ClientID string `json:"client_id"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&out)
	if resp.StatusCode != 201 || out.ClientID == "" {
		e.t.Fatalf("register: %d", resp.StatusCode)
	}
	return out.ClientID
}

func pkce() (verifier, challenge string) {
	verifier = "a-long-random-verifier-string-that-is-at-least-43-chars-long"
	sum := sha256.Sum256([]byte(verifier))
	return verifier, base64.RawURLEncoding.EncodeToString(sum[:])
}

func (e *oauthEnv) authorize(client, redirect, challenge, token, scope string) (code string, status int) {
	form := url.Values{"client_id": {client}, "redirect_uri": {redirect}, "response_type": {"code"},
		"code_challenge": {challenge}, "code_challenge_method": {"S256"}, "state": {"xyz"}, "token": {token}, "scope": {scope}}
	resp, _ := e.post("/oauth/authorize", form)
	if resp.StatusCode != http.StatusFound {
		return "", resp.StatusCode
	}
	loc, _ := url.Parse(resp.Header.Get("Location"))
	if loc.Query().Get("state") != "xyz" {
		e.t.Fatal("state must come back untouched")
	}
	return loc.Query().Get("code"), resp.StatusCode
}

func TestOAuthFlowEndToEnd(t *testing.T) {
	e := newOAuthEnv(t, RoleAdmin, RoleAdmin)
	defer e.server.Close()
	redirect := "https://chatgpt.com/connector/callback"
	client := e.register(redirect)
	verifier, challenge := pkce()

	// A wrong token on the authorization page is refused.
	if code, status := e.authorize(client, redirect, challenge, "haifymcp_nope", ""); code != "" || status != http.StatusUnauthorized {
		t.Fatalf("bad token: %q %d", code, status)
	}
	code, _ := e.authorize(client, redirect, challenge, e.secret, "haify:read")
	if code == "" {
		t.Fatal("no code for a valid token")
	}

	// A wrong verifier does not get the code exchanged, and the code is then spent.
	resp, _ := e.post("/oauth/token", url.Values{"grant_type": {"authorization_code"}, "client_id": {client},
		"code": {code}, "redirect_uri": {redirect}, "code_verifier": {"wrong"}})
	if resp.StatusCode != 400 {
		t.Fatalf("wrong verifier: %d", resp.StatusCode)
	}
	code, _ = e.authorize(client, redirect, challenge, e.secret, "haify:read")
	resp, body := e.post("/oauth/token", url.Values{"grant_type": {"authorization_code"}, "client_id": {client},
		"code": {code}, "redirect_uri": {redirect}, "code_verifier": {verifier}})
	if resp.StatusCode != 200 {
		t.Fatalf("exchange: %d %s", resp.StatusCode, body)
	}
	var tok struct {
		Access  string `json:"access_token"`
		Refresh string `json:"refresh_token"`
		Scope   string `json:"scope"`
	}
	_ = json.Unmarshal([]byte(body), &tok)
	if tok.Scope != ScopeRead {
		t.Fatalf("asked for read, got scope %q", tok.Scope)
	}
	got, err := e.store.Verify(tok.Access)
	if err != nil || got.Role != RoleRead {
		t.Fatalf("access token: %+v %v", got, err)
	}
	if _, err := e.store.Verify(tok.Refresh); err == nil {
		t.Fatal("a refresh token must not work as a bearer")
	}

	// The code is single use.
	resp, _ = e.post("/oauth/token", url.Values{"grant_type": {"authorization_code"}, "client_id": {client},
		"code": {code}, "redirect_uri": {redirect}, "code_verifier": {verifier}})
	if resp.StatusCode != 400 {
		t.Fatalf("code reuse: %d", resp.StatusCode)
	}

	// Refresh rotates: the new pair works, the old refresh token does not.
	resp, body = e.post("/oauth/token", url.Values{"grant_type": {"refresh_token"}, "client_id": {client}, "refresh_token": {tok.Refresh}})
	if resp.StatusCode != 200 {
		t.Fatalf("refresh: %d %s", resp.StatusCode, body)
	}
	var next struct {
		Access, Refresh string
	}
	var raw map[string]any
	_ = json.Unmarshal([]byte(body), &raw)
	next.Access, next.Refresh = raw["access_token"].(string), raw["refresh_token"].(string)
	if _, err := e.store.Verify(tok.Access); err == nil {
		t.Fatal("the old access token must be replaced")
	}
	if resp, _ = e.post("/oauth/token", url.Values{"grant_type": {"refresh_token"}, "client_id": {client}, "refresh_token": {tok.Refresh}}); resp.StatusCode != 400 {
		t.Fatal("a spent refresh token was accepted")
	}

	// Revoking the approving token takes the client's tokens with it.
	if _, err := e.store.Revoke("operator"); err != nil {
		t.Fatal(err)
	}
	if _, err := e.store.Verify(next.Access); err == nil {
		t.Fatal("the client kept access after the token that approved it was revoked")
	}
	if resp, _ = e.post("/oauth/token", url.Values{"grant_type": {"refresh_token"}, "client_id": {client}, "refresh_token": {next.Refresh}}); resp.StatusCode != 400 {
		t.Fatal("refresh worked after the approving token was revoked")
	}
}

func TestOAuthNeverGrantsMoreThanAllowed(t *testing.T) {
	e := newOAuthEnv(t, RoleAdmin, RoleOperate) // the server is capped at operate
	defer e.server.Close()
	redirect := "https://claude.ai/api/mcp/auth_callback"
	client := e.register(redirect)
	verifier, challenge := pkce()
	code, _ := e.authorize(client, redirect, challenge, e.secret, "haify:admin")
	_, body := e.post("/oauth/token", url.Values{"grant_type": {"authorization_code"}, "client_id": {client},
		"code": {code}, "redirect_uri": {redirect}, "code_verifier": {verifier}})
	var tok struct {
		Access string `json:"access_token"`
	}
	_ = json.Unmarshal([]byte(body), &tok)
	got, err := e.store.Verify(tok.Access)
	if err != nil || got.Role != RoleOperate {
		t.Fatalf("an admin token asking for admin on an operate-capped server got %+v (%v)", got, err)
	}
}

func TestOAuthRejectsUnregisteredRedirectAndBadRegistration(t *testing.T) {
	e := newOAuthEnv(t, RoleAdmin, RoleAdmin)
	defer e.server.Close()
	client := e.register("https://good.example/cb")
	_, challenge := pkce()
	if _, status := e.authorize(client, "https://evil.example/cb", challenge, e.secret, ""); status != http.StatusBadRequest {
		t.Fatalf("an unregistered redirect must be refused outright, got %d", status)
	}
	for _, bad := range []string{`http://public.example/cb`, `javascript:alert(1)`, `https://x.example/cb#frag`} {
		resp, err := http.Post(e.server.URL+"/oauth/register", "application/json", strings.NewReader(`{"redirect_uris":["`+bad+`"]}`))
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
		if resp.StatusCode != 400 {
			t.Fatalf("redirect %q accepted", bad)
		}
	}
	if resp, err := http.Post(e.server.URL+"/oauth/register", "application/json",
		strings.NewReader(`{"redirect_uris":["http://localhost:6274/cb"]}`)); err != nil || resp.StatusCode != 201 {
		t.Fatal("a loopback http redirect is what desktop clients use")
	}
	// Without PKCE there is no authorization.
	form := url.Values{"client_id": {client}, "redirect_uri": {"https://good.example/cb"}, "response_type": {"code"}, "token": {e.secret}}
	if resp, _ := e.post("/oauth/authorize", form); resp.StatusCode != http.StatusFound || !strings.Contains(resp.Header.Get("Location"), "error=invalid_request") {
		t.Fatal("a request without PKCE must be refused through the redirect")
	}
}

func TestMetadata(t *testing.T) {
	e := newOAuthEnv(t, RoleRead, RoleAdmin)
	defer e.server.Close()
	resp, err := http.Get(e.server.URL + "/.well-known/oauth-authorization-server")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	var m map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&m)
	if m["issuer"] != "https://mcp.example.test" || m["registration_endpoint"] != "https://mcp.example.test/oauth/register" {
		t.Fatalf("metadata: %v", m)
	}
}
