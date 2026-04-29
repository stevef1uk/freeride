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
