package main

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"io/ioutil"
	"log"
	"net/http"
	"os"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
)

type openRouterModel struct {
	ID            string `json:"id"`
	Name          string `json:"name"`
	ContextLength int    `json:"context_length"`
	Pricing       struct {
		Prompt     string `json:"prompt"`
		Completion string `json:"completion"`
	} `json:"pricing"`
	SupportedParameters []string `json:"supported_parameters"`
	Created             int64    `json:"created"`
}

type nvidiaModel struct {
	ID         string      `json:"id"`
	Object     string      `json:"object"`
	Created    int         `json:"created"`
	OwnedBy    string      `json:"owned_by"`
	Permission interface{} `json:"permission"`
	// Track tool/support capability
	SupportsTools bool `json:"-"`
}

type ollamaModelDetails struct {
	Format            string   `json:"format"`
	Family            string   `json:"family"`
	Families          []string `json:"families"`
	ParameterSize     string   `json:"parameter_size"`
	QuantizationLevel string   `json:"quantization_level"`
}

type ollamaModel struct {
	Name       string             `json:"name"`
	Model      string             `json:"model"`
	ModifiedAt string             `json:"modified_at"`
	Size       int64              `json:"size"`
	Digest     string             `json:"digest"`
	Details    ollamaModelDetails `json:"details"`
}

type ollamaTagsResponse struct {
	Models []ollamaModel `json:"models"`
}

var (
	cachedFreeModels   []openRouterModel
	cachedNvidiaModels []nvidiaModel
	cacheMutex         sync.RWMutex
	cacheTime          time.Time
	cacheTTL           = 1 * time.Hour

	cooldowns  = make(map[string]*cooldownEntry)
	cooldownMu sync.RWMutex

	debugMode bool
	toolRegex = regexp.MustCompile("(?s)<invoke name=\"([^\"]+)\">(.*?)</invoke>")
	paramRegex = regexp.MustCompile("(?s)<parameter name=\"([^\"]+)\">(.*?)</parameter>")
)

type cooldownEntry struct {
	ErrorCount  int       `json:"error_count"`
	CooldownEnd time.Time `json:"cooldown_end"`
}

const cooldownsFile = "cooldowns.json"

func saveCooldowns() {
	active := make(map[string]*cooldownEntry)
	now := time.Now()
	for k, v := range cooldowns {
		if v.ErrorCount > 0 && now.Before(v.CooldownEnd) {
			active[k] = v
		}
	}
	data, err := json.MarshalIndent(active, "", "  ")
	if err == nil {
		ioutil.WriteFile(cooldownsFile, data, 0644)
	}
}

