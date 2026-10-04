package controller

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"

	"github.com/grpc-ecosystem/grpc-gateway/v2/runtime"
	"go.uber.org/zap"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"

	pb "github.com/haify-project/sds/api/proto/v1"
	"github.com/haify-project/sds/pkg/database"
	"github.com/haify-project/sds/pkg/rbac"
)

// dbRBACStore persists the RBAC snapshot inside the controller's BBolt
// database, so runtime user/role changes survive restarts without any extra
// datastore.
type dbRBACStore struct{ db *database.DB }

func (s dbRBACStore) Load() (*rbac.Snapshot, error) {
	data, err := s.db.LoadRBAC(context.Background())
	if err != nil {
		return nil, err
	}
	if len(data) == 0 {
		return nil, nil
	}
	var snap rbac.Snapshot
	if err := json.Unmarshal(data, &snap); err != nil {
		return nil, err
	}
	return &snap, nil
}

func (s dbRBACStore) Save(snap *rbac.Snapshot) error {
	data, err := json.Marshal(snap)
	if err != nil {
		return err
	}
	return s.db.SaveRBAC(context.Background(), data)
}

// registerRBACRoutes adds read-only RBAC introspection endpoints to the REST
// gateway so the web UI and CLI can show "who am I / what can I do" and, for
// admins, the effective policy. They live on the gateway mux (port 3375), so
// the UI reaches them via its existing /api proxy. engine is nil when RBAC is
// disabled, in which case the endpoints report enabled=false.
func (c *Controller) registerRBACRoutes(mux *runtime.ServeMux, engine *rbac.Engine) {
	routes := []struct {
		method, path string
		h            runtime.HandlerFunc
	}{
		{"GET", "/v1/rbac/whoami", rbacWhoamiHandler(engine)},
		{"GET", "/v1/rbac/policies", rbacPoliciesHandler(engine)},
		{"GET", "/v1/rbac/roles", rbacRolesHandler(engine)},
		{"POST", "/v1/rbac/users", rbacCreateUserHandler(engine, c.approvals)},
		{"DELETE", "/v1/rbac/users/{name}", rbacDeleteUserHandler(engine, c.approvals)},
		{"PUT", "/v1/rbac/users/{name}/role", rbacSetRoleHandler(engine, c.approvals)},
	}
	for _, rt := range routes {
		if err := mux.HandlePath(rt.method, rt.path, rt.h); err != nil {
			c.logger.Error("failed to register RBAC route",
				zap.String("path", rt.path), zap.Error(err))
		}
	}
}

// adminCaller resolves the caller and ensures administrative rights, writing
// the appropriate error response and returning false if not.
func adminCaller(engine *rbac.Engine, w http.ResponseWriter, r *http.Request) (string, bool) {
	if engine == nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "RBAC is not enabled"})
		return "", false
	}
	user, _, ok := engine.Whoami(bearerFromHeader(r))
	if !ok {
		writeJSON(w, http.StatusUnauthorized, map[string]any{"error": "unknown or missing API token"})
		return "", false
	}
	if !engine.CanAdmin(user) {
		writeJSON(w, http.StatusForbidden, map[string]any{"error": "administrator role required"})
		return "", false
	}
	return user, true
}

// approvedHTTP holds back a user change that [rbac.approval] lists, as the
// gRPC interceptor does for the RPC of the same name: these routes call the
// RBAC engine directly, so without it they would be the way around approval.
func approvedHTTP(g *approvalGate, w http.ResponseWriter, r *http.Request, user, method string, req proto.Message) bool {
	if err := g.check(r.Context(), method, user, req); err != nil {
		writeJSON(w, http.StatusPreconditionFailed, map[string]any{"error": status.Convert(err).Message()})
		return false
	}
	return true
}

