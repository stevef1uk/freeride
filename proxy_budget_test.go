package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestOpenRouterCostReservationAndProviderCaps(t *testing.T) {
	t.Setenv("FREERIDE_WORKFLOW_MAX_COST_USD", "0.001")
	t.Setenv("FREERIDE_MAX_PROMPT_USD_PER_MILLION", "1")
	t.Setenv("FREERIDE_MAX_COMPLETION_USD_PER_MILLION", "5")
	path := filepath.Join(t.TempDir(), "budget.json")
	payload := []byte(`{"model":"sample","messages":[{"role":"user","content":"hello"}],"max_tokens":100,"provider":{"max_price":{"prompt":0.5}}}`)
	bounded, err := applyOpenRouterWorkflowBudget("rig", "wf", path, payload)
	if err != nil {
		t.Fatal(err)
	}
	var body map[string]interface{}
	if err := json.Unmarshal(bounded, &body); err != nil {
		t.Fatal(err)
	}
	prices := body["provider"].(map[string]interface{})["max_price"].(map[string]interface{})
	if prices["prompt"] != 0.5 || prices["completion"] != float64(5) || prices["request"] != float64(0) {
		t.Fatalf("caps=%v", prices)
	}
	if _, err := applyOpenRouterWorkflowBudget("rig", "wf", path, payload); err == nil {
		t.Fatal("second reservation exceeded dollar envelope")
	}
	if _, err := applyOpenRouterWorkflowBudget("rig", "wf", path, []byte(`{"model":"sample:free","messages":[],"max_tokens":100}`)); err != nil {
		t.Fatalf("free route consumed dollar allowance: %v", err)
	}
}

func TestOpenRouterWorkflowDollarOverride(t *testing.T) {
	t.Setenv("FREERIDE_WORKFLOW_MAX_COST_USD", "0.001")
	path := filepath.Join(t.TempDir(), "budget.json")
	if err := os.WriteFile(path+".limits", []byte(`{"[\"rig\",\"wf\"]":10000000}`), 0600); err != nil {
		t.Fatal(err)
	}
	payload := []byte(`{"model":"sample","messages":[{"role":"user","content":"hello"}],"max_tokens":8192}`)
	if _, err := applyOpenRouterWorkflowBudget("rig", "wf", path, payload); err != nil {
		t.Fatalf("workflow override was not applied: %v", err)
	}
	if _, err := applyOpenRouterWorkflowBudget("rig", "wf", path, payload); err != nil {
		t.Fatalf("override was not applied to subsequent reservations: %v", err)
	}
}

func TestOpenRouterCostPolicyRejectsUnaccountedCharges(t *testing.T) {
	for _, payload := range []string{
		`{"n":2}`, `{"plugins":[]}`, `{"messages":[{"content":[{"type":"image_url"}]}]}`, `{"tools":[{"type":"web_search"}]}`, `{"max_tokens":9000}`,
	} {
		if _, err := applyOpenRouterWorkflowBudget("rig", "wf", filepath.Join(t.TempDir(), "budget.json"), []byte(payload)); err == nil {
			t.Fatalf("unbounded payload admitted: %s", payload)
		}
	}
}

func TestDollarAllowanceSharedAcrossWorkflowsOnSameRig(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cost.json")
	if err := reserveBudgetUnits(path, "rig", "wf-1", 60, 100, 100); err != nil {
		t.Fatal(err)
	}
	if reserveBudgetUnits(path, "rig", "wf-2", 60, 100, 100) == nil {
		t.Fatal("new workflow bypassed rig allowance")
	}
	if err := reserveBudgetUnits(path, "other", "wf-2", 60, 100, 100); err != nil {
		t.Fatal(err)
	}
}
