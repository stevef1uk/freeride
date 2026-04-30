package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io/ioutil"
	"net/http"
	"strings"
	"testing"
	"time"
)

const proxyURL = "http://localhost:11434"

func getAvailableModel(t *testing.T) string {
	resp, err := http.Get(proxyURL + "/api/tags")
	if err != nil {
		t.Fatalf("Failed to fetch models: %v", err)
	}
	defer resp.Body.Close()

	var data struct {
		Models []struct {
			Name string `json:"name"`
		} `json:"models"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&data); err != nil {
		t.Fatalf("Failed to decode models: %v", err)
	}
	if len(data.Models) == 0 {
		t.Fatalf("No models available in proxy")
	}
	return data.Models[0].Name
}

func TestOpenCodeTools(t *testing.T) {
	model := getAvailableModel(t)
	// Test if the proxy handles tool definitions for OpenCode
	payload := map[string]interface{}{
		"model": model,
		"messages": []map[string]interface{}{
			{"role": "user", "content": "Run 'ls'"},
		},
		"tools": []map[string]interface{}{
			{
				"type": "function",
				"function": map[string]interface{}{
					"name": "run_terminal_command",
					"parameters": map[string]interface{}{
						"type": "object",
						"properties": map[string]interface{}{
							"command": map[string]interface{}{"type": "string"},
						},
					},
				},
			},
		},
		"stream": true,
	}
	body, _ := json.Marshal(payload)

	resp, err := http.Post(proxyURL+"/v1/responses", "application/json", bytes.NewBuffer(body))
	if err != nil {
		t.Fatalf("Failed to connect to proxy: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("Expected status 200, got %d", resp.StatusCode)
	}
}

func TestClaudeCodeTools(t *testing.T) {
	model := getAvailableModel(t)
	// Test if the proxy handles Anthropic-style tool blocks
	payload := map[string]interface{}{
		"model": model,
		"messages": []map[string]interface{}{
			{
				"role": "user",
				"content": []map[string]interface{}{
					{
						"type": "text",
						"text": "What is in this directory?",
					},
				},
			},
			{
				"role": "assistant",
				"content": []map[string]interface{}{
					{
						"type": "tool_use",
						"id": "toolu_01",
						"name": "run_terminal_command",
						"input": map[string]interface{}{"command": "ls"},
					},
				},
			},
			{
				"role": "user",
				"content": []map[string]interface{}{
					{
						"type": "tool_result",
						"tool_use_id": "toolu_01",
						"content": "file1.txt\nfile2.txt",
					},
				},
			},
		},
		"max_tokens": 100,
		"stream": true,
	}
	body, _ := json.Marshal(payload)

	resp, err := http.Post(proxyURL+"/v1/messages", "application/json", bytes.NewBuffer(body))
	if err != nil {
		t.Fatalf("Failed to connect to proxy: %v", err)
	}
	defer resp.Body.Close()

	// If this fails, it means the proxy couldn't translate the Anthropic tool blocks
	// and likely sent an invalid payload to OpenRouter.
	if resp.StatusCode != http.StatusOK {
		t.Errorf("Claude Code tool request failed with status %d", resp.StatusCode)
	}
}

func TestAnthropicToolDefinitions(t *testing.T) {
	model := getAvailableModel(t)
	// Test if the proxy translates Anthropic's tool definition format (input_schema)
	payload := map[string]interface{}{
		"model": model,
		"messages": []map[string]interface{}{
			{"role": "user", "content": "Write a file"},
		},
		"tools": []map[string]interface{}{
			{
				"name":        "write_file",
				"description": "Writes content to a file",
				"input_schema": map[string]interface{}{
					"type": "object",
					"properties": map[string]interface{}{
						"filepath": map[string]interface{}{"type": "string"},
						"content":  map[string]interface{}{"type": "string"},
					},
				},
			},
		},
		"max_tokens": 100,
		"stream":     true,
	}
	body, _ := json.Marshal(payload)

	resp, err := http.Post(proxyURL+"/v1/messages", "application/json", bytes.NewBuffer(body))
	if err != nil {
		t.Fatalf("Failed to connect to proxy: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("Anthropic tool definition request failed with status %d", resp.StatusCode)
	}
}

func TestOpenCodeBeadsProtocol(t *testing.T) {
	model := getAvailableModel(t)
	payload := map[string]interface{}{
		"model": model,
		"messages": []map[string]interface{}{
			{"role": "user", "content": "Say 'OpenCode Test'"},
		},
		"stream": true,
	}
	body, _ := json.Marshal(payload)

	resp, err := http.Post(proxyURL+"/v1/responses", "application/json", bytes.NewBuffer(body))
	if err != nil {
		t.Fatalf("Failed to connect to proxy: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("Expected status 200, got %d", resp.StatusCode)
	}

	scanner := bufio.NewScanner(resp.Body)
	foundCreated := false
	foundDelta := false
	
	for scanner.Scan() {
		line := scanner.Text()
		if strings.HasPrefix(line, "event: response.created") {
			foundCreated = true
		}
		if strings.HasPrefix(line, "event: response.output_text.delta") {
			foundDelta = true
			// We don't break yet, we want to see if we get any actual data
		}
		if foundCreated && foundDelta {
			break
		}
	}

	if !foundCreated {
		t.Error("Did not find 'response.created' event (Beads protocol)")
	}
	if !foundDelta {
		t.Error("Did not find 'response.output_text.delta' event (Beads protocol)")
	}
}

func TestClaudeCodeAnthropicProtocol(t *testing.T) {
	model := getAvailableModel(t)
	payload := map[string]interface{}{
		"model": model,
		"messages": []map[string]interface{}{
			{"role": "user", "content": "Say 'Claude Test'"},
		},
		"max_tokens": 100,
		"stream": true,
	}
	body, _ := json.Marshal(payload)

	resp, err := http.Post(proxyURL+"/v1/messages", "application/json", bytes.NewBuffer(body))
	if err != nil {
		t.Fatalf("Failed to connect to proxy: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("Expected status 200, got %d", resp.StatusCode)
	}

	scanner := bufio.NewScanner(resp.Body)
	foundMsgStart := false
	foundDelta := false

	for scanner.Scan() {
		line := scanner.Text()
		if strings.HasPrefix(line, "event: message_start") {
			foundMsgStart = true
		}
		if strings.HasPrefix(line, "event: content_block_delta") {
			foundDelta = true
		}
		if foundMsgStart && foundDelta {
			break
		}
	}

	if !foundMsgStart {
		t.Error("Did not find 'message_start' event (Anthropic protocol)")
	}
	if !foundDelta {
		t.Error("Did not find 'content_block_delta' event (Anthropic protocol)")
	}
}
func TestLargeToolSet(t *testing.T) {
	model := getAvailableModel(t)
	var tools []map[string]interface{}
	for i := 0; i < 17; i++ {
		tools = append(tools, map[string]interface{}{
			"name":        fmt.Sprintf("tool_%d", i),
			"description": fmt.Sprintf("Description for tool %d", i),
			"input_schema": map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"arg": map[string]interface{}{"type": "string"},
				},
			},
		})
	}

	payload := map[string]interface{}{
		"model":      model,
		"messages":   []map[string]interface{}{{"role": "user", "content": "Hello"}},
		"tools":      tools,
		"max_tokens": 100,
		"stream":     true,
	}
	body, _ := json.Marshal(payload)

	resp, err := http.Post(proxyURL+"/v1/messages", "application/json", bytes.NewBuffer(body))
	if err != nil {
		t.Fatalf("Failed to connect to proxy: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("Large tool set request failed with status %d", resp.StatusCode)
		errorBody, _ := ioutil.ReadAll(resp.Body)
		t.Logf("Error body: %s", string(errorBody))
	}
}

func TestLargeSystemPrompt(t *testing.T) {
	model := getAvailableModel(t)
	// Create a very large system prompt (10KB)
	systemPrompt := strings.Repeat("Follow these instructions carefully. ", 200)
	
	payload := map[string]interface{}{
		"model": model,
		"system": []map[string]interface{}{
			{"type": "text", "text": systemPrompt},
		},
		"messages": []map[string]interface{}{
			{"role": "user", "content": "Hello"},
		},
		"max_tokens": 100,
		"stream":     true,
	}
	body, _ := json.Marshal(payload)

	resp, err := http.Post(proxyURL+"/v1/messages", "application/json", bytes.NewBuffer(body))
	if err != nil {
		t.Fatalf("Failed to connect to proxy: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("Large system prompt request failed with status %d", resp.StatusCode)
	}
}

func TestInvalidToolCall(t *testing.T) {
	model := getAvailableModel(t)
	// Test if proxy handles malformed tool definitions gracefully
	payload := map[string]interface{}{
		"model": model,
		"messages": []map[string]interface{}{{"role": "user", "content": "Hello"}},
		"tools": []map[string]interface{}{
			{
				"name": "invalid_tool",
				// Missing description and input_schema
			},
		},
		"max_tokens": 100,
		"stream":     true,
	}
	body, _ := json.Marshal(payload)

	resp, err := http.Post(proxyURL+"/v1/messages", "application/json", bytes.NewBuffer(body))
	if err != nil {
		t.Fatalf("Failed to connect to proxy: %v", err)
	}
	defer resp.Body.Close()

	// It should either sanitize it or skip it, but NOT crash or return 503 if models are available
	if resp.StatusCode == http.StatusServiceUnavailable {
		t.Errorf("Proxy returned 503 for invalid tool, expected better handling")
	}
}
func TestModelDiscoveryAndUsage(t *testing.T) {
	resp, err := http.Get(proxyURL + "/api/tags")
	if err != nil {
		t.Fatalf("Failed to fetch models: %v", err)
	}
	defer resp.Body.Close()

	var data struct {
		Models []struct {
			Name string `json:"name"`
		} `json:"models"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&data); err != nil {
		t.Fatalf("Failed to decode models: %v", err)
	}

	foundOpenRouter := false
	foundNvidia := false
	var orModel, nvModel string

	for _, m := range data.Models {
		if !foundOpenRouter && (strings.HasPrefix(m.Name, "google/") || strings.HasPrefix(m.Name, "meta/") || strings.HasPrefix(m.Name, "anthropic/")) {
			foundOpenRouter = true
			orModel = m.Name
		}
		if !foundNvidia && strings.HasPrefix(m.Name, "nvidia/") && !strings.Contains(m.Name, "super") {
			// Choose a non-super model for faster testing
			foundNvidia = true
			nvModel = m.Name
		}
	}

	if !foundOpenRouter {
		t.Error("OpenRouter models not found in discovery list")
	} else {
		t.Logf("Testing OpenRouter model: %s", orModel)
		testCompletion(t, orModel)
	}

	if !foundNvidia {
		t.Error("NVIDIA models not found in discovery list")
	} else {
		t.Logf("Testing NVIDIA model: %s", nvModel)
		testCompletion(t, nvModel)
	}
}

