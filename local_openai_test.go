package main

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestHandleChatCompletions_LocalOpenAI_ReachesUpstream verifies the proxy POSTs
// to a local OpenAI-compatible /v1/chat/completions and maps yaml "model" to the upstream body.
func TestHandleChatCompletions_LocalOpenAI_ReachesUpstream(t *testing.T) {
	var upstreamAuth string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1/chat/completions" {
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
			http.NotFound(w, r)
			return
		}
		upstreamAuth = r.Header.Get("Authorization")
		raw, err := io.ReadAll(r.Body)
		if err != nil {
			t.Error(err)
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		var body map[string]interface{}
		if err := json.Unmarshal(raw, &body); err != nil {
			t.Error(err)
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if got, _ := body["model"].(string); got != "llama-server-model-id" {
			t.Errorf("upstream JSON model: got %q want llama-server-model-id", got)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"pong"}}],"usage":{"prompt_tokens":1,"completion_tokens":1}}`))
	}))
	defer upstream.Close()

	t.Cleanup(func() {
		fetchFreeModelsHook = nil
		fetchNvidiaFreeModelsHook = nil
		fetchCerebrasModelsHook = nil
		fetchOllamaCloudModelsHook = nil
	})
	fetchFreeModelsHook = func() ([]openRouterModel, error) { return nil, nil }
	fetchNvidiaFreeModelsHook = func() ([]nvidiaModel, error) { return nil, nil }
	fetchCerebrasModelsHook = func() ([]cerebrasModel, error) { return nil, nil }
	fetchOllamaCloudModelsHook = func() ([]ollamaModel, error) { return nil, nil }

	prevCfg := globalModelsConfig
	prevAllow := allowLocalOpenAI
	t.Cleanup(func() {
		configMutex.Lock()
		globalModelsConfig = prevCfg
		configMutex.Unlock()
		allowLocalOpenAI = prevAllow
	})
	configMutex.Lock()
	globalModelsConfig = modelsConfig{
		LocalOpenAI: []localOpenAIModel{{
			ID:       "local/qwen3-coder",
			Endpoint: upstream.URL,
			Model:    "llama-server-model-id",
		}},
	}
	configMutex.Unlock()
	allowLocalOpenAI = true

	payload := map[string]interface{}{
		"model":    "local/qwen3-coder",
		"messages": []map[string]interface{}{{"role": "user", "content": "ping"}},
	}
	b, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(b))
	rec := httptest.NewRecorder()
	handleChatCompletions(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status %d body %s", rec.Code, rec.Body.String())
	}
	if !strings.HasPrefix(upstreamAuth, "Bearer ") {
		t.Fatalf("expected Authorization Bearer (dummy key), got %q", upstreamAuth)
	}

	var out map[string]interface{}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	choices, _ := out["choices"].([]interface{})
	if len(choices) == 0 {
		t.Fatalf("no choices in %v", out)
	}
	ch0, _ := choices[0].(map[string]interface{})
	msg, _ := ch0["message"].(map[string]interface{})
	if msg["content"] != "pong" {
		t.Fatalf("choice content: got %v want pong", msg["content"])
	}
}

// TestHandleChatCompletions_LocalOpenAI_SkipBearerWhenAPIKeyEnvEmpty checks that when
// apiKeyEnv names an unset/empty env var, no Authorization header is sent (typical for plain llama-server).
func TestHandleChatCompletions_LocalOpenAI_SkipBearerWhenAPIKeyEnvEmpty(t *testing.T) {
	t.Setenv("FREERIDE_LOCAL_OPENAI_EMPTY_KEY", "")

	var upstreamAuth string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamAuth = r.Header.Get("Authorization")
		_, _ = io.Copy(io.Discard, r.Body)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"ok"}}]}`))
	}))
	defer upstream.Close()

	t.Cleanup(func() {
		fetchFreeModelsHook = nil
		fetchNvidiaFreeModelsHook = nil
		fetchCerebrasModelsHook = nil
		fetchOllamaCloudModelsHook = nil
	})
	fetchFreeModelsHook = func() ([]openRouterModel, error) { return nil, nil }
	fetchNvidiaFreeModelsHook = func() ([]nvidiaModel, error) { return nil, nil }
	fetchCerebrasModelsHook = func() ([]cerebrasModel, error) { return nil, nil }
	fetchOllamaCloudModelsHook = func() ([]ollamaModel, error) { return nil, nil }

	prevCfg := globalModelsConfig
	prevAllow := allowLocalOpenAI
	t.Cleanup(func() {
		configMutex.Lock()
		globalModelsConfig = prevCfg
		configMutex.Unlock()
		allowLocalOpenAI = prevAllow
	})
	configMutex.Lock()
	globalModelsConfig = modelsConfig{
		LocalOpenAI: []localOpenAIModel{{
			ID:        "local/no-auth",
			Endpoint:  upstream.URL,
			Model:     "m",
			APIKeyEnv: "FREERIDE_LOCAL_OPENAI_EMPTY_KEY",
		}},
	}
	configMutex.Unlock()
	allowLocalOpenAI = true

	b, _ := json.Marshal(map[string]interface{}{
		"model":    "local/no-auth",
		"messages": []map[string]interface{}{{"role": "user", "content": "x"}},
	})
	rec := httptest.NewRecorder()
	handleChatCompletions(rec, httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(b)))

	if rec.Code != http.StatusOK {
		t.Fatalf("status %d body %s", rec.Code, rec.Body.String())
	}
	if upstreamAuth != "" {
		t.Fatalf("expected no Authorization header, got %q", upstreamAuth)
	}
}