func loadCooldowns() {
	cooldownMu.Lock()
	defer cooldownMu.Unlock()
	data, err := ioutil.ReadFile(cooldownsFile)
	if err != nil {
		return
	}
	var loaded map[string]*cooldownEntry
	if err := json.Unmarshal(data, &loaded); err == nil {
		now := time.Now()
		for k, v := range loaded {
			if v.ErrorCount > 0 && now.Before(v.CooldownEnd) {
				cooldowns[k] = v
			}
		}
	}
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func calculateStandardCooldown(errorCount int) time.Duration {
	// More aggressive recovery - shorter cooldowns
	// Error 1: 10s, Error 2: 30s, Error 3+: 60s max
	n := max(1, errorCount)
	if n == 1 {
		return 10 * time.Second
	} else if n == 2 {
		return 30 * time.Second
	}
	return 60 * time.Second // cap at 1 minute
}

// ... fetchFreeModels, scoreModel, handleTags, handleVersion, handleOllamaChat existing ...

func fetchFreeModels() ([]openRouterModel, error) {
	log.Printf("[DEBUG] fetchFreeModels called")
	cacheMutex.RLock()
	if time.Since(cacheTime) < cacheTTL && len(cachedFreeModels) > 0 {
		models := cachedFreeModels
		cacheMutex.RUnlock()
		return models, nil
	}
	cacheMutex.RUnlock()

	req, err := http.NewRequestWithContext(context.Background(), "GET", "https://openrouter.ai/api/v1/models", nil)
	if err != nil {
		return nil, err
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	log.Printf("[DEBUG] OpenRouter API status: %d", resp.StatusCode)
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("OpenRouter API returned status %d", resp.StatusCode)
	}

	var wrapper struct {
		Data []openRouterModel `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&wrapper); err != nil {
		return nil, err
	}

	var freeModels []openRouterModel
	for _, m := range wrapper.Data {
		if m.Pricing.Prompt == "0" || m.Pricing.Prompt == "0.0" || m.Pricing.Prompt == "0.00" {
			// Loosen tool check - many models support tools but don't advertise it in metadata
			/*
			*/

			lowerID := strings.ToLower(m.ID)
			if strings.Contains(lowerID, "lyria") || strings.Contains(lowerID, "liquid") {
				continue
			}

			freeModels = append(freeModels, m)
		}
	}

	sort.Slice(freeModels, func(i, j int) bool {
		return scoreModel(freeModels[i]) > scoreModel(freeModels[j])
	})

	cacheMutex.Lock()
	cachedFreeModels = freeModels
	cacheTime = time.Now()
	cacheMutex.Unlock()

	log.Printf("[DEBUG] Fetched %d free OpenRouter models", len(freeModels))
	return freeModels, nil
}

func fetchNvidiaFreeModels() ([]nvidiaModel, error) {
	cacheMutex.RLock()
	if time.Since(cacheTime) < cacheTTL && len(cachedNvidiaModels) > 0 {
		models := cachedNvidiaModels
		cacheMutex.RUnlock()
		return models, nil
	}
	cacheMutex.RUnlock()

	apiKey := os.Getenv("NVIDIA_API_KEY")
	if apiKey == "" {
		log.Printf("[DEBUG] NVIDIA_API_KEY not set, skipping NVIDIA models")
		return nil, nil
	}

	req, err := http.NewRequestWithContext(context.Background(), "GET", "https://integrate.api.nvidia.com/v1/models", nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+apiKey)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("NVIDIA API returned status %d", resp.StatusCode)
	}

	var wrapper struct {
		Data []nvidiaModel `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&wrapper); err != nil {
		return nil, err
	}

	var freeModels []nvidiaModel
	for _, m := range wrapper.Data {
		lowerID := strings.ToLower(m.ID)
		// Only include chat/instruct models (not embeddings, translators, vision-only, safety, etc)
		isChatModel := strings.HasPrefix(m.ID, "nvidia/") && !strings.Contains(lowerID, "embed") && !strings.Contains(lowerID, "safety") && !strings.Contains(lowerID, "guard") && !strings.Contains(lowerID, "clip") && !strings.Contains(lowerID, "vila") && !strings.Contains(lowerID, "riva") && !strings.Contains(lowerID, "calibration") && !strings.Contains(lowerID, "pixel") && !strings.Contains(lowerID, "neva") && strings.Contains(lowerID, "instruct") || strings.Contains(lowerID, "nemotron")

		if !isChatModel {
			continue
		}

		// Mark models that support tools/function calling
		// Nemotron and newerLlama models generally support tools
		m.SupportsTools = strings.Contains(lowerID, "nemotron") ||
			strings.Contains(lowerID, "llama-3.3") ||
			strings.Contains(lowerID, "llama-3.2") ||
			strings.Contains(lowerID, "deepseek") ||
			strings.Contains(lowerID, "qwen2.5") ||
			strings.Contains(lowerID, "qwen3")

		freeModels = append(freeModels, m)
	}

	cacheMutex.Lock()
	cachedNvidiaModels = freeModels
	cacheMutex.Unlock()

	log.Printf("[DEBUG] Fetched %d free NVIDIA models (%d with tool support)", len(freeModels), func() int {
		count := 0
		for _, m := range freeModels {
			if m.SupportsTools {
				count++
			}
		}
		return count
	}())
	return freeModels, nil
}

func scoreModel(m openRouterModel) float64 {
	score := 0.0

	ctxScore := float64(m.ContextLength) / 128000.0
	if ctxScore > 1.0 {
		ctxScore = 1.0
	}
	score += ctxScore * 0.4

	capabilityScore := 0.0
	for _, p := range m.SupportedParameters {
		if p == "tools" {
			capabilityScore += 0.5
		}
		if p == "response_format" {
			capabilityScore += 0.5
		}
	}
	if capabilityScore > 1.0 {
		capabilityScore = 1.0
	}
	score += capabilityScore * 0.3

	twoYearsAgo := time.Now().AddDate(-2, 0, 0).Unix()
	now := time.Now().Unix()
	if m.Created > twoYearsAgo {
		recencyScore := float64(m.Created-twoYearsAgo) / float64(now-twoYearsAgo)
		score += recencyScore * 0.2
	}

	trustNames := []string{
		"google", "meta", "nvidia", "mistral", "anthropic",
		"openai", "microsoft", "qwen", "deepseek",
	}
	for _, name := range trustNames {
		if strings.Contains(strings.ToLower(m.ID), name) {
			score += 0.1
			break
		}
	}

	// MASSIVE BOOST for the most reliable free models
	if strings.Contains(strings.ToLower(m.ID), "gemini-2.0-flash-exp") {
		score += 10.0
	} else if strings.Contains(strings.ToLower(m.ID), "mistral-7b-instruct") {
		score += 1.0
	}

	return score
}

func handleTags(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	models, err := fetchFreeModels()
	if err != nil {
		log.Printf("Error fetching free models: %v", err)
		http.Error(w, "Internal server error", http.StatusInternalServerError)
		return
	}

	nvidiaModels, _ := fetchNvidiaFreeModels()

	var ollamaModels []ollamaModel
	for _, m := range models {
		modelName := m.ID
		if !strings.Contains(modelName, ":") {
			modelName = modelName + ":free" // Add a mock tag
		}

		ollamaModels = append(ollamaModels, ollamaModel{
			Name:       modelName,
			Model:      modelName,
			ModifiedAt: time.Unix(m.Created, 0).Format(time.RFC3339),
			Size:       0,
			Digest:     "sha256:freeride",
			Details: ollamaModelDetails{
				Format:            "gguf",
				Family:            "openrouter",
				Families:          []string{"openrouter"},
				ParameterSize:     "unknown",
				QuantizationLevel: "none",
			},
		})
	}

	// Add NVIDIA models to discovery
	for _, m := range nvidiaModels {
		ollamaModels = append(ollamaModels, ollamaModel{
			Name:       m.ID,
			Model:      m.ID,
			ModifiedAt: time.Unix(int64(m.Created), 0).Format(time.RFC3339),
			Size:       0,
			Digest:     "sha256:nvidia",
			Details: ollamaModelDetails{
				Format:            "gguf",
				Family:            "nvidia",
				Families:          []string{"nvidia"},
				ParameterSize:     "unknown",
				QuantizationLevel: "none",
			},
		})
	}

	resp := ollamaTagsResponse{Models: ollamaModels}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}

func handleVersion(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Write([]byte(`{"version":"0.1.34"}`))
}

func markCooldown(model string) {
	cooldownMu.Lock()
	entry, ok := cooldowns[model]
	if !ok {
		entry = &cooldownEntry{}
		cooldowns[model] = entry
	}
	entry.ErrorCount++
	cd := calculateStandardCooldown(entry.ErrorCount)
	entry.CooldownEnd = time.Now().Add(cd)
	saveCooldowns()
	cooldownMu.Unlock()
	log.Printf("Model %s put in cooldown for %v (ErrorCount: %d)", model, cd, entry.ErrorCount)
}

func markSuccess(model string) {
	cooldownMu.Lock()
	if entry, ok := cooldowns[model]; ok {
		entry.ErrorCount = 0
		entry.CooldownEnd = time.Time{}
		saveCooldowns()
	}
	cooldownMu.Unlock()
}

func isCooldown(model string) bool {
	cooldownMu.RLock()
	defer cooldownMu.RUnlock()
	if entry, ok := cooldowns[model]; ok {
		if time.Now().Before(entry.CooldownEnd) {
			return true
		}
	}
	return false
}

func handleChatCompletions(w http.ResponseWriter, r *http.Request) {
	// CORS headers
	w.Header().Set("Access-Control-Allow-Origin", "*")
	if r.Method == http.MethodOptions {
		w.Header().Set("Access-Control-Allow-Methods", "POST, GET, OPTIONS, PUT, DELETE")
		w.Header().Set("Access-Control-Allow-Headers", "Accept, Content-Type, Content-Length, Accept-Encoding, X-CSRF-Token, Authorization")
		return
	}

	// API key check moved to after model selection (per-provider)

	bodyBytes, err := ioutil.ReadAll(r.Body)
	if err != nil {
		http.Error(w, "Failed to read body", http.StatusBadRequest)
		return
	}
	if debugMode {
		log.Printf("[DEBUG] Incoming request to %s", r.URL.Path)
	}

	var bodyMap map[string]interface{}
	var originalModel string
	if err := json.Unmarshal(bodyBytes, &bodyMap); err == nil {
		if debugMode {
			var keys []string
			for k := range bodyMap {
				keys = append(keys, k)
			}
			log.Printf("[DEBUG] Incoming JSON keys: %v", keys)
		}
		if m, ok := bodyMap["model"].(string); ok {
			originalModel = strings.TrimSuffix(m, ":free")
			if originalModel == "openrouter" {
				originalModel = "" // Force fallback to best available
			}
		}
	} else {
		log.Printf("[ERROR] Failed to unmarshal incoming JSON: %v", err)
		bodyMap = nil
	}

	models, _ := fetchFreeModels()
	nvidiaModels, _ := fetchNvidiaFreeModels()

	// Build candidates list from BOTH OpenRouter AND NVIDIA free models
	var candidates []string
	isFree := false

	// Check OpenRouter models
	for _, m := range models {
		if m.ID == originalModel {
			isFree = true
			break
		}
	}
	// Also check NVIDIA models
	for _, m := range nvidiaModels {
		if m.ID == originalModel {
			isFree = true
			break
		}
	}

	if originalModel != "" && isFree && !isCooldown(originalModel) {
		candidates = append(candidates, originalModel)
	}
	// Add OpenRouter free models
	for _, m := range models {
		if m.ID != originalModel && !isCooldown(m.ID) {
			candidates = append(candidates, m.ID)
		}
	}
	// Add only tool-capable NVIDIA free models
	for _, m := range nvidiaModels {
		if m.ID != originalModel && !isCooldown(m.ID) && m.SupportsTools {
			candidates = append(candidates, m.ID)
		}
	}

	// If all are in cooldown, only try free models - never fall back to paid models
	if len(candidates) == 0 {
		log.Printf("[DEBUG] candidates empty, checking models: or=%s len(models)=%d len(nvidiaModels)=%d", originalModel, len(models), len(nvidiaModels))
		// Filter to only tool-capable models for fallback
		toolCapableNvidia := func() []nvidiaModel {
			var filtered []nvidiaModel
			for _, m := range nvidiaModels {
				if m.SupportsTools {
					filtered = append(filtered, m)
				}
			}
			return filtered
		}()
		if len(models) > 0 && !isCooldown(models[0].ID) {
			candidates = append(candidates, models[0].ID)
			log.Printf("[DEBUG] using OpenRouter fallback: %s", models[0].ID)
		} else if len(toolCapableNvidia) > 0 && !isCooldown(toolCapableNvidia[0].ID) {
			candidates = append(candidates, toolCapableNvidia[0].ID)
			log.Printf("[DEBUG] using NVIDIA tool-capable fallback: %s", toolCapableNvidia[0].ID)
		} else {
			log.Printf("[ERROR] All free models are in cooldown, refusing to fall back to paid model: %s", originalModel)
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusServiceUnavailable)
			w.Write([]byte(`{"error": {"type": "overloaded_error", "message": "All free models are currently in cooldown. Please try again in 30 seconds."}}`))
			return
		}
	}
	for i, candidate := range candidates {
		// Determine which API to use based on model prefix
		var targetURL string
		var apiKey string

		isNvidia := strings.HasPrefix(candidate, "nvidia/")
		isOpenRouter := strings.Contains(candidate, ":free") || strings.HasPrefix(candidate, "openrouter/") || strings.HasPrefix(candidate, "google/") || strings.HasPrefix(candidate, "meta/") || strings.HasPrefix(candidate, "mistralai/") || strings.HasPrefix(candidate, "anthropic/")

		if isOpenRouter && !isNvidia {
			targetURL = "https://openrouter.ai/api/v1/chat/completions"
			apiKey = os.Getenv("OPENROUTER_API_KEY")
			// Only use OpenRouter if we have an OpenRouter key. 
			// Do NOT fall back to OpenAI key here as it's a different provider.
		} else if isNvidia {
			targetURL = "https://integrate.api.nvidia.com/v1/chat/completions"
			apiKey = os.Getenv("NVIDIA_API_KEY")
		}

		// Skip models that need missing API keys (don't cooldown - might be available later)
		if apiKey == "" {
			log.Printf("[DEBUG] Skipping %s - API key not available", candidate)
			continue
		}

		var outboundBody []byte

		if bodyMap != nil {
			// Work on a copy of the bodyMap to avoid accumulating changes across retries
			currentBody := make(map[string]interface{})
			for k, v := range bodyMap {
				currentBody[k] = v
			}

			// Deep copy slices that sanitizeBody might modify in-place
			if msgs, ok := bodyMap["messages"].([]interface{}); ok {
				newMsgs := make([]interface{}, len(msgs))
				copy(newMsgs, msgs)
				currentBody["messages"] = newMsgs
			}
			if tools, ok := bodyMap["tools"].([]interface{}); ok {
				newTools := make([]interface{}, len(tools))
				copy(newTools, tools)
				currentBody["tools"] = newTools
			}

			currentBody["model"] = candidate
			// Strip :free suffix (provider doesn't use it)
			currentBody["model"] = strings.TrimSuffix(candidate, ":free")
			sanitizeBody(currentBody)
			outboundBody, _ = json.Marshal(currentBody)

			if debugMode {
				msgCount := 0
				if msgs, ok := currentBody["messages"].([]interface{}); ok {
					msgCount = len(msgs)
				}
				toolCount := 0
				if tools, ok := currentBody["tools"].([]interface{}); ok {
					toolCount = len(tools)
				} else if tools, ok := currentBody["tools"].([]map[string]interface{}); ok {
					toolCount = len(tools)
				}
				log.Printf("[DEBUG] Request to %s: %d messages, %d tools", candidate, msgCount, toolCount)
				if msgCount > 0 {
					msgs, _ := currentBody["messages"].([]interface{})
					if len(msgs) > 0 {
						first, _ := msgs[0].(map[string]interface{})
						last, _ := msgs[len(msgs)-1].(map[string]interface{})
						log.Printf("[DEBUG] First Msg: %v", first["content"])
						log.Printf("[DEBUG] Last Msg: %v", last["content"])
					}
				}
				// Save to file for inspection
				os.WriteFile("last_payload.json", outboundBody, 0644)
			}
		} else {
			outboundBody = bodyBytes
		}

		req, err := http.NewRequestWithContext(r.Context(), "POST", targetURL, bytes.NewBuffer(outboundBody))
		if err != nil {
			log.Printf("[ERROR] Failed to create request for %s: %v", candidate, err)
			continue
		}

		// Copy and sanitize headers
		for k, vv := range r.Header {
			lowK := strings.ToLower(k)
			// Strip Anthropic-specific and control headers
			if lowK == "accept-encoding" || strings.HasPrefix(lowK, "anthropic-") || lowK == "content-length" || lowK == "connection" {
				continue
			}
			for _, v := range vv {
				req.Header.Add(k, v)
			}
		}
		req.Header.Set("Authorization", "Bearer "+apiKey)
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", "application/json")
		req.Header.Set("Content-Length", fmt.Sprintf("%d", len(outboundBody)))

		log.Printf("Attempting request with model: %s (via %s)", candidate, targetURL)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			log.Printf("Request to %s failed: %v", candidate, err)
			if err != context.Canceled {
				markCooldown(candidate)
			} else {
				log.Printf("[DEBUG] Context canceled for %s, skipping cooldown", candidate)
			}
			continue
		}

		if (resp.StatusCode == 401 || resp.StatusCode == 404) && strings.Contains(targetURL, "nvidia.com") {
			log.Printf("[WARN] NVIDIA direct API returned %d. Falling back to OpenRouter for %s...", resp.StatusCode, candidate)
			io.Copy(ioutil.Discard, resp.Body)
			resp.Body.Close()

			// Retry via OpenRouter
			targetURL = "https://openrouter.ai/api/v1/chat/completions"
			apiKey = os.Getenv("OPENROUTER_API_KEY")
			if apiKey == "" { apiKey = os.Getenv("OPENAI_API_KEY") }

			req, _ = http.NewRequestWithContext(r.Context(), "POST", targetURL, bytes.NewBuffer(outboundBody))
			req.Header.Set("Authorization", "Bearer "+apiKey)
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("Content-Length", fmt.Sprintf("%d", len(outboundBody)))
			resp, err = http.DefaultClient.Do(req)
			if err != nil {
				log.Printf("Fallback to OpenRouter for %s failed: %v", candidate, err)
				if err != context.Canceled {
					markCooldown(candidate)
				} else {
					log.Printf("[DEBUG] Context canceled during fallback for %s, skipping cooldown", candidate)
				}
				continue
			}
		}

		if resp.StatusCode != http.StatusOK {
			log.Printf("Model %s returned status %d. Target: %s", candidate, resp.StatusCode, targetURL)
			
			// Log error body
			errorBody, _ := ioutil.ReadAll(resp.Body)
			log.Printf("[ERROR] Model %s response body: %s", candidate, string(errorBody))
			resp.Body.Close()

			// Fallback on Bad Request (400), Unauthorized (401), Payment required (402), Rate Limit (429) or Server Errors (5xx)
			if resp.StatusCode == 400 || resp.StatusCode == 401 || resp.StatusCode == 402 || resp.StatusCode == 404 || resp.StatusCode == 429 || resp.StatusCode >= 500 {
				markCooldown(candidate)
			}

			// If this was the last candidate, we have to return the error
			if i == len(candidates)-1 {
				copyResponse(w, resp)
				return
			}
			continue
		}

		// Log success only for 2xx
		if resp.StatusCode >= 200 && resp.StatusCode < 300 {
			log.Printf("Model %s succeeded (status %d). Returning response to client.", candidate, resp.StatusCode)
			markSuccess(candidate)
		} else {
			log.Printf("Model %s returned status %d. Skipping (not cooling down).", candidate, resp.StatusCode)
		}

		// Only translate SSE if it's a successful response
		if (r.URL.Path == "/v1/responses") && resp.StatusCode == 200 {
			translateSSE(w, resp)
		} else if (r.URL.Path == "/v1/messages" || r.URL.Path == "/api/v1/messages") && resp.StatusCode == 200 {
			translateAnthropicSSE(w, resp)
		} else {
			copyResponse(w, resp)
		}
		return
	}

	// If we get here, no candidates were available or all failed
	log.Printf("[ERROR] No models available to handle the request.")
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusServiceUnavailable)
	w.Write([]byte(`{"error": {"message": "No models available in the Freeride proxy. Please check your API keys.", "type": "unavailable"}}`))
}

