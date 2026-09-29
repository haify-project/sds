package mcpauth

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func openTest(t *testing.T) (*Store, *time.Time) {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "sub", "tokens.json"))
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	s.now = func() time.Time { return now }
	return s, &now
}

func TestCreateVerifyRevoke(t *testing.T) {
	s, _ := openTest(t)
	secret, tok, err := s.Create("laptop", RoleOperate, 0)
	if err != nil {
		t.Fatal(err)
	}
	got, err := s.Verify(secret)
	if err != nil || got.ID != tok.ID || got.Role != RoleOperate {
		t.Fatalf("verify: %+v, %v", got, err)
	}
	if _, err := s.Verify(secret + "x"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("a wrong token must be refused: %v", err)
	}
	if _, _, err := s.Create("laptop", RoleRead, 0); err == nil {
		t.Fatal("names are unique")
	}
	if n, err := s.Revoke("laptop"); err != nil || n != 1 {
		t.Fatalf("revoke: %d, %v", n, err)
	}
	if _, err := s.Verify(secret); !errors.Is(err, ErrInvalid) {
		t.Fatal("a revoked token must stop working")
	}
}

func TestSecretIsNotStored(t *testing.T) {
	s, _ := openTest(t)
	secret, _, _ := s.Create("a", RoleRead, 0)
	raw, err := os.ReadFile(s.path)
	if err != nil {
		t.Fatal(err)
	}
	if len(raw) == 0 || strings.Contains(string(raw), secret) {
		t.Fatal("the file holds the secret; it must hold only its hash")
	}
	fi, _ := os.Stat(s.path)
	if fi.Mode().Perm() != 0o600 {
		t.Fatalf("token file mode %v, want 0600", fi.Mode().Perm())
	}
}

func TestExpiry(t *testing.T) {
	s, now := openTest(t)
	secret, _, _ := s.Create("short", RoleRead, time.Hour)
	if _, err := s.Verify(secret); err != nil {
		t.Fatal(err)
	}
	*now = now.Add(2 * time.Hour)
	if _, err := s.Verify(secret); !errors.Is(err, ErrInvalid) {
		t.Fatal("an expired token must be refused")
	}
}

// A revoke made by another process — the CLI beside a serving server — must
// take effect on the server's next request.
func TestRevokeElsewhereIsSeen(t *testing.T) {
	s, _ := openTest(t)
	secret, _, _ := s.Create("a", RoleAdmin, 0)
	other, err := Open(s.path)
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(20 * time.Millisecond) // a later mtime than the file the first store saw
	if _, err := other.Revoke("a"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Verify(secret); !errors.Is(err, ErrInvalid) {
		t.Fatal("the server kept accepting a token revoked from the CLI")
	}
}

func TestRoles(t *testing.T) {
	if RoleFromScopes([]string{ScopeRead, ScopeAdmin}) != RoleAdmin || RoleFromScopes(nil) != "" {
		t.Fatal("RoleFromScopes")
	}
	if Min(RoleAdmin, RoleRead) != RoleRead || Min(RoleOperate, RoleAdmin) != RoleOperate {
		t.Fatal("Min")
	}
	if _, err := ParseRole("root"); err == nil {
		t.Fatal("unknown role accepted")
	}
}

func TestLimiter(t *testing.T) {
	l := NewLimiter(3, time.Minute)
	now := time.Now()
	l.now = func() time.Time { return now }
	for range 3 {
		l.Fail("ip")
	}
	if !l.Blocked("ip") || l.Blocked("other") {
		t.Fatal("blocked per address after max failures")
	}
	now = now.Add(2 * time.Minute)
	if l.Blocked("ip") {
		t.Fatal("the block must lapse")
	}
}
