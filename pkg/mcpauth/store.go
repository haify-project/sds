// Package mcpauth is the authentication behind the remote MCP server: access
// tokens with a role each, and the small OAuth 2.1 authorization server that
// lets clients which cannot be handed a token by hand — ChatGPT, claude.ai —
// obtain one.
//
// Claude Code and scripts present a token created with `sds-mcp token create`
// as a bearer credential. A client that speaks only OAuth is sent through the
// authorization flow in oauth.go, where the operator pastes such a token once
// to approve it; what the client gets back is a short-lived token of the same
// role or lower, revoked whenever the token that approved it is.
//
// Only a SHA-256 of a token is stored. A token is 256 random bits, so a fast
// hash is enough: there is nothing to brute-force, and lookups by hash need no
// comparison against every record.
package mcpauth

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// Role is what a token may do.
type Role string

const (
	// RoleRead inspects the cluster and changes nothing.
	RoleRead Role = "read"
	// RoleOperate adds the day-to-day writes: create, grow, snapshot, start,
	// mount. Nothing that deletes data or interrupts service.
	RoleOperate Role = "operate"
	// RoleAdmin is everything, including delete, restore, evict and drain.
	RoleAdmin Role = "admin"
)

// ParseRole accepts the three role names.
func ParseRole(s string) (Role, error) {
	switch r := Role(strings.ToLower(strings.TrimSpace(s))); r {
	case RoleRead, RoleOperate, RoleAdmin:
		return r, nil
	}
	return "", fmt.Errorf("role %q is not one of read, operate, admin", s)
}

// Rank orders roles: a higher rank may do everything a lower one may.
func (r Role) Rank() int {
	switch r {
	case RoleAdmin:
		return 3
	case RoleOperate:
		return 2
	case RoleRead:
		return 1
	}
	return 0
}

// Min is the lesser of two roles.
func Min(a, b Role) Role {
	if a.Rank() <= b.Rank() {
		return a
	}
	return b
}

// Scopes are the OAuth scopes a role is carried in. Each role includes the
// ones below it, so a client asking for less than a token allows gets less.
func (r Role) Scopes() []string {
	switch r {
	case RoleAdmin:
		return []string{ScopeRead, ScopeOperate, ScopeAdmin}
	case RoleOperate:
		return []string{ScopeRead, ScopeOperate}
	case RoleRead:
		return []string{ScopeRead}
	}
	return nil
}

// OAuth scope names.
const (
	ScopeRead    = "sds:read"
	ScopeOperate = "sds:operate"
	ScopeAdmin   = "sds:admin"
)

// RoleFromScopes is the highest role the scopes name.
func RoleFromScopes(scopes []string) Role {
	var best Role
	for _, s := range scopes {
		var r Role
		switch s {
		case ScopeAdmin:
			r = RoleAdmin
		case ScopeOperate:
			r = RoleOperate
		case ScopeRead:
			r = RoleRead
		}
		if r.Rank() > best.Rank() {
			best = r
		}
	}
	return best
}

// Token kinds.
const (
	// KindStatic is a token an operator created; it is what gets pasted into
	// a client's configuration or into the authorization page.
	KindStatic = "static"
	// KindAccess and KindRefresh are what the OAuth flow issues.
	KindAccess  = "access"
	KindRefresh = "refresh"
)

// ErrInvalid is returned for a token that is unknown, expired or revoked. It
// says nothing about which, so a caller probing cannot tell.
var ErrInvalid = errors.New("invalid token")

// Token is one stored credential. The secret itself is never stored.
type Token struct {
	ID      string    `json:"id"`
	Name    string    `json:"name"`
	Role    Role      `json:"role"`
	Kind    string    `json:"kind"`
	Hash    string    `json:"hash"`
	Created time.Time `json:"created"`
	// Expires is zero for a token that never does.
	Expires time.Time `json:"expires,omitzero"`
	// Parent is the static token that approved an OAuth token. Revoking the
	// parent revokes everything issued under it.
	Parent string `json:"parent,omitempty"`
	Client string `json:"client,omitempty"`
}

// Client is an OAuth client that registered itself.
type Client struct {
	ID           string    `json:"id"`
	Name         string    `json:"name"`
	RedirectURIs []string  `json:"redirect_uris"`
	Created      time.Time `json:"created"`
}

type fileData struct {
	Version int      `json:"version"`
	Tokens  []Token  `json:"tokens"`
	Clients []Client `json:"clients,omitempty"`
}

// maxClients bounds registrations: anyone who can reach the server may
// register a client, and a client can do nothing until an operator approves
// it, so the bound is only about the file not growing without limit.
const maxClients = 100

// Store keeps tokens and OAuth clients in one file.
//
// The file is re-read when it changes on disk, so `sds-mcp token revoke` run
// beside a serving process takes effect on the next request without a signal.
type Store struct {
	path string
	now  func() time.Time

	mu    sync.Mutex
	mtime time.Time
	data  fileData
}

