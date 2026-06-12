package controller

import (
	"context"
	"strings"

	"github.com/liliang-cn/sds/pkg/config"
	"github.com/liliang-cn/sds/pkg/rbac"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

// toRBACUsers/toRBACPolicies adapt the config-declared RBAC settings into the
// rbac package's own types, keeping that package free of a config dependency.
func toRBACUsers(in []config.RBACUser) []rbac.User {
	out := make([]rbac.User, len(in))
	for i, u := range in {
		out[i] = rbac.User{Name: u.Name, Token: u.Token, Role: u.Role}
	}
	return out
}

func toRBACPolicies(in []config.RBACPolicy) []rbac.Policy {
	out := make([]rbac.Policy, len(in))
	for i, p := range in {
		out[i] = rbac.Policy{Role: p.Role, Object: p.Object, Action: p.Action}
	}
	return out
}

// RBAC request flow (when [rbac] is enabled): the identity interceptor maps the
// bearer token to a user and rejects unknown tokens; the authz interceptor then
// classifies the RPC into an (object, action) pair and asks Casbin whether the
// user's role permits it. Both sit after the audit interceptor so denials are
// recorded. Health checks stay open.

type ctxUserKey struct{}

// bearerToken extracts the raw token from request metadata, accepting both the
// native "authorization" header and the grpc-gateway-prefixed variant.
func bearerToken(ctx context.Context) string {
	md, ok := metadata.FromIncomingContext(ctx)
	if !ok {
		return ""
	}
	values := md.Get("authorization")
	if len(values) == 0 {
		values = md.Get("grpcgateway-authorization")
	}
	for _, v := range values {
		token := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(v), "Bearer"))
		if token != "" {
			return token
		}
	}
	return ""
}

// userFromContext returns the authenticated user name, if any.
func userFromContext(ctx context.Context) string {
	name, _ := ctx.Value(ctxUserKey{}).(string)
	return name
}

var errPermissionDenied = status.Error(codes.PermissionDenied,
	"your role is not permitted to perform this operation")

// rbacIdentityUnaryInterceptor resolves the caller from its bearer token.
func rbacIdentityUnaryInterceptor(engine *rbac.Engine) grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req interface{}, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (interface{}, error) {
		if strings.HasPrefix(info.FullMethod, healthServicePrefix) {
			return handler(ctx, req)
		}
		name, ok := engine.ResolveUser(bearerToken(ctx))
		if !ok {
			return nil, errUnauthenticated
		}
		return handler(context.WithValue(ctx, ctxUserKey{}, name), req)
	}
}

// rbacAuthzUnaryInterceptor enforces the caller's role against the RPC.
func rbacAuthzUnaryInterceptor(engine *rbac.Engine) grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req interface{}, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (interface{}, error) {
		if strings.HasPrefix(info.FullMethod, healthServicePrefix) {
			return handler(ctx, req)
		}
		object, action := rbac.Classify(info.FullMethod)
		allowed, err := engine.Enforce(userFromContext(ctx), object, action)
		if err != nil {
			return nil, status.Errorf(codes.Internal, "authorization check failed: %v", err)
		}
		if !allowed {
			return nil, errPermissionDenied
		}
		return handler(ctx, req)
	}
}

// rbacIdentityStreamInterceptor resolves the caller for streaming RPCs.
func rbacIdentityStreamInterceptor(engine *rbac.Engine) grpc.StreamServerInterceptor {
	return func(srv interface{}, ss grpc.ServerStream, info *grpc.StreamServerInfo, handler grpc.StreamHandler) error {
		if strings.HasPrefix(info.FullMethod, healthServicePrefix) {
			return handler(srv, ss)
		}
		name, ok := engine.ResolveUser(bearerToken(ss.Context()))
		if !ok {
			return errUnauthenticated
		}
		wrapped := &wrappedServerStream{ServerStream: ss,
			ctx: context.WithValue(ss.Context(), ctxUserKey{}, name)}
		return handler(srv, wrapped)
	}
}

// rbacAuthzStreamInterceptor enforces the role for streaming RPCs.
func rbacAuthzStreamInterceptor(engine *rbac.Engine) grpc.StreamServerInterceptor {
	return func(srv interface{}, ss grpc.ServerStream, info *grpc.StreamServerInfo, handler grpc.StreamHandler) error {
		if strings.HasPrefix(info.FullMethod, healthServicePrefix) {
			return handler(srv, ss)
		}
		object, action := rbac.Classify(info.FullMethod)
		allowed, err := engine.Enforce(userFromContext(ss.Context()), object, action)
		if err != nil {
			return status.Errorf(codes.Internal, "authorization check failed: %v", err)
		}
		if !allowed {
			return errPermissionDenied
		}
		return handler(srv, ss)
	}
}

// wrappedServerStream lets a stream interceptor replace the context seen by
// downstream handlers (grpc.ServerStream.Context is read-only otherwise).
type wrappedServerStream struct {
	grpc.ServerStream
	ctx context.Context
}

func (w *wrappedServerStream) Context() context.Context { return w.ctx }