func translateResponse(body []byte) []byte {
	var resp map[string]interface{}
	if err := json.Unmarshal(body, &resp); err != nil {
		return body
	}

	choices, ok := resp["choices"].([]interface{})
	if !ok || len(choices) == 0 {
		return body
	}

	choice, ok := choices[0].(map[string]interface{})
	if !ok {
		return body
	}

	message, ok := choice["message"].(map[string]interface{})
	if !ok {
		return body
	}

	content, ok := message["content"].(string)
	if !ok || content == "" {
		return body
	}

	if debugMode {
		log.Printf("[SPY] Model returned: %s", content)
	}

	matches := toolRegex.FindAllStringSubmatch(content, -1)
	if len(matches) == 0 {
		return body
	}

	var toolCalls []map[string]interface{}
	for _, match := range matches {
		name := match[1]
		paramsRaw := match[2]

		// Parse parameters into a map
		args := make(map[string]interface{})
		paramMatches := paramRegex.FindAllStringSubmatch(paramsRaw, -1)
		for _, pm := range paramMatches {
			pName := pm[1]
			pValue := pm[2]
			args[pName] = pValue
		}

		// Map common tool names to what opencode expects
		toolName := name
		if toolName == "shell" {
			toolName = "run_terminal_command"
		}

		argsJSON, _ := json.Marshal(args)

		toolCalls = append(toolCalls, map[string]interface{}{
			"id":   fmt.Sprintf("call_%d_%s", time.Now().Unix(), name),
			"type": "function",
			"function": map[string]interface{}{
				"name":      toolName,
				"arguments": string(argsJSON),
			},
		})
	}

	if len(toolCalls) > 0 {
		message["tool_calls"] = toolCalls
		// Strip the XML from content so the agent only sees the tool call
		newContent := toolRegex.ReplaceAllString(content, "")
		message["content"] = strings.TrimSpace(newContent)

		log.Printf("[TRANS] Translated %d XML tool calls into OpenAI tool_calls", len(toolCalls))
	}

	newBody, err := json.Marshal(resp)
	if err != nil {
		return body
	}
	return newBody
}