// Open loads the store at path, creating it (and its directory, private to the
// owner) when there is none yet.
func Open(path string) (*Store, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("create %s: %w", filepath.Dir(path), err)
	}
	s := &Store{path: path, now: time.Now, data: fileData{Version: 1}}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.reloadLocked(); err != nil {
		return nil, err
	}
	return s, nil
}

func (s *Store) reloadLocked() error {
	fi, err := os.Stat(s.path)
	if errors.Is(err, os.ErrNotExist) {
		return s.saveLocked()
	}
	if err != nil {
		return fmt.Errorf("stat %s: %w", s.path, err)
	}
	if fi.ModTime().Equal(s.mtime) {
		return nil
	}
	raw, err := os.ReadFile(s.path)
	if err != nil {
		return fmt.Errorf("read %s: %w", s.path, err)
	}
	var d fileData
	if err := json.Unmarshal(raw, &d); err != nil {
		return fmt.Errorf("%s is not a token store: %w", s.path, err)
	}
	s.data, s.mtime = d, fi.ModTime()
	return nil
}

func (s *Store) saveLocked() error {
	s.pruneLocked()
	raw, err := json.MarshalIndent(s.data, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(s.path), ".tokens-*")
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(tmp.Name()) }()
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return err
	}
	if _, err := tmp.Write(raw); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmp.Name(), s.path); err != nil {
		return err
	}
	if fi, err := os.Stat(s.path); err == nil {
		s.mtime = fi.ModTime()
	}
	return nil
}

// pruneLocked drops what has expired, and OAuth tokens whose parent is gone.
func (s *Store) pruneLocked() {
	now := s.now()
	live := map[string]bool{}
	for _, t := range s.data.Tokens {
		if t.Kind == KindStatic && (t.Expires.IsZero() || t.Expires.After(now)) {
			live[t.ID] = true
		}
	}
	kept := s.data.Tokens[:0]
	for _, t := range s.data.Tokens {
		if !t.Expires.IsZero() && !t.Expires.After(now) {
			continue
		}
		if t.Kind != KindStatic && !live[t.Parent] {
			continue
		}
		kept = append(kept, t)
	}
	s.data.Tokens = kept
}

func newSecret() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return "sdsmcp_" + base64.RawURLEncoding.EncodeToString(b), nil
}

func hashOf(secret string) string {
	sum := sha256.Sum256([]byte(secret))
	return hex.EncodeToString(sum[:])
}

func newID(prefix string) (string, error) {
	b := make([]byte, 6)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return prefix + hex.EncodeToString(b), nil
}

// Create makes a static token. ttl zero means it does not expire. The secret
// is returned once and is not recoverable afterwards.
func (s *Store) Create(name string, role Role, ttl time.Duration) (string, Token, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return "", Token{}, errors.New("a token needs a name")
	}
	if role.Rank() == 0 {
		return "", Token{}, fmt.Errorf("unknown role %q", role)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.reloadLocked(); err != nil {
		return "", Token{}, err
	}
	for _, t := range s.data.Tokens {
		if t.Kind == KindStatic && t.Name == name {
			return "", Token{}, fmt.Errorf("a token named %q already exists; revoke it or pick another name", name)
		}
	}
	secret, err := newSecret()
	if err != nil {
		return "", Token{}, err
	}
	id, err := newID("t_")
	if err != nil {
		return "", Token{}, err
	}
	t := Token{ID: id, Name: name, Role: role, Kind: KindStatic, Hash: hashOf(secret), Created: s.now().UTC()}
	if ttl > 0 {
		t.Expires = t.Created.Add(ttl)
	}
	s.data.Tokens = append(s.data.Tokens, t)
	if err := s.saveLocked(); err != nil {
		return "", Token{}, err
	}
	return secret, t, nil
}

// Verify finds the live access credential a secret belongs to: a static token,
// or an OAuth access token whose approving token still stands. A refresh token
// is not one.
func (s *Store) Verify(secret string) (Token, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.reloadLocked(); err != nil {
		return Token{}, err
	}
	t, ok := s.findLocked(hashOf(secret))
	if !ok || t.Kind == KindRefresh {
		return Token{}, ErrInvalid
	}
	return t, nil
}

// findLocked returns the live token with the hash: not expired, and for an
// OAuth token, with its parent still live.
func (s *Store) findLocked(hash string) (Token, bool) {
	now := s.now()
	var found *Token
	live := map[string]bool{}
	for i := range s.data.Tokens {
		t := &s.data.Tokens[i]
		if t.Kind == KindStatic && (t.Expires.IsZero() || t.Expires.After(now)) {
			live[t.ID] = true
		}
		if t.Hash == hash {
			found = t
		}
	}
	if found == nil {
		return Token{}, false
	}
	if !found.Expires.IsZero() && !found.Expires.After(now) {
		return Token{}, false
	}
	if found.Kind != KindStatic && !live[found.Parent] {
		return Token{}, false
	}
	return *found, true
}

