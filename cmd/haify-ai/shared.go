package main

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
)

// sharedManifest is the haify-kb.json `make kb` writes next to the shared base.
type sharedManifest struct {
	Version  string `json:"version"`
	Embedder struct {
		Model string `json:"model"`
		Dim   int    `json:"dim"`
	} `json:"embedder"`
}

// sharedKnowledge returns the shared base to attach, after checking that it
// was built with the embedder this process queries with.
//
// A base can only be searched with the embedder that built it, and a mismatch
// is not an error anywhere downstream: a different width finds nothing, and a
// different model of the same width finds the wrong things, both quietly. The
// manifest installed with the base says which embedder built it, so a base
// that cannot be searched here stops haify-ai at startup instead.
func sharedKnowledge(path, embModel string, embDim int) ([]string, error) {
	if path == "" {
		return nil, nil
	}
	manifestPath := strings.TrimSuffix(path, ".db") + ".json"
	raw, err := os.ReadFile(manifestPath)
	if err != nil {
		return nil, fmt.Errorf("shared knowledge base %s: its manifest %s is installed with it and says which embedder built it: %w",
			path, manifestPath, err)
	}
	var m sharedManifest
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, fmt.Errorf("shared knowledge base manifest %s: %w", manifestPath, err)
	}
	if m.Embedder.Dim != embDim {
		return nil, fmt.Errorf("shared knowledge base %s (%s) was built at %d dimensions with %s; this node embeds at %d — "+
			"use the same embedder, or build the base with this one", path, m.Version, m.Embedder.Dim, m.Embedder.Model, embDim)
	}
	if embModel != "" && m.Embedder.Model != "" && embModel != m.Embedder.Model {
		return nil, fmt.Errorf("shared knowledge base %s (%s) was built with %s; this node embeds with %s, "+
			"whose vectors mean something else at the same width", path, m.Version, m.Embedder.Model, embModel)
	}
	return []string{path}, nil
}