func copyResponse(w http.ResponseWriter, resp *http.Response) {
	if debugMode {
		log.Printf("[DEBUG] Response Headers: %v", resp.Header)
	}

	for k, vv := range resp.Header {
		if strings.ToLower(k) == "content-length" || strings.ToLower(k) == "content-encoding" {
			continue
		}
		for _, v := range vv {
			w.Header().Add(k, v)
		}
	}

	var reader io.ReadCloser
	var err error
	switch resp.Header.Get("Content-Encoding") {
	case "gzip":
		reader, err = gzip.NewReader(resp.Body)
		if err != nil {
			http.Error(w, "Failed to create gzip reader", http.StatusInternalServerError)
			return
		}
		defer reader.Close()
	default:
		reader = resp.Body
	}

	bodyBytes, err := ioutil.ReadAll(reader)
	if err != nil {
		http.Error(w, "Failed to read upstream body", http.StatusInternalServerError)
		return
	}
	resp.Body.Close()

	translated := translateResponse(bodyBytes)
	w.Header().Set("Content-Length", fmt.Sprintf("%d", len(translated)))
	w.WriteHeader(resp.StatusCode)
	w.Write(translated)
}

func translateSSE(w http.ResponseWriter, resp *http.Response) {
	// Set headers for SSE
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.WriteHeader(resp.StatusCode)

	flusher, ok := w.(http.Flusher)

	respID := "resp_" + fmt.Sprintf("%d", time.Now().Unix())
	itemID := "item_" + fmt.Sprintf("%d", time.Now().Unix())
	modelName := "gpt-4o" // Placeholder that OpenCode accepts

	// Utility to send a named event
	sendEvent := func(evType string, data interface{}) {
		b, _ := json.Marshal(data)
		fmt.Fprintf(w, "event: %s\ndata: %s\n\n", evType, string(b))
		if ok {
			flusher.Flush()
		}
	}

	// 1. response.created
	sendEvent("response.created", map[string]interface{}{
		"type": "response.created",
		"response": map[string]interface{}{
			"id":         respID,
			"created_at": time.Now().Unix(),
			"model":      modelName,
		},
	})

	// 2. response.output_item.added (Registers the message)
	sendEvent("response.output_item.added", map[string]interface{}{
		"type":         "response.output_item.added",
		"output_index": 0,
		"item": map[string]interface{}{
			"type": "message",
			"id":   itemID,
		},
	})

	scanner := bufio.NewScanner(resp.Body)
	for scanner.Scan() {
		line := scanner.Text()
		if line == "" || !strings.HasPrefix(line, "data: ") {
			continue
		}

		data := strings.TrimPrefix(line, "data: ")
		if data == "[DONE]" {
			break
		}

		if debugMode {
			log.Printf("[RAW-SSE] %s", data)
		}

		var chunk map[string]interface{}
		if err := json.Unmarshal([]byte(data), &chunk); err == nil {
			if choices, ok := chunk["choices"].([]interface{}); ok && len(choices) > 0 {
				if choice, ok := choices[0].(map[string]interface{}); ok {
					if delta, ok := choice["delta"].(map[string]interface{}); ok {
						content, _ := delta["content"].(string)
						reasoning, _ := delta["reasoning"].(string)

						text := content
						if text == "" {
							text = reasoning
						}

						if text != "" {
							if debugMode {
								log.Printf("[SPY-CHUNK] %s", text)
							}
							// 3. response.output_text.delta
							sendEvent("response.output_text.delta", map[string]interface{}{
								"type":    "response.output_text.delta",
								"item_id": itemID,
								"delta":   text,
							})
						}
					}
				}
			}
		}
	}

	// 4. response.output_item.done
	sendEvent("response.output_item.done", map[string]interface{}{
		"type":         "response.output_item.done",
		"output_index": 0,
		"item": map[string]interface{}{
			"type": "message",
			"id":   itemID,
		},
	})

	// 5. response.completed
	sendEvent("response.completed", map[string]interface{}{
		"type": "response.completed",
		"response": map[string]interface{}{
			"usage": map[string]interface{}{
				"input_tokens":  0,
				"output_tokens": 0,
			},
		},
	})

	resp.Body.Close()
}

