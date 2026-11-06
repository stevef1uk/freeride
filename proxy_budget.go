package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
)

// reserveUpstreamAttempt bounds fallback amplification across proxy restarts.
// All upstream attempts count, even failed calls that might have been billed.
func reserveUpstreamAttempt(path, rig, workflow string, limit int64) error {
	return reserveBudgetUnits(path, rig, workflow, 1, limit)
}

func reserveBudgetUnits(path, rig, workflow string, units, limit int64, rigLimits ...int64) error {
	if limit <= 0 || units <= 0 {
		return fmt.Errorf("execution budget: allowance and reservation must be positive")
	}
	return updateBudgetLedger(path, func(ledger map[string]int64) error {
		key := budgetKey(rig, workflow)
		if ledger[key] < 0 {
			return fmt.Errorf("execution budget: negative reservation")
		}
		if units > limit-ledger[key] {
			return &budgetLimitError{rig: rig, workflow: workflow, used: ledger[key], requested: units, limit: limit}
		}
		if len(rigLimits) > 0 {
			rigKey := budgetKey(rig)
			if ledger[rigKey] < 0 {
				return fmt.Errorf("execution budget: negative rig reservation")
			}
			if units > rigLimits[0]-ledger[rigKey] {
				return &budgetLimitError{rig: rig, used: ledger[rigKey], requested: units, limit: rigLimits[0]}
			}
			ledger[rigKey] += units
		}
		ledger[key] += units
		return nil
	})
}

func budgetKey(parts ...string) string { key, _ := json.Marshal(parts); return string(key) }

type budgetLimitError struct {
	rig, workflow          string
	used, requested, limit int64
}

func (e *budgetLimitError) Error() string {
	return fmt.Sprintf("execution budget exhausted for %s/%s: %d used + %d requested exceeds %d limit", e.rig, e.workflow, e.used, e.requested, e.limit)
}

func updateBudgetLedger(path string, update func(map[string]int64) error) error {
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return fmt.Errorf("execution budget: %w", err)
	}
	lock, err := os.OpenFile(path+".lock", os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return fmt.Errorf("execution budget: %w", err)
	}
	defer lock.Close()
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX); err != nil {
		return fmt.Errorf("execution budget: %w", err)
	}
	defer syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)
	ledger := map[string]int64{}
	data, err := os.ReadFile(path)
	if err == nil {
		if err := json.Unmarshal(data, &ledger); err != nil {
			return fmt.Errorf("execution budget: corrupt proxy ledger: %w", err)
		}
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("execution budget: %w", err)
	}
	if ledger == nil {
		return fmt.Errorf("execution budget: null proxy ledger")
	}
	if err := update(ledger); err != nil {
		return err
	}
	data, err = json.Marshal(ledger)
	if err != nil {
		return fmt.Errorf("execution budget: %w", err)
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".proxy-budget-*")
	if err != nil {
		return fmt.Errorf("execution budget: %w", err)
	}
	defer os.Remove(f.Name())
	if _, err = f.Write(data); err != nil {
		f.Close()
		return fmt.Errorf("execution budget: %w", err)
	}
	if err = f.Sync(); err != nil {
		f.Close()
		return fmt.Errorf("execution budget: %w", err)
	}
	if err = f.Close(); err != nil {
		return fmt.Errorf("execution budget: %w", err)
	}
	if err = os.Rename(f.Name(), path); err != nil {
		return fmt.Errorf("execution budget: %w", err)
	}
	return nil
}

func admitWorkflowUpstream(rig, workflow string) error {
	// Non-workflow clients retain their existing behaviour. Gastown sends both
	// headers; the ledger is local accounting, not an authentication boundary.
	if workflow == "" {
		return nil
	}
	limit := int64(1000)
	if raw := os.Getenv("FREERIDE_WORKFLOW_MAX_UPSTREAM_ATTEMPTS"); raw != "" {
		n, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || n <= 0 {
			return fmt.Errorf("execution budget: invalid FREERIDE_WORKFLOW_MAX_UPSTREAM_ATTEMPTS")
		}
		limit = n
	}
	path, err := workflowBudgetPath()
	if err != nil {
		return err
	}
	return reserveUpstreamAttempt(path, rig, workflow, limit)
}

// applyOpenRouterWorkflowBudget reserves a conservative text-inference allowance
// before dispatch and constrains provider prices to those used for reservation.
// Reservations are not refunds or a substitute for provider account controls.
func applyOpenRouterWorkflowBudget(rig, workflow, path string, payload []byte, catalogs ...[]openRouterModel) ([]byte, error) {
	bounded, _, err := reserveOpenRouterWorkflowBudget(rig, workflow, path, payload, catalogs...)
	return bounded, err
}

