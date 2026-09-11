package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Settings ride the replicated volume so a model chosen on one node is already
// there when the promoter starts the agent on another. Defaulting them beside
// the knowledge DB is what buys that without another setting to get wrong.
func TestSettingsLandNextToTheKnowledgeDB(t *testing.T) {
	t.Setenv("SDS_AI_SETTINGS", "")
	got := settingsPath("/var/lib/sds/ai/drbd-reactor.db")
	if want := "/var/lib/sds/ai/sds-ai-settings.json"; got != want {
		t.Errorf("settingsPath = %q, want %q", got, want)
	}

	t.Setenv("SDS_AI_SETTINGS", "/etc/sds/ai.json")
	if got := settingsPath("/var/lib/sds/ai/drbd-reactor.db"); got != "/etc/sds/ai.json" {
		t.Errorf("an explicit SDS_AI_SETTINGS was ignored: %q", got)
	}
}

// A missing file is "nothing overridden", not an error. The agent has to start
// on a cluster that has never opened the settings page.
func TestNoSettingsFileIsNotAnError(t *testing.T) {
	s, err := loadSettings(filepath.Join(t.TempDir(), "absent.json"))
	if err != nil {
		t.Fatalf("a missing settings file errored: %v", err)
	}
	if s != (settings{}) {
		t.Errorf("a missing file produced %+v, want the zero value", s)
	}
}

// The file holds an API key, so it is the owner's alone.
func TestSavedSettingsAreNotWorldReadable(t *testing.T) {
	p := filepath.Join(t.TempDir(), "settings.json")
	if err := saveSettings(p, settings{LLMModel: "m", LLMAPIKey: "sk-secret"}); err != nil {
		t.Fatalf("saveSettings: %v", err)
	}
	fi, err := os.Stat(p)
	if err != nil {
		t.Fatal(err)
	}
	if mode := fi.Mode().Perm(); mode != 0o600 {
		t.Errorf("settings mode = %o, want 600: the file holds an API key", mode)
	}

	back, err := loadSettings(p)
	if err != nil {
		t.Fatalf("loadSettings: %v", err)
	}
	if back.LLMAPIKey != "sk-secret" || back.LLMModel != "m" {
		t.Errorf("round trip lost fields: %+v", back)
	}
	// The rename target is the only file left: a .tmp survivor would be a
	// world-visible copy of the same secret on the next failed write.
	entries, _ := os.ReadDir(filepath.Dir(p))
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".tmp") {
			t.Errorf("a temporary settings file was left behind: %s", e.Name())
		}
	}
}

// The rule that makes this endpoint safe to add.
//
// sds-ai serves unauthenticated when bound to loopback, and the controller
// proxies /ai/* from its own public port — so anything reachable there is
// reachable from wherever the console is. Reading a model name over that is
// harmless. Accepting an API key over it is not, so the write refuses rather
// than inheriting the process's own laxity.
func TestWritingSettingsRefusesWithoutAToken(t *testing.T) {
	mux := http.NewServeMux()
	// A nil agent is safe here precisely because the gate returns before
	// touching it — if that ever stops being true, this test panics rather
	// than passing quietly.
	registerConfigRoutes(mux, nil, &configStore{path: filepath.Join(t.TempDir(), "s.json")},
		"embeddinggemma", 768, false)

	rec := httptest.NewRecorder()
	body := strings.NewReader(`{"llmModel":"evil","llmApiKey":"sk-attacker"}`)
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodPut, "/ai/config", body))

	if rec.Code != http.StatusForbidden {
		t.Fatalf("PUT without a token returned %d, want 403", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "SDS_AI_TOKEN") {
		t.Errorf("the refusal does not say how to enable editing: %q", rec.Body.String())
	}
}

// And a read-only deployment says so, so the UI can explain instead of
// offering a form that 403s on submit.
func TestReadOnlyDeploymentIsAdvertised(t *testing.T) {
	st := &configStore{path: filepath.Join(t.TempDir(), "s.json")}
	// view() reads the live model off the agent; with none, exercise only the
	// fields that do not.
	v := configView{Editable: false}
	if st.cur.LLMAPIKey != "" {
		t.Fatal("a fresh store already holds a key")
	}
	if v.Editable {
		t.Fatal("the zero view is editable")
	}

	// The shape the UI keys off must be present in the JSON, under these names.
	b, err := json.Marshal(configView{LLMModel: "m", EmbModel: "e", EmbDim: 768,
		Editable: false, ReadOnlyReason: "no bearer token"})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"llmModel"`, `"embModel"`, `"embDim"`, `"editable"`, `"readOnlyReason"`} {
		if !strings.Contains(string(b), want) {
			t.Errorf("configView JSON is missing %s: %s", want, b)
		}
	}
	// And never a key, under any name.
	if strings.Contains(strings.ToLower(string(b)), "apikey\":\"") {
		t.Errorf("configView serialised an API key: %s", b)
	}
}