func proxyModels(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	if r.Method == http.MethodOptions {
		w.Header().Set("Access-Control-Allow-Methods", "GET, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Accept, Content-Type, Authorization")
		return
	}

	modelID := strings.TrimPrefix(r.URL.Path, "/v1/models/")
	if modelID == "/v1/models" {
		modelID = ""
	}

	w.Header().Set("Content-Type", "application/json")
	if modelID != "" {
		w.Write([]byte(fmt.Sprintf(`{"id":"%s","object":"model","created":1678888888,"owned_by":"freeride"}`, modelID)))
		return
	}

	models, _ := fetchFreeModels()
	nvidiaModels, _ := fetchNvidiaFreeModels()

	type openAIModel struct {
		ID          string `json:"id"`
		Type        string `json:"type"`         // Anthropic compatibility
		Object      string `json:"object"`       // OpenAI compatibility
		DisplayName string `json:"display_name"` // Anthropic compatibility
		Created     int64  `json:"created"`
		OwnedBy     string `json:"owned_by"`
	}
	var res struct {
		Object string        `json:"object"`
		Data   []openAIModel `json:"data"`
	}
	res.Object = "list"
	for _, m := range models {
		res.Data = append(res.Data, openAIModel{
			ID:          m.ID,
			Type:        "model",
			Object:      "model",
			DisplayName: m.ID,
			Created:     m.Created,
			OwnedBy:     "openrouter",
		})
	}
	// Add NVIDIA models
	for _, m := range nvidiaModels {
		res.Data = append(res.Data, openAIModel{
			ID:          m.ID,
			Type:        "model",
			Object:      "model",
			DisplayName: m.ID,
			Created:     int64(m.Created),
			OwnedBy:     "nvidia",
		})
	}
	res.Data = append(res.Data, openAIModel{ID: "gpt-4o", Type: "model", Object: "model", DisplayName: "GPT-4o", Created: 1678888888, OwnedBy: "openai"})
	res.Data = append(res.Data, openAIModel{ID: "gpt-4", Type: "model", Object: "model", DisplayName: "GPT-4", Created: 1678888888, OwnedBy: "openai"})
	res.Data = append(res.Data, openAIModel{ID: "claude-3-5-sonnet-20241022", Type: "model", Object: "model", DisplayName: "Claude 3.5 Sonnet", Created: 1678888888, OwnedBy: "anthropic"})
	res.Data = append(res.Data, openAIModel{ID: "claude-3-5-sonnet", Type: "model", Object: "model", DisplayName: "Claude 3.5 Sonnet (Legacy)", Created: 1678888888, OwnedBy: "anthropic"})
	json.NewEncoder(w).Encode(res)
}

