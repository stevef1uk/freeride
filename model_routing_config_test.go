package main

import (
	"os"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestQARoutingExcludesGLMEmptyResponses(t *testing.T) {
	data, err := os.ReadFile("models.yaml")
	if err != nil {
		t.Fatal(err)
	}

	var config struct {
		ExcludeModels []string            `yaml:"excludeModels"`
		RolePrepend   map[string][]string `yaml:"rolePrepend"`
	}
	if err := yaml.Unmarshal(data, &config); err != nil {
		t.Fatal(err)
	}

	const rejectedModel = "z-ai/glm-5.3-flash"
	excluded := false
	for _, model := range config.ExcludeModels {
		if model == rejectedModel {
			excluded = true
			break
		}
	}
	if !excluded {
		t.Fatalf("%s must be excluded from model candidates", rejectedModel)
	}

	qaModels := config.RolePrepend["qa"]
	if len(qaModels) == 0 || qaModels[0] != "deepseek/deepseek-v4-flash" {
		t.Fatalf("QA primary = %v, want deepseek/deepseek-v4-flash first", qaModels)
	}
	for _, model := range qaModels {
		if strings.EqualFold(model, rejectedModel) {
			t.Fatalf("QA candidate list still contains excluded model %s", rejectedModel)
		}
	}
}