func reserveOpenRouterWorkflowBudget(rig, workflow, path string, payload []byte, catalogs ...[]openRouterModel) ([]byte, *workflowCostReservation, error) {
	if workflow == "" {
		return payload, nil, nil
	}
	budget, err := budgetFloat("FREERIDE_WORKFLOW_MAX_COST_USD", 5)
	if err != nil {
		return nil, nil, err
	}
	rigBudget, err := budgetFloat("FREERIDE_RIG_MAX_COST_USD", 20)
	if err != nil {
		return nil, nil, err
	}
	promptPrice, err := budgetFloat("FREERIDE_MAX_PROMPT_USD_PER_MILLION", 1)
	if err != nil {
		return nil, nil, err
	}
	outputPrice, err := budgetFloat("FREERIDE_MAX_COMPLETION_USD_PER_MILLION", 5)
	if err != nil {
		return nil, nil, err
	}
	body := map[string]interface{}{}
	if err := json.Unmarshal(payload, &body); err != nil {
		return nil, nil, fmt.Errorf("execution budget: invalid payload")
	}
	model, _ := body["model"].(string)
	if strings.HasSuffix(model, ":free") || model == "openrouter/free" {
		promptPrice, outputPrice = 0, 0
	}
	if len(catalogs) > 0 {
		for _, m := range catalogs[0] {
			if m.ID == model {
				if price, e := strconv.ParseFloat(m.Pricing.Prompt, 64); e == nil && price >= 0 && !math.IsNaN(price) && !math.IsInf(price, 0) {
					promptPrice = math.Min(promptPrice, price*1000000)
				}
				if price, e := strconv.ParseFloat(m.Pricing.Completion, 64); e == nil && price >= 0 && !math.IsNaN(price) && !math.IsInf(price, 0) {
					outputPrice = math.Min(outputPrice, price*1000000)
				}
				break
			}
		}
	}
	for _, key := range []string{"n", "best_of"} {
		if n, exists := body[key]; exists && n != float64(1) {
			return nil, nil, fmt.Errorf("execution budget: multiple completions are not supported")
		}
	}
	if _, exists := body["modalities"]; exists {
		return nil, nil, fmt.Errorf("execution budget: media generation is not supported")
	}
	if tools, ok := body["tools"].([]interface{}); ok {
		for _, tool := range tools {
			item, ok := tool.(map[string]interface{})
			if !ok || item["type"] != "function" {
				return nil, nil, fmt.Errorf("execution budget: paid server tools are not supported")
			}
		}
	}
	// Cost accounting supports ordinary text plus client-side function schemas.
	// Decline media and paid provider plugins whose charges cannot be bounded here.
	if plugins, ok := body["plugins"]; ok && plugins != nil {
		return nil, nil, fmt.Errorf("execution budget: provider plugin charges are not supported")
	}
	if model, ok := body["model"].(string); ok && strings.Contains(model, ":online") {
		return nil, nil, fmt.Errorf("execution budget: online-search charges are not supported")
	}
	if msgs, ok := body["messages"].([]interface{}); ok {
		for _, msg := range msgs {
			m, ok := msg.(map[string]interface{})
			if !ok {
				return nil, nil, fmt.Errorf("execution budget: invalid message")
			}
			if content, exists := m["content"]; exists && content != nil {
				if _, ok := content.(string); !ok {
					return nil, nil, fmt.Errorf("execution budget: media content requires a separate cost policy")
				}
			}
		}
	}
	completion := float64(8192)
	for _, key := range []string{"max_tokens", "max_completion_tokens"} {
		if n, ok := body[key].(float64); ok {
			if n <= 0 || n > 8192 || math.IsNaN(n) || math.IsInf(n, 0) {
				return nil, nil, fmt.Errorf("execution budget: output allowance must be between 1 and 8192 tokens")
			}
			completion = n
		}
	}
	body["max_tokens"] = completion
	provider := map[string]interface{}{}
	if existing, ok := body["provider"].(map[string]interface{}); ok {
		for k, v := range existing {
			provider[k] = v
		}
	}
	// OpenRouter max_price prompt/completion units are dollars per million tokens.
	if existing, ok := provider["max_price"].(map[string]interface{}); ok {
		if price, ok := existing["prompt"].(float64); ok && price >= 0 {
			promptPrice = math.Min(promptPrice, price)
		}
		if price, ok := existing["completion"].(float64); ok && price >= 0 {
			outputPrice = math.Min(outputPrice, price)
		}
	}
	provider["max_price"] = map[string]interface{}{"prompt": promptPrice, "completion": outputPrice, "request": 0, "image": 0}
	body["provider"] = provider
	encoded, err := json.Marshal(body)
	if err != nil {
		return nil, nil, err
	}
	// Input byte count conservatively includes JSON/template overhead, rather than
	// trusting a model-specific tokenizer or a caller's token estimate.
	units := int64(math.Ceil(float64(len(encoded))*promptPrice + completion*outputPrice))
	if units == 0 {
		return encoded, nil, nil
	}
	limit := int64(math.Floor(budget * 1000000))
	if err := reserveBudgetUnits(path+".usd", rig, workflow, units, limit, int64(math.Floor(rigBudget*1000000))); err != nil {
		var exceeded *budgetLimitError
		if errors.As(err, &exceeded) {
			scope := "workflow"
			if exceeded.workflow == "" {
				scope = "rig"
			}
			return nil, nil, fmt.Errorf("execution budget exhausted: %s cost allowance $%.2f for %s/%s; accounted $%.6f plus next request reservation $%.6f (includes unsettled requests)", scope, float64(exceeded.limit)/1e6, rig, workflow, float64(exceeded.used)/1e6, float64(exceeded.requested)/1e6)
		}
		return nil, nil, err
	}
	return encoded, &workflowCostReservation{path: path + ".usd", rig: rig, workflow: workflow, units: units}, nil
}

func budgetFloat(name string, fallback float64) (float64, error) {
	raw := os.Getenv(name)
	if raw == "" {
		return fallback, nil
	}
	n, err := strconv.ParseFloat(raw, 64)
	if err != nil || n <= 0 || n > 1000000 || math.IsNaN(n) || math.IsInf(n, 0) {
		return 0, fmt.Errorf("execution budget: %s must be a positive finite amount <= 1000000", name)
	}
	return n, nil
}

func workflowBudgetPath() (string, error) {
	if path := os.Getenv("FREERIDE_EXECUTION_BUDGET_PATH"); path != "" {
		return path, nil
	}
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", fmt.Errorf("execution budget: %w", err)
	}
	return filepath.Join(dir, "freeride", "execution-budgets.json"), nil
}
