package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
)

func rate(v float64) *float64 { return &v }
func TestDirectProviderBudgetUsesSharedLedger(t *testing.T) {
	path := filepath.Join(t.TempDir(), "budget.json")
	prices := map[string]workflowTokenPrice{"cerebras/model": {Input: rate(1), Output: rate(2)}, "groq/*": {Input: rate(1), Output: rate(2)}}
	for _, provider := range []string{"cerebras", "groq"} {
		payload := []byte(`{"model":"model","messages":[{"role":"user","content":"hi"}],"max_tokens":100,"stream":true}`)
		encoded, reservation, err := reserveProviderWorkflowBudget("rig", "wf", path, payload, provider+"/model", map[string]string{"cerebras": "https://api.cerebras.ai/v1/chat/completions", "groq": "https://api.groq.com/openai/v1/chat/completions"}[provider], nil, prices)
		if err != nil {
			t.Fatal(err)
		}
		var body map[string]interface{}
		if err := json.Unmarshal(encoded, &body); err != nil {
			t.Fatal(err)
		}
		if _, exists := body["provider"]; exists {
			t.Fatal("OpenRouter-specific fields sent to direct provider")
		}
		if body["stream_options"].(map[string]interface{})["include_usage"] != true {
			t.Fatal("missing terminal usage request")
		}
		usage := `{"usage":{"prompt_tokens":100,"completion_tokens":10}}`
		if provider == "groq" {
			usage = `{"x_groq":{"usage":{"prompt_tokens":100,"completion_tokens":10}}}`
		}
		reservation.settleResponse([]byte(usage))
		reservation.finishAccounting()
		if mode := reservation.accountingMode(); mode != "estimated" {
			t.Fatal(mode)
		}
	}
	if got := readCostLedger(t, path)[budgetKey("rig", "wf")]; got != 240 {
		t.Fatalf("shared cost %d, want 240", got)
	}
}

func TestDirectReportedCostOverridesTokenEstimate(t *testing.T) {
	path := filepath.Join(t.TempDir(), "budget.json")
	_, r, err := reserveProviderWorkflowBudget("rig", "wf", path, []byte(`{"max_tokens":100}`), "cerebras/model", "https://api.cerebras.ai/v1/chat/completions", nil, map[string]workflowTokenPrice{"cerebras/model": {Input: rate(1), Output: rate(2)}})
	if err != nil {
		t.Fatal(err)
	}
	r.settleResponse([]byte(`{"usage":{"cost":0.00001,"prompt_tokens":1000,"completion_tokens":100}}`))
	if got := readCostLedger(t, path)[budgetKey("rig", "wf")]; got != 10 {
		t.Fatal(got)
	}
}

