package main

import (
	"os"
	"strings"
)

var geminiOpenAIBaseURL = "https://generativelanguage.googleapis.com/v1beta/openai"

// geminiModel routes a Freeride model id to a Google Gemini API model name (OpenAI-compatible endpoint).
type geminiModel struct {
	ID       string `yaml:"id"`
	Model    string `yaml:"model"`
	Cooldown string `yaml:"cooldown,omitempty"`
}

func resolveGeminiAPIKey() string {
	for _, key := range []string{"GEMINI_API_KEY", "GOOGLE_API_KEY"} {
		if v := strings.TrimSpace(os.Getenv(key)); v != "" {
			return v
		}
	}
	return ""
}

func geminiDirectEnabledFor(conf modelsConfig) bool {
	if resolveGeminiAPIKey() == "" {
		return false
	}
	return len(conf.GeminiModels) > 0
}

func geminiDirectAvailable() bool {
	configMutex.RLock()
	defer configMutex.RUnlock()
	return geminiDirectEnabledFor(globalModelsConfig)
}

func lookupGeminiModel(id string) *geminiModel {
	configMutex.RLock()
	defer configMutex.RUnlock()
	for i := range globalModelsConfig.GeminiModels {
		m := &globalModelsConfig.GeminiModels[i]
		if m.ID == id {
			return m
		}
	}
	return nil
}

func geminiAPIModelName(m *geminiModel, candidate string) string {
	if m != nil && strings.TrimSpace(m.Model) != "" {
		return strings.TrimSpace(m.Model)
	}
	name := strings.TrimPrefix(candidate, "google/")
	return strings.TrimSuffix(name, ":free")
}

func isGeminiDirectModelID(id string) bool {
	return lookupGeminiModel(id) != nil
}
