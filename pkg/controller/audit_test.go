package controller

import (
	"context"
	"testing"

	"go.uber.org/zap"

	haifypb "github.com/haify-project/haify/api/proto/v1"
	"go.uber.org/zap/zaptest/observer"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// nameReq is a stand-in for a generated request message carrying a target name.
type nameReq struct{ name string }

func (r nameReq) GetName() string { return r.name }

func newObservedLogger() (*zap.Logger, *observer.ObservedLogs) {
	core, logs := observer.New(zap.InfoLevel)
	return zap.New(core), logs
}

func invokeUnary(log *zap.Logger, includeReads bool, fullMethod string, req interface{}, err error) {
	interceptor := auditUnaryInterceptor(log, includeReads, nil, nil)
	handler := func(ctx context.Context, req interface{}) (interface{}, error) {
		return struct{}{}, err
	}
	_, _ = interceptor(context.Background(), req,
		&grpc.UnaryServerInfo{FullMethod: fullMethod}, handler)
}

func TestAuditRecordsMutatingCall(t *testing.T) {
	log, logs := newObservedLogger()
	invokeUnary(log, false, "/v1.HaifyController/CreatePool", nameReq{name: "data"}, nil)

	entries := logs.All()
	if len(entries) != 1 {
		t.Fatalf("expected 1 audit entry, got %d", len(entries))
	}
	m := entries[0].ContextMap()
	if m["method"] != "CreatePool" {
		t.Errorf("method = %v, want CreatePool", m["method"])
	}
	if m["target"] != "data" {
		t.Errorf("target = %v, want data", m["target"])
	}
	if m["result"] != codes.OK.String() {
		t.Errorf("result = %v, want OK", m["result"])
	}
	if m["granted"] != true {
		t.Errorf("granted = %v, want true", m["granted"])
	}
}

func TestAuditSkipsReadsByDefault(t *testing.T) {
	log, logs := newObservedLogger()
	invokeUnary(log, false, "/v1.HaifyController/ListPools", nameReq{}, nil)
	if n := logs.Len(); n != 0 {
		t.Fatalf("expected read-only call to be skipped, got %d entries", n)
	}
}

// The web UI polls ResourceStatus; reads without a read verb in their name
// must stay out of the trail too, or they bury every real change.
func TestAuditSkipsReadsWithoutAReadVerb(t *testing.T) {
	for _, m := range []string{"ResourceStatus", "GetAppStatus", "HealthCheck", "PlanRebalance", "CollectNodeDiagnostics"} {
		log, logs := newObservedLogger()
		invokeUnary(log, false, "/v1.HaifyController/"+m, nameReq{}, nil)
		if n := logs.Len(); n != 0 {
			t.Errorf("%s: expected read to be skipped, got %d entries", m, n)
		}
	}
	for _, m := range []string{"SetPrimary", "RunInspection", "RepairResource"} {
		log, logs := newObservedLogger()
		invokeUnary(log, false, "/v1.HaifyController/"+m, nameReq{}, nil)
		if n := logs.Len(); n != 1 {
			t.Errorf("%s: expected a change to be audited, got %d entries", m, n)
		}
	}
}

func TestAuditIncludesReadsWhenConfigured(t *testing.T) {
	log, logs := newObservedLogger()
	invokeUnary(log, true, "/v1.HaifyController/ListPools", nameReq{}, nil)
	if n := logs.Len(); n != 1 {
		t.Fatalf("expected read-only call to be audited, got %d entries", n)
	}
}

func TestAuditSkipsHealthChecks(t *testing.T) {
	log, logs := newObservedLogger()
	invokeUnary(log, true, "/grpc.health.v1.Health/Check", nameReq{}, nil)
	if n := logs.Len(); n != 0 {
		t.Fatalf("expected health check to be skipped, got %d entries", n)
	}
}

func TestAuditRecordsDeniedAttempt(t *testing.T) {
	log, logs := newObservedLogger()
	denied := status.Error(codes.Unauthenticated, "no token")
	invokeUnary(log, false, "/v1.HaifyController/DeletePool", nameReq{name: "data"}, denied)

	entries := logs.All()
	if len(entries) != 1 {
		t.Fatalf("expected 1 audit entry, got %d", len(entries))
	}
	m := entries[0].ContextMap()
	if m["result"] != codes.Unauthenticated.String() {
		t.Errorf("result = %v, want Unauthenticated", m["result"])
	}
	if m["granted"] != false {
		t.Errorf("granted = %v, want false", m["granted"])
	}
	if m["error"] != "no token" {
		t.Errorf("error = %v, want 'no token'", m["error"])
	}
}

// A handler that reports failure in its response completes the RPC as gRPC OK.
// The trail must still say it failed, and why.
func TestAuditRecordsAFailureReportedInTheResponse(t *testing.T) {
	log, logs := newObservedLogger()
	interceptor := auditUnaryInterceptor(log, false, nil, nil)
	handler := func(ctx context.Context, req interface{}) (interface{}, error) {
		return &haifypb.AddVolumeResponse{Success: false, Message: "insufficient free space"}, nil
	}
	_, _ = interceptor(context.Background(), nameReq{name: "r5"},
		&grpc.UnaryServerInfo{FullMethod: "/v1.HaifyController/AddVolume"}, handler)

	m := logs.All()[0].ContextMap()
	if m["result"] != "FAILED" || m["error"] != "insufficient free space" {
		t.Errorf("result=%v error=%v, want FAILED with the handler's message", m["result"], m["error"])
	}
	if m["granted"] != true {
		t.Errorf("a failed operation was still permitted: granted = %v", m["granted"])
	}

	logs.TakeAll()
	ok := func(ctx context.Context, req interface{}) (interface{}, error) {
		return &haifypb.AddVolumeResponse{Success: true}, nil
	}
	_, _ = interceptor(context.Background(), nameReq{name: "r5"},
		&grpc.UnaryServerInfo{FullMethod: "/v1.HaifyController/AddVolume"}, ok)
	if r := logs.All()[0].ContextMap()["result"]; r != codes.OK.String() {
		t.Errorf("a successful response was recorded as %v", r)
	}
}