func TestUnknownProviderCostIsVisible(t *testing.T) {
	path := filepath.Join(t.TempDir(), "budget.json")
	t.Setenv("FREERIDE_EXECUTION_BUDGET_PATH", path)
	_, r, err := reserveProviderWorkflowBudget("rig", "wf", path, []byte(`{"max_tokens":100}`), "cerebras/unpriced", "https://api.cerebras.ai/v1/chat/completions", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	r.settleResponse([]byte(`{"usage":{"prompt_tokens":100,"completion_tokens":10}}`))
	r.finishAccounting()
	rec := httptest.NewRecorder()
	handleWorkflowBudgetStatus(rec, httptest.NewRequest("GET", "/api/budgets?rig=rig&workflow=wf", nil))
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"accounting":"cost unknown"`) || !strings.Contains(rec.Body.String(), `"unknown_cost_requests":1`) || !strings.Contains(rec.Body.String(), `"dollar_coverage_complete":false`) {
		t.Fatal(rec.Code, rec.Body.String())
	}
}

func TestExplicitFreeAndInvalidPricing(t *testing.T) {
	for _, tc := range []struct {
		price workflowTokenPrice
		valid bool
	}{{workflowTokenPrice{Input: rate(0), Output: rate(0)}, true}, {workflowTokenPrice{Input: rate(0)}, false}, {workflowTokenPrice{Input: rate(-1), Output: rate(1)}, false}} {
		path := filepath.Join(t.TempDir(), "budget.json")
		_, r, err := reserveProviderWorkflowBudget("rig", "wf", path, []byte(`{}`), "local/free", "http://localhost/v1/chat/completions", nil, map[string]workflowTokenPrice{"local/free": tc.price})
		if tc.valid != (err == nil) {
			t.Fatalf("valid=%t error=%v", tc.valid, err)
		}
		if tc.valid {
			r.settleResponse([]byte(`{}`))
			if r.accountingMode() != "estimated" {
				t.Fatal("explicit zero pricing was unknown")
			}
		}
	}
}

func TestDirectStreamingTokenUsageSettlement(t *testing.T) {
	path := filepath.Join(t.TempDir(), "budget.json")
	_, r, err := reserveProviderWorkflowBudget("rig", "wf", path, []byte(`{"max_tokens":100}`), "cerebras/model", "https://api.cerebras.ai/v1/chat/completions", nil, map[string]workflowTokenPrice{"cerebras/*": {Input: rate(1), Output: rate(2)}})
	if err != nil {
		t.Fatal(err)
	}
	stream := "data: {\"usage\":{\"prompt_tokens\":100,\"completion_tokens\":10}}\n\ndata: [DONE]\n\n"
	body := &budgetUsageBody{ReadCloser: io.NopCloser(strings.NewReader(stream)), reservation: r}
	got, err := io.ReadAll(body)
	if err != nil || string(got) != stream {
		t.Fatal(err, string(got))
	}
	if got := readCostLedger(t, path)[budgetKey("rig", "wf")]; got != 120 {
		t.Fatal(got)
	}
}

func TestFallbackCannotBypassSharedAllowance(t *testing.T) {
	t.Setenv("FREERIDE_WORKFLOW_MAX_COST_USD", "0.001")
	path := filepath.Join(t.TempDir(), "budget.json")
	if err := reserveBudgetUnits(path+".usd", "rig", "wf", 999, 1000, 1000); err != nil {
		t.Fatal(err)
	}
	_, _, err := reserveProviderWorkflowBudget("rig", "wf", path, []byte(`{"max_tokens":100}`), "cerebras/model", "https://api.cerebras.ai/v1/chat/completions", nil, map[string]workflowTokenPrice{"cerebras/*": {Input: rate(1), Output: rate(2)}})
	if err == nil {
		t.Fatal("provider switch bypassed workflow dollar allowance")
	}
}

func TestDirectProviderBudgetThroughProxy(t *testing.T) {
	path := filepath.Join(t.TempDir(), "budget.json")
	t.Setenv("FREERIDE_EXECUTION_BUDGET_PATH", path)
	setupIsolatedLocalProxy(t, "https://api.cerebras.ai", "local/direct-test")
	configMutex.Lock()
	globalModelsConfig.WorkflowPricing = map[string]workflowTokenPrice{"local/direct-test": {Input: rate(1), Output: rate(2)}}
	configMutex.Unlock()
	previous := http.DefaultClient
	t.Cleanup(func() { http.DefaultClient = previous })
	http.DefaultClient = &http.Client{Transport: budgetTestTransport(func(req *http.Request) (*http.Response, error) {
		if req.URL.Host != "api.cerebras.ai" {
			t.Fatalf("unexpected endpoint: %s", req.URL)
		}
		var payload map[string]interface{}
		if err := json.NewDecoder(req.Body).Decode(&payload); err != nil {
			t.Fatal(err)
		}
		if _, ok := payload["provider"]; ok {
			t.Fatal("OpenRouter price-cap field leaked to Cerebras")
		}
		body := `{"choices":[{"message":{"content":"ok"}}],"usage":{"prompt_tokens":100,"completion_tokens":10}}`
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}, nil
	})}
	req := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(`{"model":"local/direct-test","messages":[{"role":"user","content":"hi"}],"max_tokens":100}`))
	req.Header.Set("X-GasTown-Rig", "rig")
	req.Header.Set("X-GasTown-Workflow", "wf")
	rec := httptest.NewRecorder()
	handleChatCompletions(rec, req)
	if rec.Code != 200 || rec.Header().Get("X-Freeride-Cost-Accounting") != "estimated" {
		t.Fatalf("status=%d header=%s body=%s", rec.Code, rec.Header().Get("X-Freeride-Cost-Accounting"), rec.Body.String())
	}
	if got := readCostLedger(t, path)[budgetKey("rig", "wf")]; got != 120 {
		t.Fatalf("cost=%d", got)
	}
}
