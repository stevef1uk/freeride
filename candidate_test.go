package main

import (
	"testing"
)

func TestSelectCandidates_CerebrasSensibleRouting(t *testing.T) {
	conf := modelsConfig{
		CerebrasBudget: []string{
			"cerebras/llama3.1-8b",
			"cerebras/gpt-oss-120b",
		},
		CerebrasPerformance: []string{
			"cerebras/llama3.3-70b",
		},
		NvidiaReliable: []string{
			"nvidia/llama-3.1-70b-instruct",
		},
	}

	cerebrasModels := []cerebrasModel{
		{ID: "llama3.1-8b"},
		{ID: "gpt-oss-120b"},
		{ID: "llama3.3-70b"},
		{ID: "new-preview-model"},
	}

	tests := []struct {
		name             string
		role             string
		isComplexRequest bool
		cooldowns        map[string]bool
		expectedFirst    string
		contains         string
		allowPaid        bool
	}{
		{
			name:             "Simple request prioritizes budget",
			role:             "user",
			isComplexRequest: false,
			expectedFirst:    "cerebras/llama3.1-8b",
			allowPaid:        true,
		},
		{
			name:             "Complex request prioritizes performance after budget",
			role:             "architect",
			isComplexRequest: true,
			expectedFirst:    "cerebras/gpt-oss-120b",
			contains:         "cerebras/llama3.3-70b",
			allowPaid:        true,
		},
		{
			name:             "Cooldown skips model",
			role:             "user",
			isComplexRequest: false,
			cooldowns:        map[string]bool{"cerebras/llama3.1-8b": true},
			expectedFirst:    "cerebras/gpt-oss-120b",
			allowPaid:        true,
		},
		{
			name:             "Dynamic budget model added even for simple",
			role:             "user",
			isComplexRequest: false,
			cooldowns:        map[string]bool{"cerebras/llama3.1-8b": true, "cerebras/gpt-oss-120b": true},
			expectedFirst:    "cerebras/new-preview-model",
			allowPaid:        true,
		},
		{
			name:             "Simple request tries free models before paid performance fallback",
			role:             "user",
			isComplexRequest: false,
			cooldowns:        map[string]bool{"cerebras/llama3.1-8b": true, "cerebras/gpt-oss-120b": true, "cerebras/new-preview-model": true},
			expectedFirst:    "nvidia/llama-3.1-70b-instruct",
			allowPaid:        true,
		},
		{
			name:             "Complex request skips performance if allowPaid is false",
			role:             "architect",
			isComplexRequest: true,
			allowPaid:        false,
			expectedFirst:    "cerebras/gpt-oss-120b",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := candidateContext{
				role:             tt.role,
				conf:             conf,
				cerebrasModels:   cerebrasModels,
				isComplexRequest: tt.isComplexRequest,
				allowPaid:        tt.allowPaid,
				isCooldown: func(m string) bool {
					return tt.cooldowns[m]
				},
				isExcluded: func(m string) bool { return false },
			}

			candidates := selectCandidates(ctx)

			if len(candidates) == 0 {
				t.Fatalf("Expected candidates, got none")
			}

			if candidates[0] != tt.expectedFirst {
				t.Errorf("Expected first candidate %s, got %s", tt.expectedFirst, candidates[0])
			}

			if tt.contains != "" {
				found := false
				for _, c := range candidates {
					if c == tt.contains {
						found = true
						break
					}
				}
				if !found {
					t.Errorf("Expected candidates to contain %s, but it didn't. Candidates: %v", tt.contains, candidates)
				}
			}
		})
	}
}

func TestSelectCandidates_RoleMassiveRequirement(t *testing.T) {
	conf := modelsConfig{
		ReliableFree: []string{
			"google/gemini-2.0-flash-exp:free", // Not massive
			"meta-llama/llama-3.1-405b:free",   // Massive
		},
	}

	tests := []struct {
		name          string
		role          string
		expectedFirst string
	}{
		{
			name:          "User gets first available",
			role:          "user",
			expectedFirst: "google/gemini-2.0-flash-exp:free",
		},
		{
			name:          "Architect requires massive",
			role:          "architect",
			expectedFirst: "meta-llama/llama-3.1-405b:free",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := candidateContext{
				role:             tt.role,
				conf:             conf,
				isComplexRequest: true,
				isCooldown:       func(m string) bool { return false },
				isExcluded:       func(m string) bool { return false },
				models: []openRouterModel{
					{ID: "google/gemini-2.0-flash-exp:free"},
					{ID: "meta-llama/llama-3.1-405b:free"},
				},
			}

			candidates := selectCandidates(ctx)

			if len(candidates) == 0 {
				t.Fatalf("Expected candidates, got none")
			}

			if candidates[0] != tt.expectedFirst {
				t.Errorf("Expected first candidate %s, got %s", tt.expectedFirst, candidates[0])
			}
		})
	}
}

func TestSelectCandidates_LocalOpenAI(t *testing.T) {
	conf := modelsConfig{
		ReliableFree: []string{"small/free:free"},
		LocalOpenAI: []localOpenAIModel{
			{ID: "local/qwen3-coder", Endpoint: "http://127.0.0.1:8080", Model: "qwen3-coder"},
		},
	}
	ctx := candidateContext{
		role:             "user",
		conf:             conf,
		isComplexRequest: false,
		allowLocalOpenAI: false,
		isCooldown:       func(m string) bool { return false },
		isExcluded:       func(m string) bool { return false },
		models: []openRouterModel{
			{ID: "small/free:free", Pricing: struct {
				Prompt     string `json:"prompt"`
				Completion string `json:"completion"`
			}{Prompt: "0", Completion: "0"}},
		},
	}
	off := selectCandidates(ctx)
	for _, c := range off {
		if c == "local/qwen3-coder" {
			t.Fatalf("expected local OpenAI ids omitted without flag, got %v", off)
		}
	}

	ctx.allowLocalOpenAI = true
	on := selectCandidates(ctx)
	if len(on) == 0 {
		t.Fatal("expected candidates")
	}
	if on[len(on)-1] != "local/qwen3-coder" {
		t.Fatalf("expected local model last, got %v", on)
	}
}
