package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"

	opspilot "github.com/liliang-cn/opspilot"
)

// The Copilot's model, changeable while it runs.
//
// Changing which LLM answers used to mean editing the unit's environment file
// and restarting sds-ai. That restart is not a restart: sds-ai.service is in the
// drbd-reactor promoter's start list for sds-meta, so stopping it demotes the
// resource and fails the whole control plane — VIP, controller and all — onto
// another node. A model name should not cost an outage, so opspilot v0.42.0
// swaps the generator in place and this is the surface for it.
//
// Two halves, and they are not the same risk:
//
//   - GET reports what is running. It carries no secret and is as safe as
//     /ai/health.
//   - PUT changes it, and may carry an API key. It REFUSES to serve without a
//     bearer token even on loopback, which is stricter than the rest of this
//     process — see requireSettingsToken.

// settings is what survives a restart. It is deliberately a file of this
// process's own rather than the unit's EnvironmentFile: systemd owns that one,
// rewriting a file another program reads at boot is a race with no owner, and
// the env stays what it should be — the bootstrap.
type settings struct {
	LLMBaseURL string `json:"llmBaseUrl,omitempty"`
	LLMModel   string `json:"llmModel,omitempty"`
	LLMAPIKey  string `json:"llmApiKey,omitempty"`
}

// settingsPath is where they live.
//
// Default: beside the knowledge database. That is not an arbitrary choice — the
// knowledge DB is on the replicated volume, so settings written on one node are
// already there when the promoter starts the agent on another. A change that
// vanished on the next failover would be worse than no change at all, because
// it would vanish quietly.
func settingsPath(knowledgeDB string) string {
	if p := strings.TrimSpace(os.Getenv("SDS_AI_SETTINGS")); p != "" {
		return p
	}
	return filepath.Join(filepath.Dir(knowledgeDB), "sds-ai-settings.json")
}

// loadSettings reads the file, treating absence as "nothing overridden".
func loadSettings(path string) (settings, error) {
	b, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return settings{}, nil
	}
	if err != nil {
		return settings{}, err
	}
	var s settings
	if err := json.Unmarshal(b, &s); err != nil {
		return settings{}, fmt.Errorf("%s: %w", path, err)
	}
	return s, nil
}

// saveSettings writes them, readable only by the owner.
//
// Written to a temporary file and renamed: a half-written settings file is one
// the agent refuses to start from, and the window for that is exactly as long
// as a non-atomic write.
func saveSettings(path string, s settings) error {
	b, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, append(b, '\n'), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// configView is what an operator is shown. There is no API key in it, and no
// field that could hold one: this is returned by a GET that needs no
// credentials, and a struct that can carry a secret eventually does.
type configView struct {
	LLMBaseURL string `json:"llmBaseUrl"`
	LLMModel   string `json:"llmModel"`
	// HasAPIKey says a key is configured, which is all a form needs to know to
	// render "leave blank to keep the current key".
	HasAPIKey bool `json:"hasApiKey"`

	// The embedder is reported and cannot be set. An index is built with one
	// embedder at one dimension and can only be queried by that same embedder,
	// so changing it here would not reconfigure anything — it would invalidate
	// every vector in the knowledge base. Shown because "does the embedder
	// still match the index" is a real question during an outage.
	EmbModel string `json:"embModel"`
	EmbDim   int    `json:"embDim"`
	// Editable is false when this process will not accept a write, so the UI
	// can say why instead of offering a form that returns 403.
	Editable bool `json:"editable"`
	// Why Editable is false. Empty when it is true.
	ReadOnlyReason string `json:"readOnlyReason,omitempty"`
}

// configStore holds what the running process knows about its own settings.
// The mutex guards the persisted copy; opspilot guards the live provider.
type configStore struct {
	mu   sync.Mutex
	path string
	cur  settings
}

// registerConfigRoutes wires GET/PUT /ai/config.
func registerConfigRoutes(mux *http.ServeMux, ag *opspilot.Agent, st *configStore,
	embModel string, embDim int, tokenConfigured bool) {

	mux.HandleFunc("/ai/config", func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			writeJSON(w, http.StatusOK, st.view(ag, embModel, embDim, tokenConfigured))

		case http.MethodPut, http.MethodPost:
			if !tokenConfigured {
				// Stricter than the rest of this process on purpose. sds-ai
				// serves unauthenticated when bound to loopback, and the
				// controller proxies /ai/* from its own public port — so
				// "loopback" is not the boundary it sounds like. Reading the
				// model over that is harmless; accepting an API key over it is
				// not.
				http.Error(w, "settings are read-only: this endpoint accepts an API key and "+
					"will not do so unauthenticated. Set SDS_AI_TOKEN (or write /etc/sds/token) "+
					"and restart sds-ai to enable editing.", http.StatusForbidden)
				return
			}
			var body struct {
				LLMBaseURL string `json:"llmBaseUrl"`
				LLMModel   string `json:"llmModel"`
				LLMAPIKey  string `json:"llmApiKey"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				http.Error(w, "bad request: "+err.Error(), http.StatusBadRequest)
				return
			}
			if err := st.apply(ag, settings{
				LLMBaseURL: body.LLMBaseURL,
				LLMModel:   body.LLMModel,
				LLMAPIKey:  body.LLMAPIKey,
			}); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			writeJSON(w, http.StatusOK, st.view(ag, embModel, embDim, tokenConfigured))

		default:
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		}
	})
}

func (st *configStore) view(ag *opspilot.Agent, embModel string, embDim int, editable bool) configView {
	st.mu.Lock()
	hasKey := st.cur.LLMAPIKey != ""
	st.mu.Unlock()

	live := ag.LLM()
	v := configView{
		LLMBaseURL: live.BaseURL,
		LLMModel:   live.Model,
		// A key from the environment counts: the question the form is asking is
		// "will a blank field leave a working key in place", not "did this file
		// supply it".
		HasAPIKey: hasKey || os.Getenv("OPSPILOT_LLM_API_KEY") != "" || os.Getenv("OPSDOCTOR_LLM_API_KEY") != "" || os.Getenv("OSS_LLM_API_KEY") != "",
		EmbModel:  embModel,
		EmbDim:    embDim,
		Editable:  editable,
	}
	if !editable {
		v.ReadOnlyReason = "no bearer token is configured, and this endpoint accepts an API key"
	}
	return v
}

// apply swaps the live model and then persists it.
//
// In that order, deliberately. A settings file that names a model the running
// agent rejected is a lie that survives a reboot; one that lags a successful
// swap by a few milliseconds is not.
func (st *configStore) apply(ag *opspilot.Agent, in settings) error {
	if _, err := ag.SetLLM(opspilot.LLMSettings{
		BaseURL: in.LLMBaseURL,
		Model:   in.LLMModel,
	}, in.LLMAPIKey); err != nil {
		return err
	}

	st.mu.Lock()
	defer st.mu.Unlock()
	live := ag.LLM()
	st.cur.LLMBaseURL, st.cur.LLMModel = live.BaseURL, live.Model
	if k := strings.TrimSpace(in.LLMAPIKey); k != "" {
		st.cur.LLMAPIKey = k
	}
	if err := saveSettings(st.path, st.cur); err != nil {
		// The swap already happened and the agent is answering on the new model.
		// Saying "failed" would invite a retry that changes nothing; saying
		// nothing would let the change disappear at the next promotion.
		return fmt.Errorf("the model is now %s on %s, but saving it failed, so a "+
			"restart or failover will revert it: %w", live.Model, live.BaseURL, err)
	}
	return nil
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}
