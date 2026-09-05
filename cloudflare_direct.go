package main

import (
	"os"
	"strings"
)

// cloudflareOpenAIBaseURL is overridable for testing.
var cloudflareOpenAIBaseURL = "https://api.cloudflare.com/client/v4/accounts"

// cloudflareModel routes a Freeride model id to a Cloudflare Workers AI model name.
type cloudflareModel struct {
	ID       string `yaml:"id"`
	Model    string `yaml:"model"`
	Cooldown string `yaml:"cooldown,omitempty"`
}

func resolveCloudflareAccountID() string {
	return strings.TrimSpace(os.Getenv("CLOUDFLARE_ACCOUNT_ID"))
}

func resolveCloudflareAPIToken() string {
	return strings.TrimSpace(os.Getenv("CLOUDFLARE_API_TOKEN"))
}

// cloudflareBaseURL returns the base URL for Cloudflare Workers AI API.
func cloudflareBaseURL() string {
	acct := resolveCloudflareAccountID()
	if acct == "" {
		return ""
	}
	return cloudflareOpenAIBaseURL + "/" + acct + "/ai/v1"
}

func cloudflareDirectEnabledFor(conf modelsConfig) bool {
	if resolveCloudflareAccountID() == "" || resolveCloudflareAPIToken() == "" {
		return false
	}
	return len(conf.CloudflareModels) > 0
}

func cloudflareDirectAvailable() bool {
	configMutex.RLock()
	defer configMutex.RUnlock()
	return cloudflareDirectEnabledFor(globalModelsConfig)
}

func lookupCloudflareModel(id string) *cloudflareModel {
	configMutex.RLock()
	defer configMutex.RUnlock()
	for i := range globalModelsConfig.CloudflareModels {
		m := &globalModelsConfig.CloudflareModels[i]
		if m.ID == id {
			return m
		}
	}
	return nil
}

func cloudflareAPIModelName(m *cloudflareModel, candidate string) string {
	if m != nil && strings.TrimSpace(m.Model) != "" {
		return strings.TrimSpace(m.Model)
	}
	return strings.TrimPrefix(candidate, "cloudflare/")
}

func isCloudflareDirectModelID(id string) bool {
	return lookupCloudflareModel(id) != nil
}
