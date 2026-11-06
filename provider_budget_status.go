package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
)

func handleWorkflowBudgetStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", "GET")
		http.Error(w, "GET required", http.StatusMethodNotAllowed)
		return
	}
	rig, workflow := r.URL.Query().Get("rig"), r.URL.Query().Get("workflow")
	if rig == "" || workflow == "" {
		http.Error(w, "rig and workflow query parameters required", http.StatusBadRequest)
		return
	}
	path, err := workflowBudgetPath()
	if err != nil {
		http.Error(w, err.Error(), 503)
		return
	}
	read := func(path string) (map[string]int64, error) {
		ledger := map[string]int64{}
		data, err := os.ReadFile(path)
		if os.IsNotExist(err) {
			return ledger, nil
		}
		if err != nil {
			return nil, err
		}
		if err = json.Unmarshal(data, &ledger); err != nil {
			return nil, err
		}
		if ledger == nil {
			return nil, fmt.Errorf("null budget ledger")
		}
		for _, value := range ledger {
			if value < 0 {
				return nil, fmt.Errorf("negative budget ledger value")
			}
		}
		return ledger, nil
	}
	dollars, err := read(path + ".usd")
	if err != nil {
		http.Error(w, "cannot read cost ledger", 503)
		return
	}
	coverage, err := read(path + ".usd.coverage")
	if err != nil {
		http.Error(w, "cannot read accounting coverage", 503)
		return
	}
	attempts, err := read(path)
	if err != nil {
		http.Error(w, "cannot read attempt ledger", 503)
		return
	}
	status := budgetCoverageSummary(coverage, rig, workflow)
	status["rig"] = rig
	status["workflow"] = workflow
	status["accounted_cost_usd"] = float64(dollars[budgetKey(rig, workflow)]) / 1e6
	status["upstream_attempts"] = attempts[budgetKey(rig, workflow)]
	attemptLimit, err := workflowUpstreamAttemptLimit()
	if err != nil {
		http.Error(w, err.Error(), 503)
		return
	}
	status["upstream_attempt_limit"] = attemptLimit

	if status["accounting"] == "no completed priced calls" && dollars[budgetKey(rig, workflow)] > 0 {
		status["accounting"] = "legacy accounted cost; provider coverage history unavailable"
	}
	limit, err := budgetFloat("FREERIDE_WORKFLOW_MAX_COST_USD", 5)
	if err != nil {
		http.Error(w, err.Error(), 503)
		return
	}
	status["workflow_allowance_usd"] = limit
	rigLimit, err := budgetFloat("FREERIDE_RIG_MAX_COST_USD", 20)
	if err != nil {
		http.Error(w, err.Error(), 503)
		return
	}
	status["rig_allowance_usd"] = rigLimit
	status["rig_accounted_cost_usd"] = float64(dollars[budgetKey(rig)]) / 1e6

	status["dollar_coverage_complete"] = status["unknown_cost_requests"] == int64(0) && status["accounting"] != "legacy accounted cost; provider coverage history unavailable"
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(status)
}
