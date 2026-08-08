package rbac

import "testing"

func TestClassify(t *testing.T) {
	cases := []struct {
		method string
		object string
		action string
	}{
		{"/v1.SDSController/CreatePool", "pool", ActWrite},
		{"/v1.SDSController/ListPools", "pool", ActRead},
		{"/v1.SDSController/AddDiskToPool", "pool", ActWrite},
		{"/v1.SDSController/CreateZFSSnapshot", "snapshot", ActWrite},
		{"/v1.SDSController/ListSnapshots", "snapshot", ActRead},
		{"/v1.SDSController/CreateNFSGateway", "gateway", ActWrite},
		{"/v1.SDSController/AddNVMeNamespace", "gateway", ActWrite},
		{"/v1.SDSController/ListGateways", "gateway", ActRead},
		{"/v1.SDSController/EnableSelfHa", "ha", ActWrite},
		{"/v1.SDSController/GetSelfHaStatus", "ha", ActRead},
		{"/v1.SDSController/RegisterNode", "node", ActWrite},
		{"/v1.SDSController/ListNodes", "node", ActRead},
		{"/v1.SDSController/CreateResource", "resource", ActWrite},
		{"/v1.SDSController/ResourceStatus", "resource", ActRead},
		{"/v1.SDSController/SetPrimary", "resource", ActWrite},
		// Off-cluster backups must not fall through to "system", where an
		// operator has read only: the feature would then be unusable by
		// anyone but admin, and nothing would say so.
		{"/v1.SDSController/CreateBackup", "backup", ActWrite},
		{"/v1.SDSController/RestoreBackup", "backup", ActWrite},
		{"/v1.SDSController/AddBackupTarget", "backup", ActWrite},
		{"/v1.SDSController/ListBackups", "backup", ActRead},
		{"/v1.SDSController/ListBackupTargets", "backup", ActRead},
	}
	for _, c := range cases {
		obj, act := Classify(c.method)
		if obj != c.object || act != c.action {
			t.Errorf("Classify(%s) = (%s, %s), want (%s, %s)",
				c.method, obj, act, c.object, c.action)
		}
	}
}

func newTestEngine(t *testing.T) *Engine {
	t.Helper()
	users := []User{
		{Name: "alice", Token: "alice-token-0123456789", Role: "admin"},
		{Name: "olivia", Token: "olivia-token-0123456789", Role: "operator"},
		{Name: "victor", Token: "victor-token-0123456789", Role: "viewer"},
	}
	e, err := New(nil, users, nil)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return e
}

func TestEnforceRoles(t *testing.T) {
	e := newTestEngine(t)
	cases := []struct {
		user, object, action string
		want                 bool
	}{
		// admin can do anything
		{"alice", "pool", ActWrite, true},
		{"alice", "system", ActWrite, true},
		// operator manages storage but not system writes
		{"olivia", "gateway", ActWrite, true},
		{"olivia", "snapshot", ActWrite, true},
		{"olivia", "system", ActWrite, false},
		{"olivia", "node", ActWrite, false},
		{"olivia", "node", ActRead, true},
		// viewer is read-only
		{"victor", "pool", ActRead, true},
		{"victor", "pool", ActWrite, false},
		{"victor", "gateway", ActWrite, false},
		// unknown subject is denied
		{"nobody", "pool", ActRead, false},
	}
	for _, c := range cases {
		got, err := e.Enforce(c.user, c.object, c.action)
		if err != nil {
			t.Fatalf("Enforce(%s,%s,%s): %v", c.user, c.object, c.action, err)
		}
		if got != c.want {
			t.Errorf("Enforce(%s, %s, %s) = %v, want %v",
				c.user, c.object, c.action, got, c.want)
		}
	}
}

func TestResolveUser(t *testing.T) {
	e := newTestEngine(t)
	if name, ok := e.ResolveUser("alice-token-0123456789"); !ok || name != "alice" {
		t.Errorf("ResolveUser(valid) = (%q, %v), want (alice, true)", name, ok)
	}
	if _, ok := e.ResolveUser("wrong-token"); ok {
		t.Errorf("ResolveUser(invalid) should fail")
	}
	if _, ok := e.ResolveUser(""); ok {
		t.Errorf("ResolveUser(empty) should fail")
	}
}

func TestCustomPolicyAndRole(t *testing.T) {
	users := []User{{Name: "dan", Token: "dan-token-0123456789xx", Role: "deployer"}}
	policies := []Policy{
		{Role: "deployer", Object: "gateway", Action: ActWrite},
		{Role: "deployer", Object: "gateway", Action: ActRead},
	}
	e, err := New(nil, users, policies)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if ok, _ := e.Enforce("dan", "gateway", ActWrite); !ok {
		t.Errorf("custom role should allow gateway write")
	}
	if ok, _ := e.Enforce("dan", "pool", ActWrite); ok {
		t.Errorf("custom role should not allow pool write")
	}
}

func TestInvalidInputs(t *testing.T) {
	if _, err := New(nil, []User{{Name: "", Token: "x", Role: "admin"}}, nil); err == nil {
		t.Errorf("expected error for user with empty name")
	}
	if _, err := New(nil, nil, []Policy{{Role: "admin", Object: "", Action: "read"}}); err == nil {
		t.Errorf("expected error for policy with empty object")
	}
}

// Notification channels carry a signing secret and decide who gets paged, so
// they are administrative configuration: readable by an operator, writable only
// by an admin. That is what the unclassified default already gives them, and
// this pins it — a later object rule that happened to match "Channel" would
// otherwise widen who can redirect a cluster's alerts without anyone noticing.
func TestNotifyChannelRPCsAreSystemScoped(t *testing.T) {
	for _, tc := range []struct {
		method string
		action string
	}{
		{"ListNotifyChannels", ActRead},
		{"SaveNotifyChannel", ActWrite},
		{"DeleteNotifyChannel", ActWrite},
		{"TestNotifyChannel", ActWrite},
	} {
		object, action := Classify("/v1.SDSController/" + tc.method)
		if object != "system" {
			t.Errorf("%s: object = %q, want system", tc.method, object)
		}
		if action != tc.action {
			t.Errorf("%s: action = %q, want %q", tc.method, action, tc.action)
		}
	}
}
