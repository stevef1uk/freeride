package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

func TestGeminiDirectRouting_Isolated(t *testing.T) {
	var gotModel, gotAuth string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1beta/openai/chat/completions" && !strings.HasSuffix(r.URL.Path, "/chat/completions") {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		gotAuth = r.Header.Get("Authorization")
		var body map[string]interface{}
		_ = json.NewDecoder(r.Body).Decode(&body)
		if m, ok := body["model"].(string); ok {
			gotModel = m
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"ok"}}],"model":"gemini-3.5-flash"}`))
	}))
	defer upstream.Close()

	t.Setenv("GEMINI_API_KEY", "test-gemini-key")
	// Point Gemini base at mock server for this test only.
	prevBase := geminiOpenAIBaseURL
	geminiOpenAIBaseURL = strings.TrimSuffix(upstream.URL, "/")
	t.Cleanup(func() {
		geminiOpenAIBaseURL = prevBase
	})

	t.Cleanup(func() {
		fetchFreeModelsHook = nil
		fetchNvidiaFreeModelsHook = nil
		fetchCerebrasModelsHook = nil
		fetchOllamaCloudModelsHook = nil
		_ = os.Unsetenv("GEMINI_API_KEY")
	})
	fetchFreeModelsHook = func() ([]openRouterModel, error) { return nil, nil }
	fetchNvidiaFreeModelsHook = func() ([]nvidiaModel, error) { return nil, nil }
	fetchCerebrasModelsHook = func() ([]cerebrasModel, error) { return nil, nil }
	fetchOllamaCloudModelsHook = func() ([]ollamaModel, error) { return nil, nil }
	resetProxyCooldownsForTest()

	prevCfg := globalModelsConfig
	t.Cleanup(func() {
		configMutex.Lock()
		globalModelsConfig = prevCfg
		configMutex.Unlock()
	})
	configMutex.Lock()
	globalModelsConfig = modelsConfig{
		GeminiModels: []geminiModel{{
			ID:    "google/gemini-3.5-flash",
			Model: "gemini-3.5-flash",
		}},
	}
	configMutex.Unlock()

	const modelID = "google/gemini-3.5-flash"
	payload := map[string]interface{}{
		"model": modelID,
		"messages": []map[string]interface{}{
			{"role": "user", "content": "ping"},
		},
		"max_tokens": 10,
	}
	b, _ := json.Marshal(payload)
	rec := httptest.NewRecorder()
	handleChatCompletions(rec, httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(b)))

	if rec.Code != http.StatusOK {
		t.Fatalf("status %d body %s", rec.Code, rec.Body.String())
	}
	if gotModel != "gemini-3.5-flash" {
		t.Fatalf("upstream model: got %q want gemini-3.5-flash", gotModel)
	}
	if gotAuth != "Bearer test-gemini-key" {
		t.Fatalf("Authorization: got %q", gotAuth)
	}
}

func TestSelectCandidates_GeminiWhenKeySet(t *testing.T) {
	t.Setenv("GEMINI_API_KEY", "test-key")
	t.Cleanup(func() { _ = os.Unsetenv("GEMINI_API_KEY") })

	ctx := candidateContext{
		conf: modelsConfig{
			GeminiModels: []geminiModel{
				{ID: "google/gemini-3.5-flash", Model: "gemini-3.5-flash"},
				{ID: "google/gemini-2.5-flash-lite", Model: "gemini-2.5-flash-lite"},
			},
		},
		isCooldown:       func(string) bool { return false },
		isExcluded:       func(string) bool { return false },
		isComplexRequest: true,
	}
	candidates := selectCandidates(ctx)
	if len(candidates) < 2 {
		t.Fatalf("expected gemini models in candidates, got %v", candidates)
	}
	if candidates[0] != "google/gemini-3.5-flash" {
		t.Fatalf("first candidate: got %q want google/gemini-3.5-flash", candidates[0])
	}
}

func TestSelectCandidates_GeminiSkippedWithoutKey(t *testing.T) {
	_ = os.Unsetenv("GEMINI_API_KEY")
	_ = os.Unsetenv("GOOGLE_API_KEY")

	prevCfg := globalModelsConfig
	t.Cleanup(func() {
		configMutex.Lock()
		globalModelsConfig = prevCfg
		configMutex.Unlock()
	})
	configMutex.Lock()
	globalModelsConfig = modelsConfig{
		GeminiModels: []geminiModel{{ID: "google/gemini-3.5-flash", Model: "gemini-3.5-flash"}},
	}
	configMutex.Unlock()

	ctx := candidateContext{
		conf: modelsConfig{
			GeminiModels: []geminiModel{
				{ID: "google/gemini-3.5-flash", Model: "gemini-3.5-flash"},
			},
		},
		isCooldown: func(string) bool { return false },
		isExcluded: func(string) bool { return false },
	}
	candidates := selectCandidates(ctx)
	for _, c := range candidates {
		if c == "google/gemini-3.5-flash" {
			t.Fatal("gemini candidate should not appear without API key")
		}
	}
}

func TestSanitizeBody_GoogleOpenRouterNotNvidia(t *testing.T) {
	body := map[string]interface{}{
		"model":       "google/gemini-2.0-flash-001",
		"tool_choice": "auto",
	}
	sanitizeBody(body)
	if _, ok := body["tool_choice"]; !ok {
		t.Fatal("OpenRouter google/ model should keep tool_choice (not NVIDIA sanitization)")
	}
}

func TestSelectCandidates_PolecatPrefersGeminiFirst(t *testing.T) {
	t.Setenv("GEMINI_API_KEY", "test-key")
	t.Cleanup(func() { _ = os.Unsetenv("GEMINI_API_KEY") })

	ctx := candidateContext{
		originalModel: "google/gemini-3.5-flash",
		role:          "polecat",
		conf: modelsConfig{
			GeminiModels: []geminiModel{
				{ID: "google/gemini-3.5-flash", Model: "gemini-3.5-flash"},
				{ID: "google/gemini-2.5-flash-lite", Model: "gemini-2.5-flash-lite"},
			},
			NvidiaReliable: []string{"nvidia/llama-3.3-nemotron-super-49b-v1"},
			CerebrasBudget: []string{"cerebras/llama3.1-8b"},
		},
		isCooldown:       func(string) bool { return false },
		isExcluded:       func(string) bool { return false },
		isComplexRequest: true,
	}
	candidates := selectCandidates(ctx)
	if len(candidates) == 0 {
		t.Fatal("expected candidates for polecat")
	}
	if candidates[0] != "google/gemini-3.5-flash" {
		t.Fatalf("polecat first candidate = %q, want google/gemini-3.5-flash (got %v)", candidates[0], candidates)
	}
}

func TestResolveGeminiAPIKey_AcceptsGoogleAPIKey(t *testing.T) {
	t.Setenv("GOOGLE_API_KEY", "google-key")
	t.Setenv("GEMINI_API_KEY", "")
	t.Cleanup(func() {
		_ = os.Unsetenv("GOOGLE_API_KEY")
		_ = os.Unsetenv("GEMINI_API_KEY")
	})
	if got := resolveGeminiAPIKey(); got != "google-key" {
		t.Fatalf("resolveGeminiAPIKey() = %q, want google-key", got)
	}
}

func TestHandleTags_IncludesGeminiWhenKeySet(t *testing.T) {
	t.Setenv("GEMINI_API_KEY", "test-key")
	t.Cleanup(func() { _ = os.Unsetenv("GEMINI_API_KEY") })

	fetchFreeModelsHook = func() ([]openRouterModel, error) { return nil, nil }
	fetchNvidiaFreeModelsHook = func() ([]nvidiaModel, error) { return nil, nil }
	fetchCerebrasModelsHook = func() ([]cerebrasModel, error) { return nil, nil }
	fetchOllamaCloudModelsHook = func() ([]ollamaModel, error) { return nil, nil }
	t.Cleanup(func() {
		fetchFreeModelsHook = nil
		fetchNvidiaFreeModelsHook = nil
		fetchCerebrasModelsHook = nil
		fetchOllamaCloudModelsHook = nil
	})

	prevCfg := globalModelsConfig
	t.Cleanup(func() {
		configMutex.Lock()
		globalModelsConfig = prevCfg
		configMutex.Unlock()
	})
	configMutex.Lock()
	globalModelsConfig = modelsConfig{
		GeminiModels: []geminiModel{{ID: "google/gemini-3.5-flash", Model: "gemini-3.5-flash"}},
	}
	configMutex.Unlock()

	rec := httptest.NewRecorder()
	handleTags(rec, httptest.NewRequest(http.MethodGet, "/api/tags", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d", rec.Code)
	}
	var data ollamaTagsResponse
	if err := json.NewDecoder(rec.Body).Decode(&data); err != nil {
		t.Fatalf("decode: %v", err)
	}
	found := false
	for _, m := range data.Models {
		if m.Name == "google/gemini-3.5-flash" {
			found = true
			if m.Details.Family != "gemini" {
				t.Fatalf("family = %q, want gemini", m.Details.Family)
			}
			break
		}
	}
	if !found {
		t.Fatalf("google/gemini-3.5-flash not in /api/tags: %v", data.Models)
	}
}

func TestGeminiAPIModelName(t *testing.T) {
	m := &geminiModel{Model: "gemini-3.5-flash"}
	if got := geminiAPIModelName(m, "google/gemini-3.5-flash"); got != "gemini-3.5-flash" {
		t.Fatalf("got %q", got)
	}
	if got := geminiAPIModelName(nil, "google/gemini-2.5-flash:free"); got != "gemini-2.5-flash" {
		t.Fatalf("fallback strip: got %q", got)
	}
}
