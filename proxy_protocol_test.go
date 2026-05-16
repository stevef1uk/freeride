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

// setupIsolatedLocalProxy configures hooks + localOpenAI so handleChatCompletions never hits the network.
func setupIsolatedLocalProxy(t *testing.T, upstreamURL string, modelID string) {
	t.Helper()
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

	resetProxyCooldownsForTest()

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
			ID:       modelID,
			Endpoint: upstreamURL,
			Model:    "upstream-model",
		}},
	}
	configMutex.Unlock()
	allowLocalOpenAI = true
}

func TestAnthropicToolDefinitions_Isolated(t *testing.T) {
	var gotTools bool
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]interface{}
		_ = json.NewDecoder(r.Body).Decode(&body)
		if tools, ok := body["tools"].([]interface{}); ok && len(tools) > 0 {
			gotTools = true
			t0, _ := tools[0].(map[string]interface{})
			fn, _ := t0["function"].(map[string]interface{})
			if fn["name"] != "write_file" {
				t.Errorf("tool name: got %v", fn["name"])
			}
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"ok\"}}]}\n\ndata: [DONE]\n\n"))
	}))
	defer upstream.Close()

	const modelID = "local/anthropic-tools"
	setupIsolatedLocalProxy(t, upstream.URL, modelID)

	payload := map[string]interface{}{
		"model": modelID,
		"messages": []map[string]interface{}{
			{"role": "user", "content": "Write a file"},
		},
		"tools": []map[string]interface{}{
			{
				"name":        "write_file",
				"description": "Writes content to a file",
				"input_schema": map[string]interface{}{
					"type": "object",
					"properties": map[string]interface{}{
						"filepath": map[string]interface{}{"type": "string"},
					},
				},
			},
		},
		"max_tokens": 100,
		"stream":     true,
	}
	b, _ := json.Marshal(payload)
	rec := httptest.NewRecorder()
	handleChatCompletions(rec, httptest.NewRequest(http.MethodPost, "/v1/messages", bytes.NewReader(b)))

	if rec.Code != http.StatusOK {
		t.Fatalf("status %d body %s", rec.Code, rec.Body.String())
	}
	if !gotTools {
		t.Fatal("expected Anthropic tools converted and forwarded to upstream")
	}
	if !strings.Contains(rec.Body.String(), "event: message_start") {
		t.Errorf("expected Anthropic SSE translation, body: %s", rec.Body.String())
	}
}

func TestLargeSystemPrompt_Isolated(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]interface{}
		_ = json.NewDecoder(r.Body).Decode(&body)
		msgs, _ := body["messages"].([]interface{})
		if len(msgs) == 0 {
			t.Error("expected messages after system conversion")
		}
		m0, _ := msgs[0].(map[string]interface{})
		if m0["role"] != "system" {
			t.Errorf("first message role: got %v", m0["role"])
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"hi\"}}]}\n\ndata: [DONE]\n\n"))
	}))
	defer upstream.Close()

	const modelID = "local/large-system"
	setupIsolatedLocalProxy(t, upstream.URL, modelID)

	systemPrompt := strings.Repeat("Follow these instructions carefully. ", 200)
	payload := map[string]interface{}{
		"model": modelID,
		"system": []map[string]interface{}{
			{"type": "text", "text": systemPrompt},
		},
		"messages": []map[string]interface{}{
			{"role": "user", "content": "Hello"},
		},
		"max_tokens": 100,
		"stream":     true,
	}
	b, _ := json.Marshal(payload)
	rec := httptest.NewRecorder()
	handleChatCompletions(rec, httptest.NewRequest(http.MethodPost, "/v1/messages", bytes.NewReader(b)))

	if rec.Code != http.StatusOK {
		t.Fatalf("status %d body %s", rec.Code, rec.Body.String())
	}
}

func TestInvalidToolCall_Isolated(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"ok\"}}]}\n\ndata: [DONE]\n\n"))
	}))
	defer upstream.Close()

	const modelID = "local/invalid-tool"
	setupIsolatedLocalProxy(t, upstream.URL, modelID)

	payload := map[string]interface{}{
		"model":    modelID,
		"messages": []map[string]interface{}{{"role": "user", "content": "Hello"}},
		"tools": []map[string]interface{}{
			{"name": "invalid_tool"},
		},
		"max_tokens": 100,
		"stream":     true,
	}
	b, _ := json.Marshal(payload)
	rec := httptest.NewRecorder()
	handleChatCompletions(rec, httptest.NewRequest(http.MethodPost, "/v1/messages", bytes.NewReader(b)))

	if rec.Code == http.StatusServiceUnavailable {
		t.Fatalf("unexpected 503 for invalid tool: %s", rec.Body.String())
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d body %s", rec.Code, rec.Body.String())
	}
}

func TestRoleBasedPrioritization_Isolated(t *testing.T) {
	const modelID = "local/role-big-70b"
	var upstreamCalled bool
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamCalled = true
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"ok"}}],"model":"upstream-model"}`))
	}))
	defer upstream.Close()

	t.Cleanup(func() {
		fetchFreeModelsHook = nil
		fetchNvidiaFreeModelsHook = nil
		fetchCerebrasModelsHook = nil
		fetchOllamaCloudModelsHook = nil
	})
	fetchFreeModelsHook = func() ([]openRouterModel, error) {
		return []openRouterModel{
			{ID: "test/small-free"},
			{ID: "test/free-big-70b"},
		}, nil
	}
	fetchNvidiaFreeModelsHook = func() ([]nvidiaModel, error) { return nil, nil }
	fetchCerebrasModelsHook = func() ([]cerebrasModel, error) { return nil, nil }
	fetchOllamaCloudModelsHook = func() ([]ollamaModel, error) { return nil, nil }
	resetProxyCooldownsForTest()

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
		ReliableFree: []string{"test/small-free", "test/free-big-70b"},
		LocalOpenAI: []localOpenAIModel{{
			ID:       modelID,
			Endpoint: upstream.URL,
			Model:    "upstream-model",
		}},
	}
	configMutex.Unlock()
	allowLocalOpenAI = true

	payload := map[string]interface{}{
		"model": "openrouter",
		"messages": []map[string]interface{}{
			{"role": "user", "content": "Hello"},
		},
		"max_tokens": 10,
	}
	b, _ := json.Marshal(payload)
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(b))
	req.Header.Set("X-GasTown-Role", "architect")
	rec := httptest.NewRecorder()
	handleChatCompletions(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status %d body %s", rec.Code, rec.Body.String())
	}
	if !upstreamCalled {
		t.Fatal("architect role should route to massive local fallback, upstream was not called")
	}
}