func sanitizeBody(body map[string]interface{}) {
	var keys []string
	for k, v := range body {
		keys = append(keys, fmt.Sprintf("%s(%T)", k, v))
	}
	log.Printf("[DEBUG] Incoming Body Types: %v", keys)
	// 0. Handle 'input' field (OpenCode format)
	if input, ok := body["input"].([]interface{}); ok {
		var msgs []interface{}
		if m, ok := body["messages"].([]interface{}); ok {
			msgs = m
		}
		// If input is a list of messages, merge them
		msgs = append(msgs, input...)
		body["messages"] = msgs
		delete(body, "input")
	}

	// 1. Convert 'system' parameter to a system message
	if system, ok := body["system"]; ok {
		sysText := ""
		if s, ok := system.(string); ok {
			sysText = s
		} else if sList, ok := system.([]interface{}); ok {
			for _, item := range sList {
				if itemMap, ok := item.(map[string]interface{}); ok {
					if txt, ok := itemMap["text"].(string); ok {
						sysText += txt + "\n"
					}
				}
			}
		}
		if sysText != "" {
			var msgs []interface{}
			if m, ok := body["messages"].([]interface{}); ok {
				msgs = m
			}
			newMsgs := append([]interface{}{
				map[string]interface{}{"role": "system", "content": sysText},
			}, msgs...)
			body["messages"] = newMsgs
		}
		delete(body, "system")
	}

	// 2. Sanitize messages: flatten content lists to strings OR handle Anthropic tool blocks
	if msgs, ok := body["messages"].([]interface{}); ok {
		var newMsgs []interface{}
		for _, m := range msgs {
			if mMap, ok := m.(map[string]interface{}); ok {
				if content, ok := mMap["content"]; ok {
					if cList, ok := content.([]interface{}); ok {
						text := ""
						var toolCalls []map[string]interface{}
						isToolResult := false
						var toolResultContent string
						var toolResultID string

						for _, item := range cList {
							if iMap, ok := item.(map[string]interface{}); ok {
								itemType, _ := iMap["type"].(string)
								switch itemType {
								case "text":
									if t, ok := iMap["text"].(string); ok {
										text += t
									}
								case "tool_use":
									id, _ := iMap["id"].(string)
									name, _ := iMap["name"].(string)
									input, _ := iMap["input"].(map[string]interface{})
									inputJSON, _ := json.Marshal(input)
									toolCalls = append(toolCalls, map[string]interface{}{
										"id":   id,
										"type": "function",
										"function": map[string]interface{}{
											"name":      name,
											"arguments": string(inputJSON),
										},
									})
								case "tool_result":
									isToolResult = true
									toolResultID, _ = iMap["tool_use_id"].(string)
									// Content can be text or blocks, for now we assume text
									if c, ok := iMap["content"].(string); ok {
										toolResultContent = c
									} else if cBlocks, ok := iMap["content"].([]interface{}); ok {
										for _, b := range cBlocks {
											if bMap, ok := b.(map[string]interface{}); ok {
												if bt, ok := bMap["text"].(string); ok {
													toolResultContent += bt
												}
											}
										}
									}
								}
							}
						}

						if isToolResult {
							// Anthropic tool_result becomes an OpenAI 'tool' role message
							newMsgs = append(newMsgs, map[string]interface{}{
								"role":         "tool",
								"tool_call_id": toolResultID,
								"content":      toolResultContent,
							})
							continue
						}

						mMap["content"] = text
						if len(toolCalls) > 0 {
							mMap["tool_calls"] = toolCalls
						}
					}
				}
				newMsgs = append(newMsgs, mMap)
			}
		}
		body["messages"] = newMsgs
	}

	// 3. Convert 'tools' to strict OpenAI 'function' schema and remove unsupported ones
	// The Responses API format has tools as {"type":"function","name":"shell","description":"...","parameters":{...}}
	// The Chat Completions format wraps under {"type":"function","function":{"name":"shell",...}}
	// We need to handle both and normalise to Chat Completions format for OpenRouter.
	if tools, ok := body["tools"].([]interface{}); ok {
		log.Printf("[DEBUG] Received %d tools from client", len(tools))
		var newTools []interface{}
		for i, t := range tools {
			if tMap, ok := t.(map[string]interface{}); ok {
				var name string
				var fn map[string]interface{}

				// Log tool keys for debugging
				log.Printf("[DEBUG] Tool %d keys: %v", i, tMap)

				// Case 1: OpenAI Chat Completions format (already correct)
				if nested, ok := tMap["function"].(map[string]interface{}); ok {
					fn = nested
					name, _ = fn["name"].(string)
				} else {
					// Case 2: Anthropic format (name, description, input_schema)
					// or legacy Responses API format (name, description, parameters)
					name, _ = tMap["name"].(string)
					description, _ := tMap["description"].(string)

					params := tMap["parameters"]
					if params == nil {
						params = tMap["input_schema"] // Map Anthropic input_schema to OpenAI parameters
					}

					if name != "" {
						fn = map[string]interface{}{
							"name":        name,
							"description": description,
							"parameters":  params,
						}
						// Wrap in the standard OpenAI function structure
						tMap = map[string]interface{}{
							"type":     "function",
							"function": fn,
						}
					}
				}

				if name == "" {
					continue
				}

				// Filter disabled for debugging
				if false && (name == "Agent" || name == "TaskUpdate" || name == "TaskCreate" || name == "TaskCreate_deprecated") {
					continue
				}
				newTools = append(newTools, tMap)
			}
		}
		log.Printf("[DEBUG] Passing %d tools to model after filtering", len(newTools))
		if len(newTools) > 0 {
			body["tools"] = newTools
		} else {
			delete(body, "tools")
		}
	}

	// 4. Strip other Anthropic-specific fields
	delete(body, "thinking")
	delete(body, "metadata")
	delete(body, "output_config")
	delete(body, "anthropic-version")

	// 5. Cap max_tokens to prevent context overflow (OpenRouter free models often have 32k limits)
	if mt, ok := body["max_tokens"].(float64); ok {
		if mt > 4096 {
			body["max_tokens"] = 4096
		}
	} else if _, ok := body["max_tokens"]; !ok {
		// Default to 4096 if not specified, to be safe
		body["max_tokens"] = 4096
	}

	// 5. Convert 'prompt' to a user message (OpenCode legacy support)
	if prompt, ok := body["prompt"].(string); ok && prompt != "" {
		var msgs []interface{}
		if m, ok := body["messages"].([]interface{}); ok {
			msgs = m
		}
		body["messages"] = append(msgs, map[string]interface{}{
			"role":    "user",
			"content": prompt,
		})
		delete(body, "prompt")
	}
}

