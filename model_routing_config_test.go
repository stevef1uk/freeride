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
		CuratedPaid []string            `yaml:"curatedPaid"`
		RolePrepend map[string][]string `yaml:"rolePrepend"`
	}
	if err := yaml.Unmarshal(data, &config); err != nil {
		t.Fatal(err)
	}

	const glmModel = "z-ai/glm-5.3-flash"
	hasModel := func(models []string, want string) bool {
		for _, model := range models {
			if strings.EqualFold(model, want) {
				return true
			}
		}
		return false
	}

	qaModels := config.RolePrepend["qa"]
	if len(qaModels) == 0 || qaModels[0] != "~deepseek/deepseek-v4-flash-latest" {
		t.Fatalf("QA primary = %v, want ~deepseek/deepseek-v4-flash-latest first", qaModels)
	}
	if hasModel(qaModels, glmModel) {
		t.Fatalf("QA candidate list still contains excluded model %s", glmModel)
	}
	if !hasModel(qaModels, "openai/gpt-6-luna") {
		t.Fatalf("QA candidate list must contain openai/gpt-6-luna as fallback, got %v", qaModels)
	}
	if hasModel(config.CuratedPaid, glmModel) {
		t.Fatalf("curatedPaid = %v, must not contain %s", config.CuratedPaid, glmModel)
	}
	for role, models := range config.RolePrepend {
		if hasModel(models, glmModel) {
			t.Errorf("rolePrepend:%s = %v, must not contain %s", role, models, glmModel)
		}
	}
	testerModels := config.RolePrepend["tester"]
	if len(testerModels) == 0 {
		t.Fatalf("tester primary is empty")
	}
	if hasModel(testerModels, glmModel) {
		t.Errorf("tester candidate list still contains %s", glmModel)
	}
}

func TestPlannerAndArchitectRoutingExcludeGLMToolCallFormatting(t *testing.T) {
	data, err := os.ReadFile("models.yaml")
	if err != nil {
		t.Fatal(err)
	}

	var config struct {
		RolePrepend map[string][]string `yaml:"rolePrepend"`
	}
	if err := yaml.Unmarshal(data, &config); err != nil {
		t.Fatal(err)
	}

	const glmModel = "z-ai/glm-5.3-flash"
	for _, role := range []string{"planner", "architect"} {
		models := config.RolePrepend[role]
		if len(models) == 0 {
			t.Errorf("%s candidate list is empty", role)
		}
		for _, model := range models {
			if strings.EqualFold(model, glmModel) {
				t.Errorf("rolePrepend:%s still contains excluded model %s: %v", role, glmModel, models)
			}
		}
	}
}
