package controller

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/haify-project/haify/pkg/rbac"
)

// doReq invokes a runtime.HandlerFunc-style handler with an optional JSON body,
// bearer token and path params, returning the recorder and decoded JSON.
func doReq(
	h func(http.ResponseWriter, *http.Request, map[string]string),
	method, token, body string,
	params map[string]string,
) (*httptest.ResponseRecorder, map[string]any) {
	var r *http.Request
	if body != "" {
		r = httptest.NewRequest(method, "/", strings.NewReader(body))
	} else {
		r = httptest.NewRequest(method, "/", nil)
	}
	if token != "" {
		r.Header.Set("Authorization", "Bearer "+token)
	}
	w := httptest.NewRecorder()
	h(w, r, params)
	var out map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &out)
	return w, out
}

func routeEngine(t *testing.T) *rbac.Engine {
	t.Helper()
	e, err := rbac.New(nil, []rbac.User{
		{Name: "alice", Token: "alice-token-0123456789", Role: "admin"},
		{Name: "victor", Token: "victor-token-0123456789", Role: "viewer"},
	}, nil)
	if err != nil {
		t.Fatalf("rbac.New: %v", err)
	}
	return e
}

func doGet(h http.HandlerFunc, token string) (*httptest.ResponseRecorder, map[string]any) {
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	if token != "" {
		r.Header.Set("Authorization", "Bearer "+token)
	}
	w := httptest.NewRecorder()
	h(w, r)
	var body map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &body)
	return w, body
}

func TestWhoamiDisabledWhenNoEngine(t *testing.T) {
	for _, tokenAuth := range []bool{false, true} {
		h := func(w http.ResponseWriter, r *http.Request) {
			rbacWhoamiHandler(nil, tokenAuth)(w, r, nil)
		}
		w, body := doGet(h, "")
		if w.Code != http.StatusOK || body["enabled"] != false {
			t.Errorf("got code=%d enabled=%v, want 200 enabled=false", w.Code, body["enabled"])
		}
		// The page tells a shared token apart from no authentication at all.
		if body["token_auth"] != tokenAuth {
			t.Errorf("token_auth = %v, want %v", body["token_auth"], tokenAuth)
		}
	}
}

func TestWhoamiReportsRole(t *testing.T) {
	engine := routeEngine(t)
	h := func(w http.ResponseWriter, r *http.Request) {
		rbacWhoamiHandler(engine, false)(w, r, nil)
	}
	w, body := doGet(h, "alice-token-0123456789")
	if w.Code != http.StatusOK {
		t.Fatalf("code = %d, want 200", w.Code)
	}
	if body["user"] != "alice" || body["role"] != "admin" || body["can_admin"] != true {
		t.Errorf("body = %v, want alice/admin/can_admin=true", body)
	}
}

func TestWhoamiRejectsUnknownToken(t *testing.T) {
	engine := routeEngine(t)
	h := func(w http.ResponseWriter, r *http.Request) {
		rbacWhoamiHandler(engine, false)(w, r, nil)
	}
	w, _ := doGet(h, "nope")
	if w.Code != http.StatusUnauthorized {
		t.Errorf("code = %d, want 401", w.Code)
	}
}

func TestPoliciesAdminOnly(t *testing.T) {
	engine := routeEngine(t)
	h := func(w http.ResponseWriter, r *http.Request) {
		rbacPoliciesHandler(engine)(w, r, nil)
	}

	// viewer is forbidden
	if w, _ := doGet(h, "victor-token-0123456789"); w.Code != http.StatusForbidden {
		t.Errorf("viewer code = %d, want 403", w.Code)
	}

	// admin gets the policy and user lists
	w, body := doGet(h, "alice-token-0123456789")
	if w.Code != http.StatusOK {
		t.Fatalf("admin code = %d, want 200", w.Code)
	}
	if _, ok := body["policies"].([]any); !ok {
		t.Errorf("expected policies array, got %T", body["policies"])
	}
	if users, ok := body["users"].([]any); !ok || len(users) != 2 {
		t.Errorf("expected 2 users, got %v", body["users"])
	}
}

func TestCreateUserViaHandler(t *testing.T) {
	engine := routeEngine(t)
	h := rbacCreateUserHandler(engine, nil)

	// viewer cannot create users
	if w, _ := doReq(h, http.MethodPost, "victor-token-0123456789",
		`{"name":"x","role":"viewer"}`, nil); w.Code != http.StatusForbidden {
		t.Errorf("viewer create code = %d, want 403", w.Code)
	}

	// admin creates a user and receives a token that resolves
	w, body := doReq(h, http.MethodPost, "alice-token-0123456789",
		`{"name":"nina","role":"operator"}`, nil)
	if w.Code != http.StatusOK {
		t.Fatalf("admin create code = %d, want 200", w.Code)
	}
	tok, _ := body["token"].(string)
	if tok == "" {
		t.Fatalf("expected a generated token in response")
	}
	if name, ok := engine.ResolveUser(tok); !ok || name != "nina" {
		t.Errorf("created token does not resolve to nina")
	}
}

func TestSetRoleAndDeleteViaHandler(t *testing.T) {
	engine := routeEngine(t)
	if _, err := engine.CreateUser("temp", "temp-token-0123456789", "viewer"); err != nil {
		t.Fatalf("seed user: %v", err)
	}

	// set-role: viewer -> operator
	setRole := rbacSetRoleHandler(engine, nil)
	w, _ := doReq(setRole, http.MethodPut, "alice-token-0123456789",
		`{"role":"operator"}`, map[string]string{"name": "temp"})
	if w.Code != http.StatusOK {
		t.Fatalf("set-role code = %d, want 200", w.Code)
	}
	if engine.RoleOf("temp") != "operator" {
		t.Errorf("role not changed, got %q", engine.RoleOf("temp"))
	}

	// delete
	del := rbacDeleteUserHandler(engine, nil)
	w, _ = doReq(del, http.MethodDelete, "alice-token-0123456789", "",
		map[string]string{"name": "temp"})
	if w.Code != http.StatusOK {
		t.Fatalf("delete code = %d, want 200", w.Code)
	}
	if _, ok := engine.ResolveUser("temp-token-0123456789"); ok {
		t.Errorf("deleted user's token still resolves")
	}
}