func handleOllamaChat(w http.ResponseWriter, r *http.Request) {
	http.Error(w, "/api/chat is not fully implemented in the Freeride proxy. Please configure your client to use the OpenAI compatibility endpoint at /v1/chat/completions.", http.StatusNotImplemented)
}

func cleanStaleCooldowns() {
	cooldownMu.Lock()
	defer cooldownMu.Unlock()
	now := time.Now()
	cleaned := 0
	for k, v := range cooldowns {
		if now.After(v.CooldownEnd) {
			delete(cooldowns, k)
			cleaned++
		}
	}
	if cleaned > 0 {
		log.Printf("[DEBUG] Cleaned %d stale cooldowns", cleaned)
	}
}

func handleHello(w http.ResponseWriter, r *http.Request) {
	log.Printf("[DEBUG] Spoofing hello response for %s", r.URL.Path)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	w.Write([]byte(`{"status": "ok", "message": "hello"}`))
}

func handleCountTokens(w http.ResponseWriter, r *http.Request) {
	log.Printf("[DEBUG] Spoofing count_tokens response")
	w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
	w.Write([]byte(`{"input_tokens": 100}`))
}

func main() {
	// Load .env file if it exists
	if data, err := ioutil.ReadFile(".env"); err == nil {
		log.Printf("[DEBUG] Loading API keys from .env file")
		lines := strings.Split(string(data), "\n")
		for _, line := range lines {
			line = strings.TrimSpace(line)
			if line == "" || strings.HasPrefix(line, "#") {
				continue
			}
			parts := strings.SplitN(line, "=", 2)
			if len(parts) == 2 {
				k := strings.TrimSpace(parts[0])
				v := strings.TrimSpace(parts[1])
				masked := ""
				if len(v) > 8 {
					masked = v[:4] + "..." + v[len(v)-4:]
				} else {
					masked = "****"
				}
				log.Printf("[DEBUG] Setting environment variable: %s = %s", k, masked)
				os.Setenv(k, v)
			}
		}
	} else {
		log.Printf("[DEBUG] No .env file found: %v", err)
	}

	flag.BoolVar(&debugMode, "debug", false, "Enable debug logging")
	flag.Parse()

	// Clean up stale cooldowns on startup
	cleanStaleCooldowns()
	loadCooldowns()


	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if debugMode {
			log.Printf("[DEBUG] %s %s", r.Method, r.URL.Path)
		}

		switch r.URL.Path {
		case "/api/tags":
			handleTags(w, r)
		case "/api/version":
			handleVersion(w, r)
		case "/api/hello", "/v1/oauth/hello":
			handleHello(w, r)
		case "/v1/messages/count_tokens", "/v1/v1/messages/count_tokens":
			handleCountTokens(w, r)
		case "/v1/v1/messages", "/v1/chat/completions", "/v1/messages", "/api/v1/messages", "/v1/responses":
			handleChatCompletions(w, r)
		case "/v1/models", "/v1/models/":
			proxyModels(w, r)
		case "/api/chat":
			handleOllamaChat(w, r)
		default:
			if strings.HasPrefix(r.URL.Path, "/v1/models/") {
				proxyModels(w, r)
			} else {
				w.WriteHeader(http.StatusOK)
				w.Write([]byte("Freeride Proxy is running"))
			}
		}
	})

	port := os.Getenv("PORT")
	if port == "" {
		port = "11434"
	}

	log.Printf("Proxy starting on :%s", port)
	if err := http.ListenAndServe(":"+port, handler); err != nil {
		log.Fatalf("Server failed: %v", err)
	}
}

