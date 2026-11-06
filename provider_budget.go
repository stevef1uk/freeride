package main

import (
	"encoding/json"
	"fmt"
	"log"
	"math"
	"net/url"
	"strings"
)

// Full routing IDs (cerebras/..., groq/..., local/...) keep providers with
// different prices for the same underlying model separate. No guessed defaults.
type workflowTokenPrice struct {
	Input  *float64 `yaml:"inputUSDPerMillion"`
	Output *float64 `yaml:"outputUSDPerMillion"`
}

func (p workflowTokenPrice) validate() error {
	for _, rate := range []*float64{p.Input, p.Output} {
		if rate == nil || *rate < 0 || math.IsNaN(*rate) || math.IsInf(*rate, 0) || *rate > 1e6 {
			return fmt.Errorf("both inputUSDPerMillion and outputUSDPerMillion must be finite rates between 0 and 1000000; use explicit zero for free usage")
		}
	}
	return nil
}

func isOpenRouterEndpoint(endpoint string) bool {
	u, err := url.Parse(endpoint)
	if err != nil || u.Scheme != "https" {
		return false
	}
	return u.Hostname() == "openrouter.ai" || u.Hostname() == "eu.openrouter.ai" || u.Hostname() == "us.openrouter.ai"
}

func reserveProviderWorkflowBudget(rig, workflow, path string, payload []byte, candidate, endpoint string, catalog []openRouterModel, prices map[string]workflowTokenPrice) ([]byte, *workflowCostReservation, error) {
	if workflow == "" {
		return payload, nil, nil
	}
	if isOpenRouterEndpoint(endpoint) {
		return reserveOpenRouterWorkflowBudget(rig, workflow, path, payload, catalog)
	}
	price, priced := prices[candidate]
	if !priced {
		if slash := strings.Index(candidate, "/"); slash >= 0 {
			price, priced = prices[candidate[:slash]+"/*"]
		}
	}
	r := &workflowCostReservation{path: path + ".usd", rig: rig, workflow: workflow, unknownPricing: !priced}
	if !priced {
		if err := checkKnownWorkflowCost(path+".usd", rig, workflow); err != nil {
			return nil, nil, err
		}
		log.Printf("[BUDGET] %s/%s model=%s: cost unknown; no workflowPricing configured. Request limits and Gas Town token limits remain active; dollar allowance cannot bound unpriced calls.", rig, workflow, candidate)
		return payload, r, nil
	}
	if err := price.validate(); err != nil {
		return nil, nil, fmt.Errorf("execution budget: workflowPricing[%q]: %w", candidate, err)
	}
	var body map[string]interface{}
	if err := json.Unmarshal(payload, &body); err != nil {
		return nil, nil, fmt.Errorf("execution budget: invalid request")
	}
	// This pricing policy covers one text completion plus client-side functions.
	for _, key := range []string{"modalities", "plugins"} {
		if value, exists := body[key]; exists && value != nil {
			return nil, nil, fmt.Errorf("execution budget: priced direct requests do not support %s", key)
		}
	}
	for _, key := range []string{"n", "best_of"} {
		if n, ok := body[key]; ok && n != float64(1) {
			return nil, nil, fmt.Errorf("execution budget: multiple completions are not supported")
		}
	}
	if tools, ok := body["tools"].([]interface{}); ok {
		for _, tool := range tools {
			item, ok := tool.(map[string]interface{})
			if !ok || item["type"] != "function" {
				return nil, nil, fmt.Errorf("execution budget: priced direct requests support client-side function tools only")
			}
		}
	}
	if messages, ok := body["messages"].([]interface{}); ok {
		for _, message := range messages {
			m, ok := message.(map[string]interface{})
			if !ok {
				return nil, nil, fmt.Errorf("execution budget: invalid message")
			}
			if content := m["content"]; content != nil {
				if _, ok := content.(string); !ok {
					return nil, nil, fmt.Errorf("execution budget: priced direct requests support text content only")
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
	if _, hasMax := body["max_tokens"]; !hasMax {
		if _, hasCompletionMax := body["max_completion_tokens"]; !hasCompletionMax {
			body["max_tokens"] = completion
		}
	}
	// Ask compatible streaming APIs for their terminal usage chunk. This is a
	// provider adapter, not a requirement imposed on arbitrary local endpoints.
	u, _ := url.Parse(endpoint)
	if body["stream"] == true && u != nil && (u.Hostname() == "api.cerebras.ai" || u.Hostname() == "api.groq.com") {
		options, _ := body["stream_options"].(map[string]interface{})
		if options == nil {
			options = map[string]interface{}{}
		}
		options["include_usage"] = true
		body["stream_options"] = options
	}
	encoded, err := json.Marshal(body)
	if err != nil {
		return nil, nil, err
	}
	r.rates = &price
	r.units = int64(math.Ceil(float64(len(encoded))**price.Input + completion**price.Output))
	if r.units > 0 {
		workflowLimit, err := budgetFloat("FREERIDE_WORKFLOW_MAX_COST_USD", 5)
		if err != nil {
			return nil, nil, err
		}
		rigLimit, err := budgetFloat("FREERIDE_RIG_MAX_COST_USD", 20)
		if err != nil {
			return nil, nil, err
		}
		if err := reserveBudgetUnits(r.path, rig, workflow, r.units, int64(workflowLimit*1e6), int64(rigLimit*1e6)); err != nil {
			return nil, nil, formatCostBudgetError(err, rig, workflow)
		}
	}
	return encoded, r, nil
}

func accountingCostFromUsage(raw json.RawMessage, rates *workflowTokenPrice) (float64, string, bool) {
	var usage struct {
		Cost       *float64 `json:"cost"`
		Prompt     *float64 `json:"prompt_tokens"`
		Completion *float64 `json:"completion_tokens"`
	}
	if json.Unmarshal(raw, &usage) != nil {
		return 0, "", false
	}
	if usage.Cost != nil {
		return *usage.Cost, "reported", true
	}
	if rates != nil && usage.Prompt != nil && usage.Completion != nil && *usage.Prompt >= 0 && *usage.Completion >= 0 {
		return (*usage.Prompt**rates.Input + *usage.Completion**rates.Output) / 1e6, "estimated", true
	}
	return 0, "", false
}

// Expose unpriced coverage separately: a zero known-dollar total is not a claim
// that an unpriced provider was free. Token estimates are labelled estimates.
func budgetCoverageMode(reported, estimated, unknown int64) string {
	if unknown > 0 {
		return "cost unknown"
	}
	if estimated > 0 {
		return "estimated from token usage"
	}
	if reported > 0 {
		return "provider-reported cost"
	}
	return "no completed priced calls"
}

func budgetCoverageSummary(ledger map[string]int64, rig, workflow string) map[string]interface{} {
	r, e, u := ledger[budgetKey(rig, workflow, "reported")], ledger[budgetKey(rig, workflow, "estimated")], ledger[budgetKey(rig, workflow, "unknown")]
	return map[string]interface{}{"accounting": budgetCoverageMode(r, e, u), "reported_requests": r, "estimated_requests": e, "unknown_cost_requests": u}
}

func checkKnownWorkflowCost(path, rig, workflow string) error {
	workflowLimit, err := budgetFloat("FREERIDE_WORKFLOW_MAX_COST_USD", 5)
	if err != nil {
		return err
	}
	rigLimit, err := budgetFloat("FREERIDE_RIG_MAX_COST_USD", 20)
	if err != nil {
		return err
	}
	err = updateBudgetLedger(path, func(ledger map[string]int64) error {
		for _, item := range []struct {
			key   string
			limit int64
			scope string
		}{{budgetKey(rig, workflow), int64(workflowLimit * 1e6), workflow}, {budgetKey(rig), int64(rigLimit * 1e6), ""}} {
			used := ledger[item.key]
			if used < 0 {
				return fmt.Errorf("execution budget: negative reservation")
			}
			if used >= item.limit {
				return &budgetLimitError{rig: rig, workflow: item.scope, used: used, limit: item.limit}
			}
		}
		return nil
	})
	if err != nil {
		return formatCostBudgetError(err, rig, workflow)
	}
	return nil
}
