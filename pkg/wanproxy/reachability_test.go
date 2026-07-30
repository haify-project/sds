package wanproxy

import (
	"context"
	"strings"
	"testing"
	"time"
)

// fastReach shrinks the retry window so failure-path tests don't sleep for
// seconds. It restores the previous values on cleanup.
func fastReach(t *testing.T, attempts int) {
	t.Helper()
	pa, pd := reachAttempts, reachRetryDelay
	reachAttempts, reachRetryDelay = attempts, 0
	t.Cleanup(func() { reachAttempts, reachRetryDelay = pa, pd })
}

func TestVerifyReachabilitySuccess(t *testing.T) {
	fastReach(t, 3)
	spec := sampleSpec()
	f := &fakeDeploy{}

	if err := VerifyReachability(context.Background(), f, spec); err != nil {
		t.Fatalf("VerifyReachability: %v", err)
	}
	// Exactly one probe on the primary, targeting the DR endpoint.
	if len(f.events) != 1 {
		t.Fatalf("want 1 probe event, got %d", len(f.events))
	}
	e := f.events[0]
	if e.kind != "exec" || !sameHosts(e.hosts, []string{spec.PrimaryNodeAddr}) {
		t.Fatalf("probe ran on wrong hosts: %+v", e)
	}
	if !strings.Contains(e.cmd, spec.DRPublicEndpoint) || !strings.Contains(e.cmd, "/dev/tcp/") {
		t.Fatalf("probe command does not test the DR endpoint over TCP: %q", e.cmd)
	}
}

func TestVerifyReachabilityRetriesThenFails(t *testing.T) {
	fastReach(t, 3)
	spec := sampleSpec()
	// The probe command always fails (blocked port).
	f := &fakeDeploy{failExecSubstr: "/dev/tcp/"}

	err := VerifyReachability(context.Background(), f, spec)
	if err == nil {
		t.Fatal("expected VerifyReachability to fail when the port is blocked")
	}
	// It must retry the full budget.
	if len(f.events) != 3 {
		t.Fatalf("want 3 probe attempts, got %d", len(f.events))
	}
	// The error must name the port and point at the firewall/security group.
	for _, want := range []string{"not reachable", "security group", "37901"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("error %q missing %q", err.Error(), want)
		}
	}
}

func TestVerifyReachabilityHonorsContextCancel(t *testing.T) {
	// A non-zero delay + already-cancelled context: the loop must abort in the
	// wait rather than burning all attempts.
	pa, pd := reachAttempts, reachRetryDelay
	reachAttempts, reachRetryDelay = 5, time.Hour
	t.Cleanup(func() { reachAttempts, reachRetryDelay = pa, pd })

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	f := &fakeDeploy{failExecSubstr: "/dev/tcp/"}

	if err := VerifyReachability(ctx, f, sampleSpec()); err == nil {
		t.Fatal("expected an error when the context is cancelled")
	}
	// One attempt, then the cancelled wait breaks out.
	if len(f.events) != 1 {
		t.Fatalf("want 1 attempt before cancel, got %d", len(f.events))
	}
}

func TestProvisionFailsWhenDRUnreachable(t *testing.T) {
	spec := newSpecWithTempPKI(t)
	fastReach(t, 2)
	// Everything provisions fine, but the final reachability probe fails.
	f := &fakeDeploy{failExecSubstr: "/dev/tcp/"}

	err := Provision(context.Background(), f, spec)
	if err == nil {
		t.Fatal("expected Provision to fail the reachability preflight")
	}
	if !strings.Contains(err.Error(), "not reachable") {
		t.Fatalf("error %q should report the unreachable DR endpoint", err.Error())
	}
}

func TestProvisionSkipsReachabilityWhenRequested(t *testing.T) {
	spec := newSpecWithTempPKI(t)
	spec.SkipReachabilityCheck = true
	// Even if a probe would fail, it must never run.
	f := &fakeDeploy{failExecSubstr: "/dev/tcp/"}

	if err := Provision(context.Background(), f, spec); err != nil {
		t.Fatalf("Provision with SkipReachabilityCheck: %v", err)
	}
	for _, e := range f.events {
		if strings.Contains(e.cmd, "/dev/tcp/") {
			t.Fatalf("reachability probe ran despite SkipReachabilityCheck: %q", e.cmd)
		}
	}
}

func TestStatusReportsHealth(t *testing.T) {
	spec := sampleSpec()
	f := &fakeDeploy{}

	st, err := Status(context.Background(), f, spec)
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if st.Resource != spec.Resource {
		t.Fatalf("resource = %q, want %q", st.Resource, spec.Resource)
	}
	if !st.Primary.Active || !st.DR.Active || !st.WANReachable {
		t.Fatalf("expected a healthy status, got %+v", st)
	}
	if !st.Healthy() {
		t.Fatal("Healthy() should be true when both active and reachable")
	}
	if st.Primary.Host != spec.PrimaryNodeAddr || st.DR.Host != spec.DRNodeAddr {
		t.Fatalf("status hosts wrong: %+v", st)
	}
}

func TestStatusReportsUnreachableAndInactive(t *testing.T) {
	spec := sampleSpec()
	// Both the is-active check and the reachability probe fail.
	f := &fakeDeploy{failExecSubstr: ""}
	f.failAll = true

	st, err := Status(context.Background(), f, spec)
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if st.Primary.Active || st.DR.Active || st.WANReachable {
		t.Fatalf("expected an unhealthy status, got %+v", st)
	}
	if st.Healthy() {
		t.Fatal("Healthy() must be false when nothing is up")
	}
}

