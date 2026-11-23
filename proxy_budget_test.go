package main

import (
	"encoding/json"
	"path/filepath"
	"testing"
)

func TestOpenRouterCostAccountingDoesNotMutatePayload(t *testing.T) {
	path := filepath.Join(t.TempDir(), "budget.json")
	payload := []byte(`{"model":"sample","messages":[{"role":"user","content":"hello"}],"max_tokens":9000,"n":2,"plugins":[],"provider":{"max_price":{"prompt":0.5}}}`)
	catalog := []openRouterModel{{ID: "sample"}}
	catalog[0].Pricing.Prompt = "0.000001"
	catalog[0].Pricing.Completion = "0.000002"
	tracked, accounting := trackOpenRouterWorkflowCost("rig", "wf", path, payload, catalog)
	if string(tracked) != string(payload) {
		t.Fatalf("proxy mutated payload: %s", tracked)
	}
	if accounting == nil || accounting.unknownPricing || accounting.rates == nil {
		t.Fatalf("missing accounting: %+v", accounting)
	}
	var body map[string]interface{}
	if err := json.Unmarshal(tracked, &body); err != nil {
		t.Fatal(err)
	}
	if prices := body["provider"].(map[string]interface{})["max_price"].(map[string]interface{}); prices["prompt"] != 0.5 {
		t.Fatalf("caller price cap was changed: %v", prices)
	}
	if err := accounting.recordCost(0.00001); err != nil {
		t.Fatal(err)
	}
	if got := readCostLedger(t, path)[budgetKey("rig", "wf")]; got != 10 {
		t.Fatalf("accounted %d, want 10", got)
	}
}

func TestOpenRouterFreeRouteAccountsZero(t *testing.T) {
	path := filepath.Join(t.TempDir(), "budget.json")
	_, accounting := trackOpenRouterWorkflowCost("rig", "wf", path, []byte(`{"model":"sample:free"}`), nil)
	if accounting == nil || accounting.rates == nil {
		t.Fatalf("free route not priced: %+v", accounting)
	}
	accounting.accountResponse([]byte(`{}`))
	if got := readCostLedger(t, path)[budgetKey("rig", "wf")]; got != 0 {
		t.Fatalf("free route accounted %d, want 0", got)
	}
}

func TestOpenRouterCatalogPricesAreUsedForEstimates(t *testing.T) {
	path := filepath.Join(t.TempDir(), "budget.json")
	catalog := []openRouterModel{{ID: "sample"}}
	catalog[0].Pricing.Prompt = "0.000001"
	catalog[0].Pricing.Completion = "0.000002"
	_, accounting := trackOpenRouterWorkflowCost("rig", "wf", path, []byte(`{"model":"sample"}`), catalog)
	if accounting == nil || accounting.rates == nil {
		t.Fatalf("catalog price not used: %+v", accounting)
	}
	accounting.accountResponse([]byte(`{"usage":{"prompt_tokens":100,"completion_tokens":10}}`))
	if got := readCostLedger(t, path)[budgetKey("rig", "wf")]; got != 120 {
		t.Fatalf("accounted %d, want 120", got)
	}
}
