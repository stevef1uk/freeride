package main

import (
	"encoding/json"
	"fmt"
	"log"
	"math"
	"net/url"
	"strconv"
	"strings"
)

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

func trackProviderWorkflowCost(rig, workflow, path string, payload []byte, candidate, endpoint string, catalog []openRouterModel, prices map[string]workflowTokenPrice) ([]byte, *workflowCostAccounting) {
	if workflow == "" {
		return payload, nil
	}
	if isOpenRouterEndpoint(endpoint) {
		return trackOpenRouterWorkflowCost(rig, workflow, path, payload, catalog)
	}
	price, priced := prices[candidate]
	if !priced {
		if slash := strings.Index(candidate, "/"); slash >= 0 {
			price, priced = prices[candidate[:slash]+"/*"]
		}
	}
	r := &workflowCostAccounting{path: path + ".usd", rig: rig, workflow: workflow}
	if !priced {
		r.unknownPricing = true
		log.Printf("[BUDGET] %s/%s model=%s: cost unknown; no workflowPricing configured. Gastown dollar limits cannot account for unpriced calls.", rig, workflow, candidate)
		return payload, r
	}
	if err := price.validate(); err != nil {
		r.unknownPricing = true
		log.Printf("[BUDGET] %s/%s model=%s: invalid workflowPricing: %v; cost unknown", rig, workflow, candidate, err)
		return payload, r
	}
	r.rates = &price
	var body map[string]interface{}
	if err := json.Unmarshal(payload, &body); err != nil {
		r.unknownPricing = true
		log.Printf("[BUDGET] %s/%s: cannot parse request for usage accounting: %v", rig, workflow, err)
		return payload, r
	}
	u, _ := url.Parse(endpoint)
	if body["stream"] == true && u != nil && (u.Hostname() == "api.cerebras.ai" || u.Hostname() == "api.groq.com") {
		options, _ := body["stream_options"].(map[string]interface{})
		if options == nil {
			options = map[string]interface{}{}
		}
		options["include_usage"] = true
		body["stream_options"] = options
		encoded, err := json.Marshal(body)
		if err == nil {
			return encoded, r
		}
	}
	return payload, r
}

func trackOpenRouterWorkflowCost(rig, workflow, path string, payload []byte, catalog []openRouterModel) ([]byte, *workflowCostAccounting) {
	r := &workflowCostAccounting{path: path + ".usd", rig: rig, workflow: workflow}
	var body map[string]interface{}
	if err := json.Unmarshal(payload, &body); err != nil {
		r.unknownPricing = true
		return payload, r
	}
	model, _ := body["model"].(string)
	if strings.HasSuffix(model, ":free") || model == "openrouter/free" {
		zero := 0.0
		r.rates = &workflowTokenPrice{Input: &zero, Output: &zero}
		return payload, r
	}
	for _, m := range catalog {
		if m.ID != model {
			continue
		}
		prompt, promptErr := strconv.ParseFloat(m.Pricing.Prompt, 64)
		completion, completionErr := strconv.ParseFloat(m.Pricing.Completion, 64)
		if promptErr == nil && completionErr == nil && prompt >= 0 && completion >= 0 && !math.IsNaN(prompt) && !math.IsNaN(completion) && !math.IsInf(prompt, 0) && !math.IsInf(completion, 0) {
			prompt *= 1000000
			completion *= 1000000
			r.rates = &workflowTokenPrice{Input: &prompt, Output: &completion}
			return payload, r
		}
		break
	}
	r.unknownPricing = true
	log.Printf("[BUDGET] %s/%s model=%s: cost unknown; no provider-reported cost or catalog price available", rig, workflow, model)
	return payload, r
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
