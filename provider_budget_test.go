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

func TestDirectProviderAccountingUsesSharedLedger(t *testing.T) {
	path := filepath.Join(t.TempDir(), "budget.json")
	prices := map[string]workflowTokenPrice{"cerebras/model": {Input: rate(1), Output: rate(2)}, "groq/*": {Input: rate(1), Output: rate(2)}}
	for _, provider := range []string{"cerebras", "groq"} {
		payload := []byte(`{"model":"model","messages":[{"role":"user","content":"hi"}],"max_tokens":100,"stream":true}`)
		encoded, accounting := trackProviderWorkflowCost("rig", "wf", path, payload, provider+"/model", map[string]string{"cerebras": "https://api.cerebras.ai/v1/chat/completions", "groq": "https://api.groq.com/openai/v1/chat/completions"}[provider], nil, prices)
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
		accounting.accountResponse([]byte(usage))
		accounting.finishAccounting()
		if mode := accounting.accountingMode(); mode != "estimated" {
			t.Fatal(mode)
		}
	}
	if got := readCostLedger(t, path)[budgetKey("rig", "wf")]; got != 240 {
		t.Fatalf("shared cost %d, want 240", got)
	}
}

func TestDirectReportedCostOverridesTokenEstimate(t *testing.T) {
	path := filepath.Join(t.TempDir(), "budget.json")
	_, accounting := trackProviderWorkflowCost("rig", "wf", path, []byte(`{"max_tokens":100}`), "cerebras/model", "https://api.cerebras.ai/v1/chat/completions", nil, map[string]workflowTokenPrice{"cerebras/model": {Input: rate(1), Output: rate(2)}})
	accounting.accountResponse([]byte(`{"usage":{"cost":0.00001,"prompt_tokens":1000,"completion_tokens":100}}`))
	if got := readCostLedger(t, path)[budgetKey("rig", "wf")]; got != 10 {
		t.Fatal(got)
	}
}

func TestProxyDoesNotEnforcePayloadOrPricePolicy(t *testing.T) {
	path := filepath.Join(t.TempDir(), "budget.json")
	payload := []byte(`{"n":2,"plugins":[],"modalities":["image"],"tools":[{"type":"web_search"}],"messages":[{"content":[{"type":"image_url"}]}],"max_tokens":9000}`)
	tracked, accounting := trackProviderWorkflowCost("rig", "wf", path, payload, "cerebras/model", "https://api.cerebras.ai/v1/chat/completions", nil, map[string]workflowTokenPrice{"cerebras/model": {Input: rate(1), Output: rate(2)}})
	if string(tracked) != string(payload) {
		t.Fatalf("proxy mutated payload: %s", tracked)
	}
	if accounting == nil || accounting.unknownPricing {
		t.Fatalf("valid pricing marked unknown: %+v", accounting)
	}
}

func TestUnknownProviderCostIsVisible(t *testing.T) {
	path := filepath.Join(t.TempDir(), "budget.json")
	t.Setenv("FREERIDE_EXECUTION_BUDGET_PATH", path)
	_, accounting := trackProviderWorkflowCost("rig", "wf", path, []byte(`{"max_tokens":100}`), "cerebras/unpriced", "https://api.cerebras.ai/v1/chat/completions", nil, nil)
	accounting.accountResponse([]byte(`{"usage":{"prompt_tokens":100,"completion_tokens":10}}`))
	accounting.finishAccounting()
	rec := httptest.NewRecorder()
	handleWorkflowBudgetStatus(rec, httptest.NewRequest("GET", "/api/budgets?rig=rig&workflow=wf", nil))
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"accounting":"cost unknown"`) || !strings.Contains(rec.Body.String(), `"unknown_cost_requests":1`) || !strings.Contains(rec.Body.String(), `"dollar_coverage_complete":false`) {
		t.Fatal(rec.Code, rec.Body.String())
	}
}

func TestInvalidPricingIsAccountingUnknownNotAnError(t *testing.T) {
	for _, price := range []workflowTokenPrice{{Input: rate(0)}, {Input: rate(-1), Output: rate(1)}} {
		path := filepath.Join(t.TempDir(), "budget.json")
		payload := []byte(`{}`)
		tracked, accounting := trackProviderWorkflowCost("rig", "wf", path, payload, "local/free", "http://localhost/v1/chat/completions", nil, map[string]workflowTokenPrice{"local/free": price})
		if string(tracked) != string(payload) || accounting == nil || !accounting.unknownPricing {
			t.Fatalf("pricing rejected request: tracked=%s accounting=%+v", tracked, accounting)
		}
	}
}

func TestExplicitFreeRouteAccountsZero(t *testing.T) {
	path := filepath.Join(t.TempDir(), "budget.json")
	_, accounting := trackProviderWorkflowCost("rig", "wf", path, []byte(`{}`), "local/free", "http://localhost/v1/chat/completions", nil, map[string]workflowTokenPrice{"local/free": {Input: rate(0), Output: rate(0)}})
	accounting.accountResponse([]byte(`{}`))
	if accounting.accountingMode() != "estimated" {
		t.Fatal(accounting.accountingMode())
	}
	if got := readCostLedger(t, path)[budgetKey("rig", "wf")]; got != 0 {
		t.Fatalf("free route accounted %d", got)
	}
}

func TestDirectStreamingTokenUsageAccounting(t *testing.T) {
	path := filepath.Join(t.TempDir(), "budget.json")
	_, accounting := trackProviderWorkflowCost("rig", "wf", path, []byte(`{"max_tokens":100}`), "cerebras/model", "https://api.cerebras.ai/v1/chat/completions", nil, map[string]workflowTokenPrice{"cerebras/*": {Input: rate(1), Output: rate(2)}})
	stream := "data: {\"usage\":{\"prompt_tokens\":100,\"completion_tokens\":10}}\n\ndata: [DONE]\n\n"
	body := &usageAccountingBody{ReadCloser: io.NopCloser(strings.NewReader(stream)), accounting: accounting}
	got, err := io.ReadAll(body)
	if err != nil || string(got) != stream {
		t.Fatal(err, string(got))
	}
	if got := readCostLedger(t, path)[budgetKey("rig", "wf")]; got != 120 {
		t.Fatal(got)
	}
}

func TestDirectProviderAccountingThroughProxy(t *testing.T) {
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
	req := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(`{"model":"local/direct-test","messages":[{"role":"user","content":"hi"}],"max_tokens":100,"plugins":[],"modalities":["image"]}`))
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

func TestBudgetStatusIncludesRigAccountingWithoutAllowances(t *testing.T) {
	path := filepath.Join(t.TempDir(), "budget.json")
	t.Setenv("FREERIDE_EXECUTION_BUDGET_PATH", path)
	if err := updateBudgetLedger(path+".usd", func(ledger map[string]int64) error {
		ledger[budgetKey("rig", "wf")] = 480000
		ledger[budgetKey("rig")] = 1250000
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	handleWorkflowBudgetStatus(rec, httptest.NewRequest("GET", "/api/budgets?rig=rig&workflow=wf", nil))
	var status map[string]interface{}
	if err := json.Unmarshal(rec.Body.Bytes(), &status); err != nil {
		t.Fatal(err)
	}
	if rec.Code != 200 || status["rig_accounted_cost_usd"] != 1.25 || status["accounted_cost_usd"] != 0.48 {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
	for _, key := range []string{"rig_allowance_usd", "workflow_allowance_usd"} {
		if _, exists := status[key]; exists {
			t.Fatalf("status still reports limit %s", key)
		}
	}
}
