package controller

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	"go.uber.org/zap"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"

	"github.com/haify-project/sds/pkg/config"
	"github.com/haify-project/sds/pkg/database"
	"github.com/haify-project/sds/pkg/event"
)

// Two-person approval ([rbac.approval]).
//
// RBAC decides what a role may do, and an admin may do everything — so one
// stolen admin token could delete the backup target, the backups and the
// snapshots, and nothing in sds would object. With approval on, each call on
// the list runs only after a second user approved that exact call:
//
//  1. Alice makes the call. It fails with FAILED_PRECONDITION, naming request
//     <id>, which is now pending and raises an approval.requested event.
//  2. Bob, a different user with the approve right (admin, security-officer),
//     reads the request's arguments and runs `sds approval approve <id>`.
//  3. Alice repeats the identical call within the TTL. It runs, once.
//
// "Identical" is the method plus the deterministic encoding of its arguments,
// so an approval for deleting one backup cannot be spent deleting another.
// Creating users and changing roles are on the default list: otherwise the
// stolen token would simply mint its own second approver.

// approvalGate holds the requests and decides whether a call may run.
type approvalGate struct {
	db      *database.DB
	methods map[string]bool
	ttl     time.Duration
	events  *event.Bus
	log     *zap.Logger
	now     func() time.Time

	// mu makes check-then-consume atomic, so one approval runs one call even
	// when the same call arrives twice at once.
	mu sync.Mutex
}

func newApprovalGate(cfg config.ApprovalConfig, db *database.DB, events *event.Bus, log *zap.Logger) *approvalGate {
	if !cfg.Enabled || db == nil {
		return nil
	}
	methods := map[string]bool{}
	for _, m := range cfg.ApprovalMethods() {
		methods[strings.TrimSpace(m)] = true
	}
	ttl := time.Duration(cfg.TTLMinutes) * time.Minute
	if ttl <= 0 {
		ttl = time.Hour
	}
	return &approvalGate{db: db, methods: methods, ttl: ttl, events: events, log: log, now: time.Now}
}

// requestDigest identifies one call: its method and exact arguments.
func requestDigest(method string, req proto.Message) (string, error) {
	b, err := proto.MarshalOptions{Deterministic: true}.Marshal(req)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(append([]byte(method+"\x00"), b...))
	return hex.EncodeToString(sum[:]), nil
}

// maskedRequest renders the arguments for the approver with every field that
// looks like a credential replaced: the request list is readable by anyone
// who may read the cluster.
func maskedRequest(req proto.Message) string {
	b, err := protojson.MarshalOptions{UseProtoNames: true}.Marshal(req)
	if err != nil {
		return "{}"
	}
	var m any
	if json.Unmarshal(b, &m) != nil {
		return "{}"
	}
	maskSecrets(m)
	out, _ := json.Marshal(m)
	return string(out)
}

func maskSecrets(v any) {
	switch t := v.(type) {
	case map[string]any:
		for k, val := range t {
			lk := strings.ToLower(k)
			if strings.Contains(lk, "secret") || strings.Contains(lk, "password") || strings.Contains(lk, "token") ||
				strings.Contains(lk, "passphrase") || strings.HasSuffix(lk, "key") {
				t[k] = "***"
				continue
			}
			maskSecrets(val)
		}
	case []any:
		for _, val := range t {
			maskSecrets(val)
		}
	}
}

// check lets method run for user when an approval for exactly this call is
// waiting, and consumes it. Otherwise it records (or finds) a pending request
// and returns the FAILED_PRECONDITION error that names it.
func (g *approvalGate) check(ctx context.Context, method, user string, req proto.Message) error {
	if g == nil || !g.methods[method] {
		return nil
	}
	if user == "" {
		return status.Error(codes.PermissionDenied, method+" needs a second person's approval, and the caller has no identity")
	}
	digest, err := requestDigest(method, req)
	if err != nil {
		return status.Errorf(codes.Internal, "approval: %v", err)
	}

	g.mu.Lock()
	defer g.mu.Unlock()
	now := g.now()
	all, err := g.db.ListApprovals(ctx)
	if err != nil {
		return status.Errorf(codes.Internal, "approval: %v", err)
	}
	var pending *database.Approval
	for _, a := range all {
		if a.Digest != digest || a.Requester != user {
			continue
		}
		switch a.EffectiveState(now) {
		case database.ApprovalApproved:
			a.State = database.ApprovalUsed
			if err := g.db.SaveApproval(ctx, a); err != nil {
				return status.Errorf(codes.Internal, "approval: %v", err)
			}
			g.log.Info("Running an approved call", zap.String("method", method), zap.String("id", a.ID),
				zap.String("requester", user), zap.String("approver", a.DecidedBy))
			return nil
		case database.ApprovalPending:
			pending = a
		}
	}
	if pending == nil {
		pending = &database.Approval{
			ID: newApprovalID(), Method: method, Digest: digest, Request: maskedRequest(req),
			Requester: user, CreatedAt: now, ExpiresAt: now.Add(g.ttl), State: database.ApprovalPending,
		}
		if err := g.db.SaveApproval(ctx, pending); err != nil {
			return status.Errorf(codes.Internal, "approval: %v", err)
		}
		_ = g.db.PruneApprovals(ctx, now.AddDate(0, 0, -30))
		if g.events != nil {
			g.events.Publish(event.Event{
				Type: event.TypeApprovalRequested, Severity: event.SeverityWarning, Status: event.StatusInfo,
				Message: fmt.Sprintf("%s asks to run %s %s; approve with `sds approval approve %s`",
					user, method, pending.Request, pending.ID),
				Details: map[string]string{"id": pending.ID, "method": method, "requester": user},
			})
		}
	}
	return status.Errorf(codes.FailedPrecondition,
		"%s needs a second person's approval: request %s is pending until %s. Another user with the approve right "+
			"runs `sds approval approve %s`; then repeat this exact call",
		method, pending.ID, pending.ExpiresAt.UTC().Format(time.RFC3339), pending.ID)
}

// decide approves or rejects request id as user.
func (g *approvalGate) decide(ctx context.Context, id, user string, approve bool) (*database.Approval, error) {
	if g == nil {
		return nil, fmt.Errorf("two-person approval is not enabled ([rbac.approval])")
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	a, err := g.db.GetApproval(ctx, id)
	if err != nil {
		return nil, err
	}
	now := g.now()
	if st := a.EffectiveState(now); st != database.ApprovalPending {
		return a, fmt.Errorf("request %s is %s, not pending", id, st)
	}
	if approve && a.Requester == user {
		return a, fmt.Errorf("request %s was made by %s, and a request cannot be approved by the user who made it", id, user)
	}
	a.DecidedBy, a.DecidedAt = user, now
	if approve {
		a.State = database.ApprovalApproved
		// The requester gets a full TTL to make the call.
		a.ExpiresAt = now.Add(g.ttl)
	} else {
		a.State = database.ApprovalRejected
	}
	return a, g.db.SaveApproval(ctx, a)
}

func newApprovalID() string {
	b := make([]byte, 6)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// approvalUnaryInterceptor runs after the RBAC interceptors: the caller is
// known and allowed the call; the gate decides whether it also needs a
// second person.
func approvalUnaryInterceptor(g *approvalGate) grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req interface{}, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (interface{}, error) {
		if msg, ok := req.(proto.Message); ok {
			if err := g.check(ctx, shortMethod(info.FullMethod), userFromContext(ctx), msg); err != nil {
				return nil, err
			}
		}
		return handler(ctx, req)
	}
}
