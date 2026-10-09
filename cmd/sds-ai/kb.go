package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/liliang-cn/steward"
)

// registerKBRoutes wires the knowledge-base update endpoints onto mux. They MUTATE
// the vector/graph index, so the embedder in effect (STEWARD_EMB_MODEL) MUST be the one
// the index was built with — 768-dim embeddinggemma. A mismatched embedder
// writes vectors of a different dimension and corrupts retrieval (steward spec
// O1). sds-ai already fails fast if EmbDim disagrees, but the model itself must
// also match.
//
// Endpoints (all POST, JSON in/out):
//
//	/ai/kb/doc      {id,title,content}   -> ingest/replace one document
//	/ai/kb/ingest   {dir}                -> ingest a directory (docs + code error strings)
//	/ai/kb/refresh  {dir}                -> purge that source then re-ingest (captures edits/deletes)
//	/ai/kb/purge    {match,prefix}       -> remove a source
//
// Plus two GETs that read: /ai/kb/list (what is in the index) and
// /ai/kb/doctor (whether retrieval over it still works).
func registerKBRoutes(mux *http.ServeMux, ag *steward.Agent) {
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
			// How far the extracted graph has wandered from the vocabulary
			// domain.toml declares. An undeclared EDGE type is the one that
			// costs something: expansion filters on the declared vocabulary,
			// so the edge is stored and then never traversed, and the graph
			// looks populated while expanding to nothing.
			"drift": inv.Drift,
		})
	})

	// GET /ai/kb/doctor answers "does retrieval still work", which /ai/kb/list
	// cannot: an index can hold the right documents and still return nothing.
	//
	// Every check corresponds to a failure that produced no error anywhere on
	// this cluster — a stale HTTP_PROXY in front of the embedder while chat kept
	// working, a vector index capping recall below the requested k, the Haify code
	// graph outnumbering the runbooks ten to one, and the extractor inventing a
	// "StorageClass" node type the domain never declared. The copilot answered
	// through all of them; it just answered worse.
	//
	// GET rather than POST because it changes nothing, and unauthenticated for
	// the same reason the rest of this surface is: `guard` keeps it on loopback.
	mux.HandleFunc("/ai/kb/doctor", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "use GET", http.StatusMethodNotAllowed)
			return
		}
		d := ag.Doctor(r.Context())
		checks := make([]map[string]any, 0, len(d.Checks))
		for _, c := range d.Checks {
			checks = append(checks, map[string]any{
				"name": c.Name, "status": string(c.Status), "detail": c.Detail, "hint": c.Hint,
			})
		}
		// ok is false when a check FAILED, not when one warned: a warning is
		// something to look at, and a monitor that pages on every one of them
		// gets muted.
		writeKBJSON(w, map[string]any{"ok": !d.Failed, "checks": checks})
	})

	// GET /ai/kb/resolve merges entities that are one concept spelled two ways.
	//
	// Entity ids keep separators, so "DRBDResource" and "DRBD resource" are two
	// nodes and each document's edges attached to whichever spelling it used.
	// The copilot's walk pools them, so answers no longer depend on how a name
	// was typed — but the graph still holds two entities and doctor still warns.
	// This ends it.
	//
	// GET and dry by default: deleting a node is not reversible, and the list is
	// short enough to read first. ?apply=true makes the merges.
	mux.HandleFunc("/ai/kb/resolve", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "use GET", http.StatusMethodNotAllowed)
			return
		}
		apply := r.URL.Query().Get("apply") == "true"
		rep, err := ag.ResolveSpellings(r.Context(), !apply)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		groups := make([]map[string]any, 0, len(rep.Groups))
		for _, g := range rep.Groups {
			groups = append(groups, map[string]any{"canonical": g.Canonical, "aliases": g.Aliases})
		}
		writeKBJSON(w, map[string]any{
			"applied": apply, "merged": rep.Merged, "groups": groups,
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
func dirHandler(fn func(context.Context, string) (steward.IngestStats, error)) http.HandlerFunc {
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
