package rbac

import "testing"

// memStore is an in-memory Store for exercising persistence.
type memStore struct{ snap *Snapshot }

func (m *memStore) Load() (*Snapshot, error) { return m.snap, nil }
func (m *memStore) Save(s *Snapshot) error   { m.snap = s; return nil }

func TestCreateAndResolveUser(t *testing.T) {
	store := &memStore{}
	e, err := New(store, nil, nil)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	token, err := e.CreateUser("nina", "", "operator")
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	if len(token) < 16 {
		t.Errorf("generated token too short: %q", token)
	}
	if name, ok := e.ResolveUser(token); !ok || name != "nina" {
		t.Errorf("ResolveUser = (%q,%v), want nina,true", name, ok)
	}
	if ok, _ := e.Enforce("nina", "gateway", ActWrite); !ok {
		t.Errorf("operator should be allowed gateway write")
	}
}

func TestCreateUserRejectsBadInput(t *testing.T) {
	e, _ := New(&memStore{}, nil, nil)
	if _, err := e.CreateUser("bob", "tok", "operator"); err == nil {
		t.Errorf("short token should be rejected")
	}
	if _, err := e.CreateUser("bob", "valid-token-0123456789", "wizard"); err == nil {
		t.Errorf("unknown role should be rejected")
	}
	if _, err := e.CreateUser("bob", "valid-token-0123456789", "viewer"); err != nil {
		t.Fatalf("valid create failed: %v", err)
	}
	if _, err := e.CreateUser("bob", "another-token-0123456789", "viewer"); err == nil {
		t.Errorf("duplicate user should be rejected")
	}
}

func TestSetRole(t *testing.T) {
	e, _ := New(&memStore{}, nil, nil)
	tok, _ := e.CreateUser("kim", "", "viewer")
	if ok, _ := e.Enforce("kim", "pool", ActWrite); ok {
		t.Fatalf("viewer should not write")
	}
	if err := e.SetRole("kim", "admin"); err != nil {
		t.Fatalf("SetRole: %v", err)
	}
	if ok, _ := e.Enforce("kim", "pool", ActWrite); !ok {
		t.Errorf("after promotion to admin, write should be allowed")
	}
	// token unchanged across role change
	if name, ok := e.ResolveUser(tok); !ok || name != "kim" {
		t.Errorf("token should still resolve after role change")
	}
}

func TestDeleteUserAndPinnedProtection(t *testing.T) {
	store := &memStore{}
	// alice is config-pinned; she must not be deletable.
	e, err := New(store, []User{{Name: "alice", Token: "alice-token-0123456789", Role: "admin"}}, nil)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := e.DeleteUser("alice"); err == nil {
		t.Errorf("pinned user should not be deletable")
	}
	tok, _ := e.CreateUser("temp", "", "viewer")
	if err := e.DeleteUser("temp"); err != nil {
		t.Fatalf("DeleteUser: %v", err)
	}
	if _, ok := e.ResolveUser(tok); ok {
		t.Errorf("deleted user's token should no longer resolve")
	}
}

func TestPersistenceRoundTrip(t *testing.T) {
	store := &memStore{}
	e1, _ := New(store, []User{{Name: "alice", Token: "alice-token-0123456789", Role: "admin"}}, nil)
	tok, _ := e1.CreateUser("dana", "", "operator")

	// A fresh engine over the same store must see the runtime-added user.
	e2, err := New(store, []User{{Name: "alice", Token: "alice-token-0123456789", Role: "admin"}}, nil)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	if name, ok := e2.ResolveUser(tok); !ok || name != "dana" {
		t.Errorf("persisted user not restored: (%q,%v)", name, ok)
	}
	if e2.RoleOf("dana") != "operator" {
		t.Errorf("persisted role not restored: %q", e2.RoleOf("dana"))
	}
	// pinned status is reconstructed from config, not persisted.
	users := e2.Users()
	var alicePinned, danaPinned bool
	for _, u := range users {
		if u.Name == "alice" {
			alicePinned = u.Pinned
		}
		if u.Name == "dana" {
			danaPinned = u.Pinned
		}
	}
	if !alicePinned {
		t.Errorf("alice should be pinned")
	}
	if danaPinned {
		t.Errorf("dana (runtime) should not be pinned")
	}
}