func translateAnthropicSSE(w http.ResponseWriter, resp *http.Response) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.WriteHeader(resp.StatusCode)

	flusher, _ := w.(http.Flusher)
	respID := "msg_" + fmt.Sprintf("%d", time.Now().Unix())

	// 1. message_start
	sendAnthropicEvent(w, flusher, "message_start", map[string]interface{}{
		"type": "message_start",
		"message": map[string]interface{}{
			"id":      respID,
			"type":    "message",
			"role":    "assistant",
			"content": []interface{}{},
			"model":   "claude-3-5-sonnet-20241022",
			"usage": map[string]interface{}{
				"input_tokens":  0,
				"output_tokens": 0,
			},
		},
	})

	// 2. content_block_start
	sendAnthropicEvent(w, flusher, "content_block_start", map[string]interface{}{
		"type":  "content_block_start",
		"index": 0,
		"content_block": map[string]interface{}{
			"type": "text",
			"text": "",
		},
	})

	maxIndex := 0
	scanner := bufio.NewScanner(resp.Body)
	for scanner.Scan() {
		line := scanner.Text()
		if line == "" || !strings.HasPrefix(line, "data: ") {
			continue
		}
		data := strings.TrimPrefix(line, "data: ")
		log.Printf("[DEBUG] Raw SSE data: %s", data)
		if data == "[DONE]" {
			break
		}
		var chunk map[string]interface{}
		if err := json.Unmarshal([]byte(data), &chunk); err == nil {
			if choices, ok := chunk["choices"].([]interface{}); ok && len(choices) > 0 {
				if choice, ok := choices[0].(map[string]interface{}); ok {
					if delta, ok := choice["delta"].(map[string]interface{}); ok {
						// 1. Text Content
						if content, ok := delta["content"].(string); ok && content != "" {
							// 3. content_block_delta
							sendAnthropicEvent(w, flusher, "content_block_delta", map[string]interface{}{
								"type":  "content_block_delta",
								"index": 0,
								"delta": map[string]interface{}{
									"type": "text_delta",
									"text": content,
								},
							})
						}
						// 2. Tool Calls
						if toolCalls, ok := delta["tool_calls"].([]interface{}); ok && len(toolCalls) > 0 {
							for _, tc := range toolCalls {
								if tcMap, ok := tc.(map[string]interface{}); ok {
									idx, _ := tcMap["index"].(float64)
									index := int(idx) + 1
									if index > maxIndex {
										maxIndex = index
									}
									if fn, ok := tcMap["function"].(map[string]interface{}); ok {
										name, _ := fn["name"].(string)
										args, _ := fn["arguments"].(string)

										// If it's the start of a tool call (has name)
										if name != "" {
											id, _ := tcMap["id"].(string)
											sendAnthropicEvent(w, flusher, "content_block_start", map[string]interface{}{
												"type":  "content_block_start",
												"index": index,
												"content_block": map[string]interface{}{
													"type":  "tool_use",
													"id":    id,
													"name":  name,
													"input": map[string]interface{}{},
												},
											})
										}

										// If it has arguments (delta)
										if args != "" {
											sendAnthropicEvent(w, flusher, "content_block_delta", map[string]interface{}{
												"type":  "content_block_delta",
												"index": index,
												"delta": map[string]interface{}{
													"type":         "input_json_delta",
													"partial_json": args,
												},
											})
										}
									}
								}
							}
						}
					}
				}
			}
		}
	}

	// 4. content_block_stop for ALL blocks
	for i := 0; i <= maxIndex; i++ {
		sendAnthropicEvent(w, flusher, "content_block_stop", map[string]interface{}{
			"type":  "content_block_stop",
			"index": i,
		})
	}

	// 5. message_delta
	sendAnthropicEvent(w, flusher, "message_delta", map[string]interface{}{
		"type": "message_delta",
		"delta": map[string]interface{}{
			"stop_reason":   "end_turn",
			"stop_sequence": nil,
		},
		"usage": map[string]interface{}{
			"input_tokens":  0,
			"output_tokens": 0,
		},
	})

	// 6. message_stop
	sendAnthropicEvent(w, flusher, "message_stop", map[string]interface{}{
		"type": "message_stop",
	})

	resp.Body.Close()
}

func sendAnthropicEvent(w http.ResponseWriter, flusher http.Flusher, evType string, data interface{}) {
	b, _ := json.Marshal(data)
	log.Printf("[SSE] %s: %s", evType, string(b))
	fmt.Fprintf(w, "event: %s\ndata: %s\n\n", evType, string(b))
	if flusher != nil {
		flusher.Flush()
	}
}

