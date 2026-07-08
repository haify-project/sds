// Command sds-ai hosts the SDS AI Copilot backend. It imports the oss-agent
// library, wires it to the read-only sds-mcp cluster tools and the drbd-reactor
// knowledge base, and serves a single NDJSON streaming endpoint the sds web-ui
// Copilot sidebar talks to.
//
// It is deliberately a small, separate binary (its own Go module) so the
// AI/vector-DB dependency tree never bloats the lean sds-controller, and so the
// AI is optional and independently deployable. The controller never imports it.
//
// Config via environment (all optional except a knowledge DB + LLM key):
//
//	SDS_AI_ADDR           listen address (default ":7634")
//	SDS_AI_KNOWLEDGE_DB   path to drbd-reactor.db cortexdb store (required)
//	SDS_AI_DOMAIN         path to ai/domain.toml (default "ai/domain.toml")
//	SDS_AI_CONTROLLER     sds controller addr for sds-mcp (default "192.168.123.250:3374")
//	SDS_AI_MCP_CMD        sds-mcp executable (default "sds-mcp")
//	SDS_AI_EMB_DIM        embedding dim of the knowledge index (default 1024)
//	SDS_AI_ALLOW_ORIGIN   CORS allow-origin (default "*")
//	OSS_LLM_API_KEY / OSS_LLM_BASE_URL / OSS_LLM_MODEL   LLM (via oss-agent env fallback)
//	OSS_EMB_MODEL / OSS_EMB_BASE_URL / OSS_EMB_API_KEY   embedder (must match the index)
package main

import (
	"encoding/json"
	"log"
	"net/http"
	"os"
	"strconv"

	ossagent "github.com/liliang-cn/oss-agent"
)

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func atoiOr(key string, def int) int {
	if v := os.Getenv(key); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return def
}

func main() {
	addr := envOr("SDS_AI_ADDR", ":7634")
	knowledgeDB := os.Getenv("SDS_AI_KNOWLEDGE_DB")
	if knowledgeDB == "" {
		log.Fatal("SDS_AI_KNOWLEDGE_DB is required (path to drbd-reactor.db)")
	}

	ag, err := ossagent.New(ossagent.Config{
		KnowledgeDBPath: knowledgeDB,
		DomainFile:      envOr("SDS_AI_DOMAIN", "ai/domain.toml"),
		// The drbd-reactor.db index is 1024-dim; the embedder model comes from
		// OSS_EMB_MODEL (must match how the index was built — see spec O1).
		EmbDim: atoiOr("SDS_AI_EMB_DIM", 1024),
		// Mount the sds cluster tools read-only: only observational tools reach
		// the agent; every change is proposed via suggest_action and approved in
		// the UI, executed through the controller REST.
		MCPServers: []ossagent.MCPServerSpec{{
			Name:      "sds",
			Transport: "stdio",
			Command:   envOr("SDS_AI_MCP_CMD", "sds-mcp"),
			Args:      []string{"--controller", envOr("SDS_AI_CONTROLLER", "192.168.123.250:3374")},
			ReadOnly:  true,
		}},
	})
	if err != nil {
		log.Fatalf("init agent: %v", err)
	}
	defer ag.Close()

	for _, s := range ag.MCPStatus() {
		log.Printf("mcp %q connected=%v tools=%d skipped=%d err=%q",
			s.Name, s.Connected, s.Tools, s.Skipped, s.Err)
	}

	allowOrigin := envOr("SDS_AI_ALLOW_ORIGIN", "*")
	mux := http.NewServeMux()
	mux.HandleFunc("/ai/health", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "mcp": ag.MCPStatus()})
	})
	mux.HandleFunc("/ai/chat/stream", streamHandler(ag))
	// Knowledge-base update surface (POST /ai/kb/{doc,ingest,refresh,purge}).
	registerKBRoutes(mux, ag)

	log.Printf("sds-ai listening on %s (knowledge=%s)", addr, knowledgeDB)
	if err := http.ListenAndServe(addr, withCORS(allowOrigin, mux)); err != nil {
		log.Fatal(err)
	}
}

func withCORS(origin string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", origin)
		w.Header().Set("Access-Control-Allow-Methods", "POST, GET, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
		w.Header().Set("Access-Control-Expose-Headers", "X-Session-Id")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}

type chatBody struct {
	Message   string `json:"message"`
	SessionID string `json:"session_id"`
}

// streamHandler runs one turn of the agent and streams NDJSON frames matching the
// web-ui aiClient contract:
//
//	{"t":"tool","name":..,"args":{..}}   {"t":"tool_result","name":..}
//	{"t":"text","d":".."}   {"t":"reset"}   {"t":"suggestion", <Suggestion>}
//	{"t":"error","d":".."}   {"t":"done"}
func streamHandler(ag *ossagent.Agent) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "use POST", http.StatusMethodNotAllowed)
			return
		}
		var body chatBody
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Message == "" {
			http.Error(w, "missing message", http.StatusBadRequest)
			return
		}
		flusher, ok := w.(http.Flusher)
		if !ok {
			http.Error(w, "streaming unsupported", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/x-ndjson; charset=utf-8")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("X-Accel-Buffering", "no")
		if body.SessionID != "" {
			w.Header().Set("X-Session-Id", body.SessionID)
		}
		w.WriteHeader(http.StatusOK)

		enc := json.NewEncoder(w)
		frame := func(v any) {
			_ = enc.Encode(v)
			flusher.Flush()
		}

		// Stream is single-turn (stateless). Cross-turn memory is a follow-up
		// once the facade exposes a session-aware streaming method.
		_, _, err := ag.Stream(r.Context(), body.Message, func(ev ossagent.Event) {
			switch ev.Kind {
			case ossagent.EventText:
				if ev.Text != "" {
					frame(map[string]any{"t": "text", "d": ev.Text})
				}
			case ossagent.EventReset:
				frame(map[string]any{"t": "reset"})
			case ossagent.EventToolCall:
				frame(map[string]any{"t": "tool", "name": ev.Tool, "args": ev.Args})
			case ossagent.EventToolResult:
				frame(map[string]any{"t": "tool_result", "name": ev.Tool})
			case ossagent.EventSuggestion:
				if s := ev.Suggestion; s != nil {
					frame(map[string]any{
						"t":        "suggestion",
						"action":   s.Action,
						"params":   s.Params,
						"reason":   s.Reason,
						"severity": s.Severity,
						"verdict": map[string]any{
							"blocked":  s.Verdict.Blocked,
							"ruleId":   s.Verdict.RuleID,
							"reason":   s.Verdict.Reason,
							"severity": string(s.Verdict.Severity),
						},
					})
				}
			case ossagent.EventError:
				frame(map[string]any{"t": "error", "d": ev.Text})
			}
		})
		if err != nil {
			frame(map[string]any{"t": "error", "d": err.Error()})
		}
		frame(map[string]any{"t": "done"})
	}
}
