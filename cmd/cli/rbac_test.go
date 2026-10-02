package main

import "testing"

// `rbac user add` must not shadow the global --token: the caller's admin
// token and the new user's token are different things.
func TestRbacUserAddDoesNotShadowGlobalToken(t *testing.T) {
	cmd := rbacUserAddCommand()
	if cmd.Flags().Lookup("token") != nil {
		t.Fatal("rbac user add defines a local --token, shadowing the caller's --token")
	}
	if cmd.Flags().Lookup("user-token") == nil {
		t.Fatal("rbac user add has no --user-token")
	}
}
