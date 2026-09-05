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

func TestCloudflareDirectRouting_Isolated(t *testing.T) {
	var gotModel, gotAuth string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/chat/completions") {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		gotAuth = r.Header.Get("Authorization")
		var body map[string]interface{}
		_ = json.NewDecoder(r.Body).Decode(&body)
		if m, ok := body["model"].(string); ok {
			gotModel = m
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"ok"}}],"model":"@cf/meta/llama-3.3-70b-instruct-fp8-fast"}`))
	}))
	defer upstream.Close()

	t.Setenv("CLOUDFLARE_ACCOUNT_ID", "test-account-id")
	t.Setenv("CLOUDFLARE_API_TOKEN", "test-api-token")

	// Point Cloudflare base at mock server for this test only.
	prevBase := cloudflareOpenAIBaseURL
	cloudflareOpenAIBaseURL = strings.TrimSuffix(upstream.URL, "/test-account-id/ai/v1")
	if cloudflareOpenAIBaseURL == strings.TrimSuffix(upstream.URL, "/") {
		// Mock server URL doesn't include account path, adjust
		cloudflareOpenAIBaseURL = strings.TrimSuffix(upstream.URL, "/")
	}
	t.Cleanup(func() {
		cloudflareOpenAIBaseURL = prevBase
	})

	t.Cleanup(func() {
		fetchFreeModelsHook = nil
		fetchNvidiaFreeModelsHook = nil
		fetchCerebrasModelsHook = nil
		fetchOllamaCloudModelsHook = nil
		_ = os.Unsetenv("CLOUDFLARE_ACCOUNT_ID")
		_ = os.Unsetenv("CLOUDFLARE_API_TOKEN")
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
		CloudflareModels: []cloudflareModel{{
			ID:    "cloudflare/llama-3.3-70b",
			Model: "@cf/meta/llama-3.3-70b-instruct-fp8-fast",
		}},
	}
	configMutex.Unlock()

	const modelID = "cloudflare/llama-3.3-70b"
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

	if gotModel != "@cf/meta/llama-3.3-70b-instruct-fp8-fast" {
		t.Errorf("upstream model = %q, want @cf/meta/llama-3.3-70b-instruct-fp8-fast", gotModel)
	}
	if gotAuth != "Bearer test-api-token" {
		t.Errorf("upstream auth = %q, want Bearer test-api-token", gotAuth)
	}
}

func TestCloudflareDirectDisabled_NoCreds(t *testing.T) {
	// Without CLOUDFLARE_ACCOUNT_ID, Cloudflare direct should not route
	t.Setenv("CLOUDFLARE_API_TOKEN", "test-token")
	_ = os.Unsetenv("CLOUDFLARE_ACCOUNT_ID")

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

	if cloudflareDirectAvailable() {
		t.Fatal("cloudflareDirectAvailable() should be false without CLOUDFLARE_ACCOUNT_ID")
	}
}

func TestCloudflareDirectDisabled_NoModels(t *testing.T) {
	t.Setenv("CLOUDFLARE_ACCOUNT_ID", "test-account")
	t.Setenv("CLOUDFLARE_API_TOKEN", "test-token")

	t.Cleanup(func() {
		configMutex.Lock()
		globalModelsConfig = modelsConfig{}
		configMutex.Unlock()
	})
	configMutex.Lock()
	globalModelsConfig = modelsConfig{
		CloudflareModels: nil, // empty
	}
	configMutex.Unlock()

	if cloudflareDirectAvailable() {
		t.Fatal("cloudflareDirectAvailable() should be false without CloudflareModels config")
	}
}

func TestCloudflareDirectEnabled(t *testing.T) {
	t.Setenv("CLOUDFLARE_ACCOUNT_ID", "test-account")
	t.Setenv("CLOUDFLARE_API_TOKEN", "test-token")

	t.Cleanup(func() {
		configMutex.Lock()
		globalModelsConfig = modelsConfig{}
		configMutex.Unlock()
	})
	configMutex.Lock()
	globalModelsConfig = modelsConfig{
		CloudflareModels: []cloudflareModel{{
			ID:    "cloudflare/llama-3.3-70b",
			Model: "@cf/meta/llama-3.3-70b-instruct-fp8-fast",
		}},
	}
	configMutex.Unlock()

	if !cloudflareDirectAvailable() {
		t.Fatal("cloudflareDirectAvailable() should be true with valid config and credentials")
	}
}

func TestLookupCloudflareModel(t *testing.T) {
	t.Setenv("CLOUDFLARE_ACCOUNT_ID", "test-account")
	t.Setenv("CLOUDFLARE_API_TOKEN", "test-token")

	t.Cleanup(func() {
		configMutex.Lock()
		globalModelsConfig = modelsConfig{}
		configMutex.Unlock()
	})
	configMutex.Lock()
	globalModelsConfig = modelsConfig{
		CloudflareModels: []cloudflareModel{
			{ID: "cloudflare/llama-3.3-70b", Model: "@cf/meta/llama-3.3-70b-instruct-fp8-fast"},
			{ID: "cloudflare/qwen-2.5-coder-32b", Model: "@cf/qwen/qwen2.5-coder-32b-instruct"},
		},
	}
	configMutex.Unlock()

	// Found
	m := lookupCloudflareModel("cloudflare/llama-3.3-70b")
	if m == nil {
		t.Fatal("expected to find cloudflare/llama-3.3-70b")
	}
	if m.Model != "@cf/meta/llama-3.3-70b-instruct-fp8-fast" {
		t.Errorf("model = %q, want @cf/meta/llama-3.3-70b-instruct-fp8-fast", m.Model)
	}

	// Found second model
	m2 := lookupCloudflareModel("cloudflare/qwen-2.5-coder-32b")
	if m2 == nil {
		t.Fatal("expected to find cloudflare/qwen-2.5-coder-32b")
	}

	// Not found
	m3 := lookupCloudflareModel("cloudflare/nonexistent")
	if m3 != nil {
		t.Errorf("expected nil for nonexistent model, got %+v", m3)
	}
}

func TestCloudflareAPIModelName(t *testing.T) {
	// With configured model
	m := &cloudflareModel{Model: "@cf/meta/llama-3.3-70b-instruct-fp8-fast"}
	name := cloudflareAPIModelName(m, "cloudflare/llama-3.3-70b")
	if name != "@cf/meta/llama-3.3-70b-instruct-fp8-fast" {
		t.Errorf("cloudflareAPIModelName() = %q, want @cf/meta/llama-3.3-70b-instruct-fp8-fast", name)
	}

	// With empty model, falls back to stripping prefix
	name2 := cloudflareAPIModelName(nil, "cloudflare/llama-3.3-70b")
	if name2 != "llama-3.3-70b" {
		t.Errorf("cloudflareAPIModelName(nil) = %q, want llama-3.3-70b", name2)
	}
}

func TestCloudflareBaseURL(t *testing.T) {
	// No account ID
	_ = os.Unsetenv("CLOUDFLARE_ACCOUNT_ID")
	if url := cloudflareBaseURL(); url != "" {
		t.Errorf("cloudflareBaseURL() = %q, want empty", url)
	}

	// With account ID
	t.Setenv("CLOUDFLARE_ACCOUNT_ID", "my-account-123")
	want := "https://api.cloudflare.com/client/v4/accounts/my-account-123/ai/v1"
	if url := cloudflareBaseURL(); url != want {
		t.Errorf("cloudflareBaseURL() = %q, want %q", url, want)
	}
}

func TestIsCloudflareDirectModelID(t *testing.T) {
	t.Setenv("CLOUDFLARE_ACCOUNT_ID", "test-account")
	t.Setenv("CLOUDFLARE_API_TOKEN", "test-token")

	t.Cleanup(func() {
		configMutex.Lock()
		globalModelsConfig = modelsConfig{}
		configMutex.Unlock()
	})
	configMutex.Lock()
	globalModelsConfig = modelsConfig{
		CloudflareModels: []cloudflareModel{{
			ID: "cloudflare/llama-3.3-70b",
		}},
	}
	configMutex.Unlock()

	if !isCloudflareDirectModelID("cloudflare/llama-3.3-70b") {
		t.Error("expected cloudflare/llama-3.3-70b to be a Cloudflare direct model")
	}
	if isCloudflareDirectModelID("openrouter/some-model") {
		t.Error("expected openrouter/some-model to NOT be a Cloudflare direct model")
	}
}
