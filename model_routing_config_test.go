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
	if !hasModel(config.CuratedPaid, glmModel) {
		t.Fatalf("curatedPaid = %v, want %s", config.CuratedPaid, glmModel)
	}
	for _, role := range []string{"polecat", "architect", "setup"} {
		if !hasModel(config.RolePrepend[role], glmModel) {
			t.Errorf("rolePrepend:%s = %v, want %s", role, config.RolePrepend[role], glmModel)
		}
	}
	testerModels := config.RolePrepend["tester"]
	if len(testerModels) == 0 || testerModels[0] != "openai/gpt-5.6-luna" {
		t.Fatalf("tester primary = %v, want openai/gpt-5.6-luna first", testerModels)
	}
	if hasModel(testerModels, glmModel) {
		t.Errorf("tester candidate list still contains %s", glmModel)
	}
}

func TestPlannerRoutingExcludesGLMToolCallFormatting(t *testing.T) {
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

	plannerModels := config.RolePrepend["planner"]
	if len(plannerModels) == 0 || plannerModels[0] != "openai/gpt-5.6-luna" {
		t.Fatalf("planner primary = %v, want openai/gpt-5.6-luna first", plannerModels)
	}
	const glmModel = "z-ai/glm-5.3-flash"
	for _, model := range plannerModels {
		if strings.EqualFold(model, glmModel) {
			t.Fatalf("planner candidate list still contains excluded model %s: %v", glmModel, plannerModels)
		}
	}
}
