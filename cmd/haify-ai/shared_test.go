package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSharedKnowledgeRefusesAnotherEmbedder(t *testing.T) {
	dir := t.TempDir()
	db := filepath.Join(dir, "haify-kb.db")
	if err := os.WriteFile(db, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	manifest := `{"version":"v1","embedder":{"model":"embeddinggemma:latest","dim":768}}`
	if err := os.WriteFile(filepath.Join(dir, "haify-kb.json"), []byte(manifest), 0o644); err != nil {
		t.Fatal(err)
	}

	if got, err := sharedKnowledge("", "x", 768); err != nil || got != nil {
		t.Fatalf("no shared base configured: got %v, %v", got, err)
	}
	if got, err := sharedKnowledge(db, "embeddinggemma:latest", 768); err != nil || len(got) != 1 {
		t.Fatalf("matching embedder: got %v, %v", got, err)
	}
	if _, err := sharedKnowledge(db, "text-embedding-v4", 1024); err == nil || !strings.Contains(err.Error(), "768") {
		t.Fatalf("another width must be refused: %v", err)
	}
	if _, err := sharedKnowledge(db, "nomic-embed-text", 768); err == nil {
		t.Fatal("another model at the same width must be refused")
	}
	if err := os.Remove(filepath.Join(dir, "haify-kb.json")); err != nil {
		t.Fatal(err)
	}
	if _, err := sharedKnowledge(db, "embeddinggemma:latest", 768); err == nil {
		t.Fatal("a base without its manifest must be refused")
	}
}