// Revoke removes the static token with that ID or name, and everything issued
// under it. It reports how many records went.
func (s *Store) Revoke(ref string) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.reloadLocked(); err != nil {
		return 0, err
	}
	var id string
	for _, t := range s.data.Tokens {
		if t.Kind == KindStatic && (t.ID == ref || t.Name == ref) {
			id = t.ID
		}
	}
	if id == "" {
		return 0, fmt.Errorf("no token named or numbered %q", ref)
	}
	kept := s.data.Tokens[:0]
	removed := 0
	for _, t := range s.data.Tokens {
		if t.ID == id || t.Parent == id {
			removed++
			continue
		}
		kept = append(kept, t)
	}
	s.data.Tokens = kept
	return removed, s.saveLocked()
}

// List returns the static tokens, oldest first.
func (s *Store) List() ([]Token, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.reloadLocked(); err != nil {
		return nil, err
	}
	var out []Token
	for _, t := range s.data.Tokens {
		if t.Kind == KindStatic {
			out = append(out, t)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Created.Before(out[j].Created) })
	return out, nil
}

// RegisterClient records an OAuth client. Past maxClients the oldest goes.
func (s *Store) RegisterClient(name string, redirects []string) (Client, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.reloadLocked(); err != nil {
		return Client{}, err
	}
	id, err := newID("c_")
	if err != nil {
		return Client{}, err
	}
	c := Client{ID: id, Name: name, RedirectURIs: redirects, Created: s.now().UTC()}
	s.data.Clients = append(s.data.Clients, c)
	if len(s.data.Clients) > maxClients {
		s.data.Clients = s.data.Clients[len(s.data.Clients)-maxClients:]
	}
	return c, s.saveLocked()
}

// Client looks a registered client up.
func (s *Store) Client(id string) (Client, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.reloadLocked(); err != nil {
		return Client{}, false
	}
	for _, c := range s.data.Clients {
		if c.ID == id {
			return c, true
		}
	}
	return Client{}, false
}

// IssueOAuth gives a client an access token and a refresh token under the
// static token that approved it, replacing any it already held under that
// approval so the file does not grow with every refresh.
func (s *Store) IssueOAuth(parent Token, client Client, role Role, accessTTL, refreshTTL time.Duration) (access, refresh string, exp time.Time, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.reloadLocked(); err != nil {
		return "", "", time.Time{}, err
	}
	return s.issueLocked(parent.ID, parent.Name, client, role, accessTTL, refreshTTL)
}

func (s *Store) issueLocked(parentID, parentName string, client Client, role Role, accessTTL, refreshTTL time.Duration) (string, string, time.Time, error) {
	kept := s.data.Tokens[:0]
	for _, t := range s.data.Tokens {
		if t.Kind != KindStatic && t.Parent == parentID && t.Client == client.ID {
			continue
		}
		kept = append(kept, t)
	}
	s.data.Tokens = kept

	access, err := newSecret()
	if err != nil {
		return "", "", time.Time{}, err
	}
	refresh, err := newSecret()
	if err != nil {
		return "", "", time.Time{}, err
	}
	now := s.now().UTC()
	exp := now.Add(accessTTL)
	name := "oauth:" + client.Name + " (approved by " + parentName + ")"
	for _, t := range []struct {
		kind, secret string
		ttl          time.Duration
	}{{KindAccess, access, accessTTL}, {KindRefresh, refresh, refreshTTL}} {
		id, err := newID("o_")
		if err != nil {
			return "", "", time.Time{}, err
		}
		s.data.Tokens = append(s.data.Tokens, Token{
			ID: id, Name: name, Role: role, Kind: t.kind, Hash: hashOf(t.secret),
			Created: now, Expires: now.Add(t.ttl), Parent: parentID, Client: client.ID,
		})
	}
	return access, refresh, exp, s.saveLocked()
}

// Refresh exchanges a refresh token for a new pair. The old refresh token is
// spent: presenting it again fails.
func (s *Store) Refresh(refreshSecret, clientID string, accessTTL, refreshTTL time.Duration) (access, refresh string, exp time.Time, role Role, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.reloadLocked(); err != nil {
		return "", "", time.Time{}, "", err
	}
	t, ok := s.findLocked(hashOf(refreshSecret))
	if !ok || t.Kind != KindRefresh || t.Client != clientID {
		return "", "", time.Time{}, "", ErrInvalid
	}
	var client Client
	for _, c := range s.data.Clients {
		if c.ID == clientID {
			client = c
		}
	}
	if client.ID == "" {
		return "", "", time.Time{}, "", ErrInvalid
	}
	parentName := t.Parent
	for _, p := range s.data.Tokens {
		if p.ID == t.Parent {
			parentName = p.Name
		}
	}
	access, refresh, exp, err = s.issueLocked(t.Parent, parentName, client, t.Role, accessTTL, refreshTTL)
	return access, refresh, exp, t.Role, err
}
