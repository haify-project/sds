package controller

import (
	"context"
	"testing"

	"go.uber.org/zap"
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
	invokeUnary(log, false, "/v1.SDSController/CreatePool", nameReq{name: "data"}, nil)

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
	invokeUnary(log, false, "/v1.SDSController/ListPools", nameReq{}, nil)
	if n := logs.Len(); n != 0 {
		t.Fatalf("expected read-only call to be skipped, got %d entries", n)
	}
}

func TestAuditIncludesReadsWhenConfigured(t *testing.T) {
	log, logs := newObservedLogger()
	invokeUnary(log, true, "/v1.SDSController/ListPools", nameReq{}, nil)
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
	invokeUnary(log, false, "/v1.SDSController/DeletePool", nameReq{name: "data"}, denied)

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
