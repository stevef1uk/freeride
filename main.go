package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"io/ioutil"
	"log"
	"math"
	"net/http"
	"os"
	"sort"
	"strings"
	"sync"
	"time"
	"flag"
	"regexp"
	"compress/gzip"
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
	cachedFreeModels []openRouterModel
	cacheMutex       sync.RWMutex
	cacheTime        time.Time
	cacheTTL         = 1 * time.Hour

	cooldowns        = make(map[string]*cooldownEntry)
	cooldownMu       sync.RWMutex

	debugMode        bool
	toolRegex        = regexp.MustCompile("(?s)<invoke name=\"([^\"]+)\">.*?<parameter name=\"command\">(.*?)</parameter>.*?</invoke>")
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
	n := max(1, errorCount)
	exp := min(n-1, 3)
	ms := 60_000 * int(math.Pow(5, float64(exp)))
	ms = min(3_600_000, ms) // cap at 1 hour
	return time.Duration(ms) * time.Millisecond
}

// ... fetchFreeModels, scoreModel, handleTags, handleVersion, handleOllamaChat existing ...

func fetchFreeModels() ([]openRouterModel, error) {
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
			hasTools := false
			for _, p := range m.SupportedParameters {
				if p == "tools" {
					hasTools = true
					break
				}
			}
			if !hasTools {
				continue
			}

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
				Family:            "freeride",
				Families:          []string{"freeride"},
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

	apiKey := os.Getenv("OPENROUTER_API_KEY")
	if apiKey == "" {
		http.Error(w, "OPENROUTER_API_KEY not set in environment", http.StatusInternalServerError)
		return
	}

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
		}
	} else {
		log.Printf("[ERROR] Failed to unmarshal incoming JSON: %v", err)
		bodyMap = nil
	}

	models, _ := fetchFreeModels()
	
	// Build candidates list (prioritizing the requested model ONLY if it is a known free model)
	var candidates []string
	isFree := false
	for _, m := range models {
		if m.ID == originalModel {
			isFree = true
			break
		}
	}

	if originalModel != "" && isFree && !isCooldown(originalModel) {
		candidates = append(candidates, originalModel)
	}
	for _, m := range models {
		if m.ID != originalModel && !isCooldown(m.ID) {
			candidates = append(candidates, m.ID)
		}
	}

	// If all are in cooldown, just try the original or highest rank as a last resort
	if len(candidates) == 0 {
		if originalModel != "" {
			candidates = append(candidates, originalModel)
		} else if len(models) > 0 {
			candidates = append(candidates, models[0].ID)
		} else {
			http.Error(w, "No models available", http.StatusServiceUnavailable)
			return
		}
	}
	for i, candidate := range candidates {
		targetURL := "https://openrouter.ai/api/v1/chat/completions"
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
				}
				log.Printf("[DEBUG] Request to %s: %d messages, %d tools", candidate, msgCount, toolCount)
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
		req.Header.Set("Content-Length", fmt.Sprintf("%d", len(outboundBody)))
		
		log.Printf("Attempting request with model: %s", candidate)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			log.Printf("Request to %s failed: %v", candidate, err)
			markCooldown(candidate)
			continue
		}

		// Fallback on Bad Request (400 - validation errors), Payment (402), Rate Limit (429) or Server Errors (5xx)
		if resp.StatusCode == 400 || resp.StatusCode == 402 || resp.StatusCode == 429 || resp.StatusCode >= 500 {
			log.Printf("Model %s returned status %d. Marking in cooldown...", candidate, resp.StatusCode)
			markCooldown(candidate)
			
			// Discard body so connection can be reused
			io.Copy(ioutil.Discard, resp.Body)
			resp.Body.Close()
			
			// If this was the last candidate, we have to return the error
			if i == len(candidates)-1 {
				copyResponse(w, resp)
				return
			}
			continue
		}

		log.Printf("Model %s succeeded (status %d). Returning response to client.", candidate, resp.StatusCode)
		markSuccess(candidate)
		
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
		command := match[2]

		// Map common tool names to what opencode expects
		toolName := name
		if toolName == "shell" {
			toolName = "run_terminal_command"
		}

		toolCalls = append(toolCalls, map[string]interface{}{
			"id":   fmt.Sprintf("call_%d_%s", time.Now().Unix(), name),
			"type": "function",
			"function": map[string]interface{}{
				"name":      toolName,
				"arguments": fmt.Sprintf("{\"command\": %q}", command),
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
	type openAIModel struct {
		ID      string `json:"id"`
		Object  string `json:"object"`
		Created int64  `json:"created"`
		OwnedBy string `json:"owned_by"`
	}
	var res struct {
		Object string        `json:"object"`
		Data   []openAIModel `json:"data"`
	}
	res.Object = "list"
	for _, m := range models {
		res.Data = append(res.Data, openAIModel{
			ID:      m.ID,
			Object:  "model",
			Created: m.Created,
			OwnedBy: "openrouter",
		})
	}
	res.Data = append(res.Data, openAIModel{ID: "gpt-4o", Object: "model", Created: 1678888888, OwnedBy: "openai"})
	res.Data = append(res.Data, openAIModel{ID: "gpt-4", Object: "model", Created: 1678888888, OwnedBy: "openai"})
	res.Data = append(res.Data, openAIModel{ID: "claude-3-5-sonnet", Object: "model", Created: 1678888888, OwnedBy: "anthropic"})
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

	// 2. Sanitize messages: flatten content lists to strings
	if msgs, ok := body["messages"].([]interface{}); ok {
		for i, m := range msgs {
			if mMap, ok := m.(map[string]interface{}); ok {
				if content, ok := mMap["content"]; ok {
					if cList, ok := content.([]interface{}); ok {
						text := ""
						for _, item := range cList {
							if iMap, ok := item.(map[string]interface{}); ok {
								if t, ok := iMap["text"].(string); ok {
									text += t
								} else if t, ok := iMap["input_text"].(string); ok {
									text += t
								} else if t, ok := iMap["input_text"].(string); ok {
									text += t
								}
							}
						}
						mMap["content"] = text
					}
				}
				msgs[i] = mMap
			}
		}
	}

	// 3. Convert 'tools' to strict OpenAI 'function' schema and remove unsupported ones
	// The Responses API format has tools as {"type":"function","name":"shell","description":"...","parameters":{...}}
	// The Chat Completions format wraps under {"type":"function","function":{"name":"shell",...}}
	// We need to handle both and normalise to Chat Completions format for OpenRouter.
	if tools, ok := body["tools"].([]interface{}); ok {
		log.Printf("[DEBUG] Received %d tools from client", len(tools))
		var newTools []map[string]interface{}
		for _, t := range tools {
			if tMap, ok := t.(map[string]interface{}); ok {
				typeStr, _ := tMap["type"].(string)
				if typeStr != "function" {
					continue
				}

				var name string
				var fn map[string]interface{}

				if nested, ok := tMap["function"].(map[string]interface{}); ok {
					// Chat Completions format: name nested under "function"
					fn = nested
					name, _ = fn["name"].(string)
				} else if topName, ok := tMap["name"].(string); ok {
					// Responses API format: name at top level — convert to Chat Completions format
					name = topName
					fn = map[string]interface{}{
						"name":        name,
						"description": tMap["description"],
						"parameters":  tMap["parameters"],
					}
					tMap = map[string]interface{}{
						"type":     "function",
						"function": fn,
					}
				} else {
					continue
				}

				// Filter out internal tools that cause validation bloat
				if name == "Agent" || name == "TaskUpdate" || name == "TaskCreate" || name == "TaskCreate_deprecated" {
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

func main() {
	flag.BoolVar(&debugMode, "debug", false, "Enable verbose debug logging")
	flag.Parse()

	loadCooldowns()

	port := os.Getenv("PORT")
	if port == "" {
		port = "11434"
	}

	http.HandleFunc("/api/tags", handleTags)
	http.HandleFunc("/api/version", handleVersion)
	http.HandleFunc("/v1/chat/completions", handleChatCompletions)
	http.HandleFunc("/v1/messages", handleChatCompletions)
	http.HandleFunc("/api/v1/messages", handleChatCompletions)
	http.HandleFunc("/v1/responses", handleChatCompletions)
	http.HandleFunc("/v1/models", proxyModels)
	http.HandleFunc("/v1/models/", proxyModels)
	http.HandleFunc("/api/chat", handleOllamaChat)
	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("Freeride Proxy is running"))
	})

	log.Printf("Starting Freeride Ollama proxy with Auto-Fallback on port %s...", port)
	if err := http.ListenAndServe(":"+port, nil); err != nil {
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
			"id":   respID,
			"type": "message",
			"role": "assistant",
			"content": []interface{}{},
			"model": "claude-3-5-sonnet-20241022",
			"usage": map[string]interface{}{
				"input_tokens":  0,
				"output_tokens": 0,
			},
		},
	})

	// 2. content_block_start
	sendAnthropicEvent(w, flusher, "content_block_start", map[string]interface{}{
		"type": "content_block_start",
		"index": 0,
		"content_block": map[string]interface{}{
			"type": "text",
			"text": "",
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
		var chunk map[string]interface{}
		if err := json.Unmarshal([]byte(data), &chunk); err == nil {
			if choices, ok := chunk["choices"].([]interface{}); ok && len(choices) > 0 {
				if choice, ok := choices[0].(map[string]interface{}); ok {
					if delta, ok := choice["delta"].(map[string]interface{}); ok {
						if content, ok := delta["content"].(string); ok && content != "" {
							// 3. content_block_delta
							sendAnthropicEvent(w, flusher, "content_block_delta", map[string]interface{}{
								"type": "content_block_delta",
								"index": 0,
								"delta": map[string]interface{}{
									"type": "text_delta",
									"text": content,
								},
							})
						}
					}
				}
			}
		}
	}

	// 4. content_block_stop
	sendAnthropicEvent(w, flusher, "content_block_stop", map[string]interface{}{
		"type": "content_block_stop",
		"index": 0,
	})

	// 5. message_delta
	sendAnthropicEvent(w, flusher, "message_delta", map[string]interface{}{
		"type": "message_delta",
		"delta": map[string]interface{}{
			"stop_reason": "end_turn",
			"stop_sequence": nil,
		},
		"usage": map[string]interface{}{
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
	fmt.Fprintf(w, "event: %s\ndata: %s\n\n", evType, string(b))
	if flusher != nil {
		flusher.Flush()
	}
}
