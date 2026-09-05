package main

import "testing"

func TestIsNvidiaNIMModel(t *testing.T) {
	tests := []struct {
		model string
		want  bool
	}{
		// NVIDIA NIM models (should route to integrate.api.nvidia.com)
		{"nvidia/nemotron-3-super-120b-a12b", true},
		{"nvidia/nemotron-3-ultra-550b-a55b", true},
		{"nvidia/llama-3.1-70b-instruct", true},
		{"meta/llama-3.3-70b-instruct", true},
		{"mistralai/mistral-large-latest", true},
		{"microsoft/phi-4", true},
		{"qwen/qwen3-235b-a22b", true},
		{"abacusai/smaug-72b", true},
		{"ai21labs/jamba-instruct", true},
		{"01-ai/yi-large", true},

		// OpenRouter :free models with nvidia/ prefix (must NOT route to NIM)
		{"nvidia/nemotron-3.5-lightning:free", false},
		{"nvidia/nemotron-3-ultra-550b-a55b:free", false},
		{"nvidia/nemotron-3-super-120b-a12b:free", false},

		// OpenRouter :free models with other NIM prefixes
		{"meta/llama-3.3-70b-instruct:free", false},
		{"qwen/qwen3-80b-a3b-instruct:free", false},

		// Non-NIM models (should never route to NIM)
		{"minimax/minimax-m3:free", false},
		{"openai/gpt-5.6-luna", false},
		{"google/gemini-3.5-flash", false},
		{"cloudflare/llama-3.3-70b", false},
		{"deepseek/deepseek-v4-flash", false},
		{"cerebras/llama-3.3-70b", false},
		{"groq/llama-3.3-70b-versatile", false},
		{"ollama/llama3.3:70b", false},
	}

	for _, tt := range tests {
		t.Run(tt.model, func(t *testing.T) {
			got := isNvidiaNIMModel(tt.model)
			if got != tt.want {
				t.Errorf("isNvidiaNIMModel(%q) = %v, want %v", tt.model, got, tt.want)
			}
		})
	}
}