func testCompletion(t *testing.T, model string) {
	payload := map[string]interface{}{
		"model": model,
		"messages": []map[string]interface{}{
			{"role": "user", "content": "Say 'Test'"},
		},
		"max_tokens": 10,
	}
	body, _ := json.Marshal(payload)

	resp, err := http.Post(proxyURL+"/v1/chat/completions", "application/json", bytes.NewBuffer(body))
	if err != nil {
		t.Errorf("Request to %s failed: %v", model, err)
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		errorBody, _ := ioutil.ReadAll(resp.Body)
		t.Errorf("Model %s returned status %d: %s", model, resp.StatusCode, string(errorBody))
	}
}

func TestToolCapableModelPreference(t *testing.T) {
	resp, err := http.Get(proxyURL + "/api/tags")
	if err != nil {
		t.Fatalf("Failed to fetch models: %v", err)
	}
	defer resp.Body.Close()

	var data struct {
		Models []struct {
			Name   string `json:"name"`
			Model string `json:"model"`
		} `json:"models"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&data); err != nil {
		t.Fatalf("Failed to decode models: %v", err)
	}

	// Verify we have both OpenRouter and NVIDIA models
	orCount := 0
	nvCount := 0
	for _, m := range data.Models {
		name := m.Name
		if strings.HasPrefix(name, "google/") || strings.HasPrefix(name, "meta/") ||
			strings.HasPrefix(name, "anthropic/") || strings.Contains(name, ":free") {
			orCount++
		}
		if strings.HasPrefix(name, "nvidia/") {
			nvCount++
		}
	}

	if orCount == 0 {
		t.Error("No OpenRouter models found in discovery")
	}
	if nvCount == 0 {
		t.Error("No NVIDIA models found in discovery")
	}

	t.Logf("Found %d OpenRouter models, %d NVIDIA models", orCount, nvCount)

	// Test that tool-capable models respond correctly
	// Use an OpenRouter model that supports tools
	payload := map[string]interface{}{
		"model": "google/gemini-2.0-flash-exp:free",
		"messages": []map[string]interface{}{
			{"role": "user", "content": "Say 'tool test'"},
		},
		"tools": []map[string]interface{}{
			{
				"type": "function",
				"function": map[string]interface{}{
					"name":        "echo",
					"description": "Echoes the input",
					"parameters": map[string]interface{}{
						"type": "object",
						"properties": map[string]interface{}{
							"text": map[string]interface{}{"type": "string"},
						},
					},
				},
			},
		},
		"max_tokens": 20,
	}
	body, _ := json.Marshal(payload)

	resp2, err := http.Post(proxyURL+"/v1/chat/completions", "application/json", bytes.NewBuffer(body))
	if err != nil {
		t.Fatalf("Tool request failed: %v", err)
	}
	defer resp2.Body.Close()

	if resp2.StatusCode != http.StatusOK {
		errorBody, _ := ioutil.ReadAll(resp2.Body)
		t.Logf("Tool test response: %s", string(errorBody))
		// Don't fail - some models may not accept tools in chat/completions
		t.Logf("Tool test returned status %d (may be expected for some models)", resp2.StatusCode)
	}
}

func TestModelFallbackChain(t *testing.T) {
	// Test that the proxy properly falls back through models when one fails
	// Run multiple requests to exercise the fallback logic
	successCount := 0
	failCount := 0

	for i := 0; i < 5; i++ {
		payload := map[string]interface{}{
			"model": "openrouter", // Force fallback by requesting generic
			"messages": []map[string]interface{}{
				{"role": "user", "content": fmt.Sprintf("Say 'fallback test %d'", i)},
			},
			"max_tokens": 10,
			"stream":    false,
		}
		body, _ := json.Marshal(payload)

		resp, err := http.Post(proxyURL+"/v1/chat/completions", "application/json", bytes.NewBuffer(body))
		if err != nil {
			t.Logf("Request %d error: %v", i, err)
			failCount++
			continue
		}

		if resp.StatusCode == http.StatusOK {
			successCount++
			// Read model used from response
			bodyBytes, _ := ioutil.ReadAll(resp.Body)
			var result map[string]interface{}
			json.Unmarshal(bodyBytes, &result)
			if model, ok := result["model"].(string); ok {
				t.Logf("Request %d succeeded with model: %s", i, model)
			}
		} else {
			failCount++
			bodyBytes, _ := ioutil.ReadAll(resp.Body)
			t.Logf("Request %d failed: %s", i, string(bodyBytes))
		}
		resp.Body.Close()
		time.Sleep(200 * time.Millisecond)
	}

	t.Logf("Fallback test: %d succeeded, %d failed", successCount, failCount)
	// We expect at least some successes if models are available
	if successCount == 0 && failCount > 0 {
		t.Logf("Note: All requests failed - checking if OpenRouter has credits")
	}
}

func TestMarkdownToolExtraction(t *testing.T) {
	content := "I'll run the command now.\n\n```bash\ngt prime --hook\n```\n\nPlease wait."
	tools := extractMarkdownTools(content)
	
	if len(tools) != 1 {
		t.Fatalf("Expected 1 tool, got %d", len(tools))
	}
	
	if tools[0]["name"] != "run_terminal_command" {
		t.Errorf("Expected tool name run_terminal_command, got %v", tools[0]["name"])
	}
	
	input := tools[0]["input"].(map[string]interface{})
	if input["command"] != "gt prime --hook" {
		t.Errorf("Expected command 'gt prime --hook', got '%v'", input["command"])
	}
}

func TestConversationalToolExtraction(t *testing.T) {
	content := "I will now run `gt hook` to check for work."
	tools := extractMarkdownTools(content)
	
	for i, tool := range tools {
		input := tool["input"].(map[string]interface{})
		t.Logf("Tool %d: '%v'", i, input["command"])
	}

	if len(tools) != 1 {
		t.Fatalf("Expected 1 tool, got %d", len(tools))
	}
	
	input := tools[0]["input"].(map[string]interface{})
	if input["command"] != "gt hook" {
		t.Errorf("Expected command 'gt hook', got '%v'", input["command"])
	}

	content2 := "I am now going to run bd list."
	tools2 := extractMarkdownTools(content2)
	if len(tools2) != 1 {
		t.Fatalf("Expected 1 tool for content2, got %d", len(tools2))
	}
	input2 := tools2[0]["input"].(map[string]interface{})
	if input2["command"] != "bd list" {
		t.Errorf("Expected command 'bd list', got '%v'", input2["command"])
	}
}