func writeJSON(w http.ResponseWriter, status int, body interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

func bearerFromHeader(r *http.Request) string {
	v := strings.TrimSpace(r.Header.Get("Authorization"))
	return strings.TrimSpace(strings.TrimPrefix(v, "Bearer"))
}

// rbacWhoamiHandler reports the caller's identity and role.
func rbacWhoamiHandler(engine *rbac.Engine) runtime.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request, _ map[string]string) {
		if engine == nil {
			writeJSON(w, http.StatusOK, map[string]interface{}{"enabled": false})
			return
		}
		user, role, ok := engine.Whoami(bearerFromHeader(r))
		if !ok {
			writeJSON(w, http.StatusUnauthorized, map[string]interface{}{
				"enabled": true,
				"error":   "unknown or missing API token",
			})
			return
		}
		writeJSON(w, http.StatusOK, map[string]interface{}{
			"enabled":   true,
			"user":      user,
			"role":      role,
			"can_admin": engine.CanAdmin(user),
		})
	}
}

// rbacPoliciesHandler returns the effective policy and user assignments. This
// is sensitive, so it is restricted to administrators.
func rbacPoliciesHandler(engine *rbac.Engine) runtime.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request, _ map[string]string) {
		if engine == nil {
			writeJSON(w, http.StatusOK, map[string]interface{}{"enabled": false})
			return
		}
		user, _, ok := engine.Whoami(bearerFromHeader(r))
		if !ok {
			writeJSON(w, http.StatusUnauthorized, map[string]interface{}{
				"error": "unknown or missing API token",
			})
			return
		}
		if !engine.CanAdmin(user) {
			writeJSON(w, http.StatusForbidden, map[string]interface{}{
				"error": "administrator role required",
			})
			return
		}
		writeJSON(w, http.StatusOK, map[string]interface{}{
			"enabled":  true,
			"policies": engine.EffectivePolicies(),
			"users":    engine.Users(),
		})
	}
}

// rbacRolesHandler returns the known role names for selection UIs.
func rbacRolesHandler(engine *rbac.Engine) runtime.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request, _ map[string]string) {
		if engine == nil {
			writeJSON(w, http.StatusOK, map[string]any{"enabled": false})
			return
		}
		if _, _, ok := engine.Whoami(bearerFromHeader(r)); !ok {
			writeJSON(w, http.StatusUnauthorized, map[string]any{"error": "unknown or missing API token"})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"enabled": true, "roles": engine.Roles()})
	}
}

// rbacCreateUserHandler adds a user and returns the (possibly generated) token
// once. Admin only.
func rbacCreateUserHandler(engine *rbac.Engine, g *approvalGate) runtime.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request, _ map[string]string) {
		user, ok := adminCaller(engine, w, r)
		if !ok {
			return
		}
		var body struct {
			Name  string `json:"name"`
			Role  string `json:"role"`
			Token string `json:"token"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid request body"})
			return
		}
		if !approvedHTTP(g, w, r, user, "CreateRbacUser",
			&pb.CreateRbacUserRequest{Name: strings.TrimSpace(body.Name), Role: body.Role, Token: strings.TrimSpace(body.Token)}) {
			return
		}
		token, err := engine.CreateUser(strings.TrimSpace(body.Name), strings.TrimSpace(body.Token), body.Role)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"name":  body.Name,
			"role":  body.Role,
			"token": token,
		})
	}
}

// rbacDeleteUserHandler removes a runtime-managed user. Admin only.
func rbacDeleteUserHandler(engine *rbac.Engine, g *approvalGate) runtime.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request, pathParams map[string]string) {
		user, ok := adminCaller(engine, w, r)
		if !ok {
			return
		}
		if !approvedHTTP(g, w, r, user, "DeleteRbacUser", &pb.DeleteRbacUserRequest{Name: pathParams["name"]}) {
			return
		}
		if err := engine.DeleteUser(pathParams["name"]); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})
	}
}

// rbacSetRoleHandler changes a user's role. Admin only.
func rbacSetRoleHandler(engine *rbac.Engine, g *approvalGate) runtime.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request, pathParams map[string]string) {
		user, ok := adminCaller(engine, w, r)
		if !ok {
			return
		}
		var body struct {
			Role string `json:"role"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid request body"})
			return
		}
		if !approvedHTTP(g, w, r, user, "SetRbacUserRole", &pb.SetRbacUserRoleRequest{Name: pathParams["name"], Role: body.Role}) {
			return
		}
		if err := engine.SetRole(pathParams["name"], body.Role); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})
	}
}
