package main

import (
	"context"
	"encoding/json"
	"net/http"

	ossagent "github.com/liliang-cn/oss-agent"
)

// registerKBRoutes wires the knowledge-base update endpoints onto mux. They MUTATE
// the vector/graph index, so the embedder in effect (OSS_EMB_MODEL) MUST be the one
// the index was built with — 1024-dim text-embedding-v4. A mismatched embedder
// writes vectors of a different dimension and corrupts retrieval (oss-agent spec
// O1). sds-ai already fails fast if EmbDim disagrees, but the model itself must
// also match.
//
// Endpoints (all POST, JSON in/out):
//
//	/ai/kb/doc      {id,title,content}   -> ingest/replace one document
//	/ai/kb/ingest   {dir}                -> ingest a directory (docs + code error strings)
//	/ai/kb/refresh  {dir}                -> purge that source then re-ingest (captures edits/deletes)
//	/ai/kb/purge    {match,prefix}       -> remove a source
func registerKBRoutes(mux *http.ServeMux, ag *ossagent.Agent) {
	mux.HandleFunc("/ai/kb/doc", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "use POST", http.StatusMethodNotAllowed)
			return
		}
		var b struct {
			ID      string `json:"id"`
			Title   string `json:"title"`
			Content string `json:"content"`
		}
		if err := json.NewDecoder(r.Body).Decode(&b); err != nil || b.ID == "" || b.Content == "" {
			http.Error(w, "missing id/content", http.StatusBadRequest)
			return
		}
		if err := ag.IngestDoc(r.Context(), b.ID, b.Title, b.Content); err != nil {
			writeKBErr(w, err)
			return
		}
		writeKBJSON(w, map[string]any{"ok": true, "id": b.ID})
	})

	mux.HandleFunc("/ai/kb/ingest", dirHandler(ag.IngestDir))
	mux.HandleFunc("/ai/kb/refresh", dirHandler(ag.Refresh))

	mux.HandleFunc("/ai/kb/purge", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "use POST", http.StatusMethodNotAllowed)
			return
		}
		var b struct {
			Match  string `json:"match"`
			Prefix bool   `json:"prefix"`
		}
		if err := json.NewDecoder(r.Body).Decode(&b); err != nil || b.Match == "" {
			http.Error(w, "missing match", http.StatusBadRequest)
			return
		}
		chunks, nodes, err := ag.PurgeSource(r.Context(), b.Match, b.Prefix)
		if err != nil {
			writeKBErr(w, err)
			return
		}
		writeKBJSON(w, map[string]any{"ok": true, "chunks": chunks, "nodes": nodes})
	})
}

// dirHandler adapts a directory-taking ingest/refresh method into an HTTP handler
// that reads {"dir": "..."} and returns the resulting stats.
func dirHandler(fn func(context.Context, string) (ossagent.IngestStats, error)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "use POST", http.StatusMethodNotAllowed)
			return
		}
		var b struct {
			Dir string `json:"dir"`
		}
		if err := json.NewDecoder(r.Body).Decode(&b); err != nil || b.Dir == "" {
			http.Error(w, "missing dir", http.StatusBadRequest)
			return
		}
		stats, err := fn(r.Context(), b.Dir)
		if err != nil {
			writeKBErr(w, err)
			return
		}
		writeKBJSON(w, map[string]any{"ok": true, "stats": stats})
	}
}

func writeKBJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

func writeKBErr(w http.ResponseWriter, err error) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusInternalServerError)
	_ = json.NewEncoder(w).Encode(map[string]any{"ok": false, "error": err.Error()})
}
