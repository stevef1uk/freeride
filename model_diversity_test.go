package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
)

func isolateRoleModelHistory(t *testing.T) {
	t.Helper()
	roleModelHistory.Lock()
	previous := roleModelHistory.models
	roleModelHistory.models = make(map[string]map[string]bool)
	roleModelHistory.Unlock()
	t.Cleanup(func() {
		roleModelHistory.Lock()
		roleModelHistory.models = previous
		roleModelHistory.Unlock()
	})
}

func TestPreferDifferentRoleModels(t *testing.T) {
	isolateRoleModelHistory(t)
	previous := globalModelsConfig
	conf := modelsConfig{
		RolePreferDifferentFrom:   map[string][]string{"qa": {"polecat", "planner", "tester"}},
		RolePrependBeforeOriginal: []string{"polecat", "planner", "tester", "qa"},
		RolePrepend: map[string][]string{
			"polecat": {"openai/gpt-5.6-luna"},
			"planner": {"openai/gpt-5.6-luna"},
			"tester":  {"openai/gpt-5.6-luna"},
			"qa":      {"google/gemini-3.6-flash", "deepseek/deepseek-v4-flash", "openai/gpt-5.6-luna"},
		},
	}
	globalModelsConfig = conf
	t.Cleanup(func() { globalModelsConfig = previous })
	// A fallback actually used by the tester is shared, even though it isn't
	// that role's configured primary. Calls by QA and unrelated roles aren't.
	recordRoleModel("tester", "groq/openai/gpt-oss-120b", conf)
	recordRoleModel("qa", "google/gemini-3.6-flash", conf)
	recordRoleModel("mayor", "deepseek/deepseek-v4-flash", conf)
	for _, tt := range []struct {
		name       string
		role       string
		allowPaid  bool
		candidates []string
		want       []string
	}{
		{
			name: "priority tiers and provider aliases cannot bypass independence", role: "qa", allowPaid: true,
			candidates: []string{"openai/gpt-oss-120b:free", "openai/gpt-5.6-luna", "google/gemini-3.6-flash", "deepseek/deepseek-v4-flash"},
			want:       []string{"google/gemini-3.6-flash", "deepseek/deepseek-v4-flash", "openai/gpt-oss-120b:free", "openai/gpt-5.6-luna"},
		},
		{
			name: "curated independent choices lead unused provider priority", role: "qa", allowPaid: true,
			candidates: []string{"cerebras/qwen-3-235b", "openai/gpt-5.6-luna", "google/gemini-3.6-flash", "deepseek/deepseek-v4-flash"},
			want:       []string{"google/gemini-3.6-flash", "deepseek/deepseek-v4-flash", "cerebras/qwen-3-235b", "openai/gpt-5.6-luna"},
		},
		{
			name: "no alternative still permits shared model", role: "qa", allowPaid: true,
			candidates: []string{"openai/gpt-5.6-luna"}, want: []string{"openai/gpt-5.6-luna"},
		},
		{
			name: "other roles retain their ordering", role: "polecat", allowPaid: true,
			candidates: []string{"openai/gpt-5.6-luna", "google/gemini-3.6-flash"},
			want:       []string{"openai/gpt-5.6-luna", "google/gemini-3.6-flash"},
		},
		{
			name: "free mode still defers observed models", role: "qa",
			candidates: []string{"cerebras/gpt-oss-120b", "google/gemini-3.6-flash"},
			want:       []string{"google/gemini-3.6-flash", "cerebras/gpt-oss-120b"},
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			got, _ := preferDifferentRoleModels(tt.candidates, candidateContext{role: tt.role, conf: conf, allowPaid: tt.allowPaid})
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("candidates = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestQAIndependentModelBeforeSharedFallback(t *testing.T) {
	for _, failIndependent := range []bool{false, true} {
		name := "independent succeeds"
		if failIndependent {
			name = "shared model only after independent fails"
		}
		t.Run(name, func(t *testing.T) {
			isolateRoleModelHistory(t)
			var calls []string
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var body map[string]interface{}
				_ = json.NewDecoder(r.Body).Decode(&body)
				model, _ := body["model"].(string)
				calls = append(calls, model)
				if model == "independent" && failIndependent {
					http.Error(w, "temporarily unavailable", http.StatusServiceUnavailable)
					return
				}
				w.Header().Set("Content-Type", "application/json")
				_, _ = io.WriteString(w, `{"choices":[{"message":{"content":"review complete"}}]}`)
			}))
			defer upstream.Close()
			setupIsolatedLocalProxy(t, upstream.URL, "local/shared")
			configMutex.Lock()
			globalModelsConfig.LocalOpenAI[0].Model = "shared"
			// Even an explicitly priority-marked shared model must be deferred.
			globalModelsConfig.LocalOpenAI[0].Priority = true
			globalModelsConfig.LocalOpenAI = append(globalModelsConfig.LocalOpenAI, localOpenAIModel{
				ID: "local/independent", Endpoint: upstream.URL, Model: "independent",
			})
			globalModelsConfig.RolePreferDifferentFrom = map[string][]string{"qa": {"polecat"}}
			conf := globalModelsConfig
			configMutex.Unlock()
			request := func(role string) {
				req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(
					`{"model":"local/shared","messages":[{"role":"user","content":"review"}]}`))
				req.Header.Set("X-GasTown-Role", role)
				rec := httptest.NewRecorder()
				handleChatCompletions(rec, req)
				if rec.Code != http.StatusOK {
					t.Fatalf("%s status %d: %s", role, rec.Code, rec.Body.String())
				}
			}
			// Exercise actual success recording rather than seeding the history.
			request("polecat")
			calls = nil
			request("qa")
			want := []string{"independent"}
			if failIndependent {
				want = append(want, "shared")
			}
			if !reflect.DeepEqual(calls, want) {
				t.Fatalf("upstream calls = %v, want %v (policy %v)", calls, want, conf.RolePreferDifferentFrom)
			}
		})
	}
}
