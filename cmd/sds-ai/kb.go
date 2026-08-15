package main

import (
	"context"
	"encoding/json"
	"fmt"
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

	// GET /ai/kb/list answers "what is actually in there?". Without it the only
	// way to check an ingest landed — or that an index still matches the
	// configured embedder — was to open the SQLite file on the node by hand.
	mux.HandleFunc("/ai/kb/list", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "use GET", http.StatusMethodNotAllowed)
			return
		}
		inv, err := ag.Inventory(r.Context())
		if err != nil {
			writeKBErr(w, err)
			return
		}
		sources := make([]map[string]any, 0, len(inv.Sources))
		for _, s := range inv.Sources {
			sources = append(sources, map[string]any{
				"document_id": s.DocumentID, "chunks": s.Chunks, "bytes": s.Bytes,
			})
		}
		writeKBJSON(w, map[string]any{
			"ok": true, "sources": sources, "chunks": inv.Chunks,
			"graph_nodes": inv.Nodes, "graph_edges": inv.Edges,
			// Read back from the index, not from SDS_AI_EMB_DIM: the two
			// disagreeing is what makes every search silently return nothing.
			"dim": inv.Dim,
		})
	})

	// POST /ai/kb/upload ingests documents sent over the wire.
	//
	// /ai/kb/ingest reads a directory ON THE NODE, so loading a corpus from
	// anywhere else meant copying the files there first — scp, root, a path to
	// clean up afterwards, and the corpus left sitting on the DRBD mount for no
	// reason. That is a lot of ceremony for "here are eight markdown files".
	// This takes the documents themselves.
	//
	// Re-uploading an id replaces it, so this is also how a corpus is refreshed
	// after the docs change: send them all again.
	mux.HandleFunc("/ai/kb/upload", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "use POST", http.StatusMethodNotAllowed)
			return
		}
		var b struct {
			Docs []struct {
				ID      string `json:"id"`
				Title   string `json:"title"`
				Content string `json:"content"`
			} `json:"docs"`
		}
		if err := json.NewDecoder(r.Body).Decode(&b); err != nil {
			http.Error(w, `body must be {"docs":[{"id","title","content"}]}`, http.StatusBadRequest)
			return
		}
		if len(b.Docs) == 0 {
			http.Error(w, "no documents", http.StatusBadRequest)
			return
		}

		// Each document costs an embedding round trip and an extraction call, so
		// a failure partway through is normal enough to report precisely rather
		// than collapse into one error: the operator needs to know which
		// documents landed and which to send again.
		ingested := make([]string, 0, len(b.Docs))
		failures := map[string]string{}
		for i, d := range b.Docs {
			if d.ID == "" || d.Content == "" {
				failures[fmt.Sprintf("#%d", i)] = "missing id or content"
				continue
			}
			if err := ag.IngestDoc(r.Context(), d.ID, d.Title, d.Content); err != nil {
				failures[d.ID] = err.Error()
				continue
			}
			ingested = append(ingested, d.ID)
		}
		writeKBJSON(w, map[string]any{
			"ok": len(failures) == 0, "ingested": ingested, "failed": failures,
		})
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
