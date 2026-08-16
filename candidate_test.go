package main

import (
	"strings"
	"testing"
)

// testMassivePatterns mirrors the models.yaml massiveModelPatterns list used in
// production. Massive classification is fully config-driven (no hardcoded list
// in code), so tests must provide their own patterns.
var testMassivePatterns = []string{
	"671b", "550b", "397b", "235b", "1t", "120b", "large", "480b",
	"405b", "90b", "80b", "70b", "30b", "sonnet", "gpt-4", "gpt-5.6",
	"gemini", "opus", "deepseek-v4",
}

func TestSelectCandidates_CerebrasSensibleRouting(t *testing.T) {
	const (
		budgetSmall  = "cerebras/budget-small"
		budgetLarge  = "cerebras/budget-large"
		perf70       = "cerebras/perf-70b"
		nvidiaBig    = "test/nvidia-big-70b"
		cbPreviewNew = "cerebras/preview-new"
	)
	prevCfg := globalModelsConfig
	t.Cleanup(func() {
		configMutex.Lock()
		globalModelsConfig = prevCfg
		configMutex.Unlock()
	})
	conf := modelsConfig{
		CerebrasBudget: []string{
			budgetSmall,
			budgetLarge,
		},
		CerebrasPerformance: []string{
			perf70,
		},
		NvidiaReliable: []string{
			nvidiaBig,
		},
		MassiveModelPatterns: testMassivePatterns,
		ModelClassification: modelClassificationConfig{
			CerebrasBudgetMarkers: []string{"8b", "preview", "oss", "qwen-3"},
		},
	}
	configMutex.Lock()
	globalModelsConfig = conf
	configMutex.Unlock()

	cerebrasModels := []cerebrasModel{
		{ID: "budget-small"},
		{ID: "budget-large"},
		{ID: "perf-70b"},
		{ID: "preview-new"},
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
			expectedFirst:    budgetSmall,
			allowPaid:        true,
		},
		{
			name:             "Complex request prioritizes Cerebras performance first",
			role:             "architect",
			isComplexRequest: true,
			expectedFirst:    perf70,
			contains:         budgetLarge,
			allowPaid:        true,
		},
		{
			name:             "Cooldown skips model",
			role:             "user",
			isComplexRequest: false,
			cooldowns:        map[string]bool{budgetSmall: true},
			expectedFirst:    cbPreviewNew, // budget-large is massive (tier 0.05 only when complex)
			allowPaid:        true,
		},
		{
			name:             "Dynamic budget model added even for simple",
			role:             "user",
			isComplexRequest: false,
			cooldowns:        map[string]bool{budgetSmall: true, budgetLarge: true},
			expectedFirst:    cbPreviewNew,
			allowPaid:        true,
		},
		{
			name:             "Simple request tries free models before paid performance fallback",
			role:             "user",
			isComplexRequest: false,
			cooldowns:        map[string]bool{budgetSmall: true, budgetLarge: true, cbPreviewNew: true},
			expectedFirst:    nvidiaBig,
			allowPaid:        true,
		},
		{
			name:             "Complex request skips performance if allowPaid is false",
			role:             "architect",
			isComplexRequest: true,
			allowPaid:        false,
			expectedFirst:    budgetLarge,
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
	const (
		smallFree = "test/small-free"
		big70Free = "test/big-70b-free"
	)
	prevCfg := globalModelsConfig
	t.Cleanup(func() {
		configMutex.Lock()
		globalModelsConfig = prevCfg
		configMutex.Unlock()
	})
	conf := modelsConfig{
		ReliableFree: []string{
			smallFree,
			big70Free,
		},
		MassiveModelPatterns: testMassivePatterns,
	}
	configMutex.Lock()
	globalModelsConfig = conf
	configMutex.Unlock()

	tests := []struct {
		name          string
		role          string
		expectedFirst string
	}{
		{
			name:          "User gets first available",
			role:          "user",
			expectedFirst: smallFree,
		},
		{
			name:          "Architect requires massive",
			role:          "architect",
			expectedFirst: big70Free,
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
					{ID: smallFree},
					{ID: big70Free},
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
	const (
		freeOR   = "test/free-or"
		localGPU = "local/test-gpu"
	)
	conf := modelsConfig{
		ReliableFree: []string{freeOR},
		LocalOpenAI: []localOpenAIModel{
			{ID: localGPU, Endpoint: "http://127.0.0.1:8080", Model: "upstream-weights"},
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
			{ID: freeOR, Pricing: struct {
				Prompt     string `json:"prompt"`
				Completion string `json:"completion"`
			}{Prompt: "0", Completion: "0"}},
		},
	}
	off := selectCandidates(ctx)
	for _, c := range off {
		if c == localGPU {
			t.Fatalf("expected local OpenAI ids omitted without flag, got %v", off)
		}
	}

	ctx.allowLocalOpenAI = true
	on := selectCandidates(ctx)
	if len(on) == 0 {
		t.Fatal("expected candidates")
	}
	if on[len(on)-1] != localGPU {
		t.Fatalf("expected local model last (fallback) when enabled, got %v", on)
	}
	if on[0] == localGPU {
		t.Fatalf("local must not be first candidate (cloud-first policy), got %v", on)
	}
}

func TestLocalGPUBlockPatternMatches_GeminiNotMini(t *testing.T) {
	if localGPUBlockPatternMatches("google/gemini-3.5-flash", "mini") {
		t.Fatal("pattern mini must not match gemini id")
	}
	if !localGPUBlockPatternMatches("nvidia/nemotron-mini-4b-instruct", "mini") {
		t.Fatal("pattern mini should match nemotron-mini")
	}
}

func TestIsBlockedSmallCloudWhenLocalGPU(t *testing.T) {
	const (
		localGPU     = "local/test-gpu"
		blockTarget  = "test/block-target"
		nanoPattern  = "test/vendor-nano-8b"
	)
	prevAllow := allowLocalOpenAI
	prevCfg := globalModelsConfig
	t.Cleanup(func() {
		allowLocalOpenAI = prevAllow
		configMutex.Lock()
		globalModelsConfig = prevCfg
		configMutex.Unlock()
	})

	configMutex.Lock()
	globalModelsConfig = modelsConfig{
		LocalOpenAI: []localOpenAIModel{
			{ID: localGPU, Endpoint: "http://127.0.0.1:8080", Model: "upstream"},
		},
		BlockSmallCloudWhenLocalGPU: blockSmallCloudWhenLocalGPUConfig{
			Models:   []string{blockTarget},
			Patterns: []string{"nano", "mini"},
		},
	}
	configMutex.Unlock()
	allowLocalOpenAI = true

	if !isBlockedSmallCloudWhenLocalGPU(blockTarget) {
		t.Error("expected explicit block list entry to block")
	}
	if !isBlockedSmallCloudWhenLocalGPU(nanoPattern) {
		t.Error("expected pattern 'nano' to block")
	}
	if isBlockedSmallCloudWhenLocalGPU("google/gemini-3.5-flash") {
		t.Error("pattern 'mini' must not block gemini models (substring false positive)")
	}
	if !isBlockedSmallCloudWhenLocalGPU("nvidia/nemotron-mini-4b-instruct") {
		t.Error("expected '-mini' style id to still block with pattern 'mini'")
	}
	if isBlockedSmallCloudWhenLocalGPU(localGPU) {
		t.Error("local route id should not be blocked")
	}
	if !isCandidateExcluded(blockTarget) {
		t.Error("isCandidateExcluded should include local-GPU block")
	}

	allowLocalOpenAI = false
	if isBlockedSmallCloudWhenLocalGPU(blockTarget) {
		t.Error("block list inactive without --allow-local-openai")
	}
}

func TestSelectCandidates_LocalGPUBlocksSmallCloud(t *testing.T) {
	const (
		cbSmall  = "cerebras/small"
		cbLarge  = "cerebras/large-120b"
		localGPU = "local/test-gpu"
	)
	prevAllow := allowLocalOpenAI
	prevCfg := globalModelsConfig
	t.Cleanup(func() {
		allowLocalOpenAI = prevAllow
		configMutex.Lock()
		globalModelsConfig = prevCfg
		configMutex.Unlock()
	})

	conf := modelsConfig{
		CerebrasBudget: []string{"cerebras/small", "cerebras/large-120b"},
		LocalOpenAI: []localOpenAIModel{
			{ID: localGPU, Endpoint: "http://127.0.0.1:8080", Model: "upstream"},
		},
		BlockSmallCloudWhenLocalGPU: blockSmallCloudWhenLocalGPUConfig{
			Models:   []string{cbSmall},
			Patterns: []string{"nano"},
		},
		MassiveModelPatterns: testMassivePatterns,
	}
	configMutex.Lock()
	globalModelsConfig = conf
	configMutex.Unlock()
	allowLocalOpenAI = true

	ctx := candidateContext{
		role:             "planner",
		conf:             conf,
		isComplexRequest: true,
		allowLocalOpenAI: true,
		allowPaid:        true,
		isCooldown:       func(m string) bool { return false },
		isExcluded:       isCandidateExcluded,
		cerebrasModels: []cerebrasModel{
			{ID: "small"},
			{ID: "large-120b"},
		},
	}

	candidates := selectCandidates(ctx)
	if len(candidates) == 0 {
		t.Fatal("expected candidates")
	}
	if candidates[0] == localGPU {
		t.Fatalf("local must not be first (cloud-first), got %v", candidates)
	}
	if candidates[len(candidates)-1] != localGPU {
		t.Fatalf("expected local last as fallback, got %v", candidates)
	}
	for _, c := range candidates {
		if c == cbSmall {
			t.Fatalf("small cerebras should be blocked in local GPU mode, got %v", candidates)
		}
	}
}

func TestSelectCandidates_PlannerKeepsLocalOpenAI(t *testing.T) {
	const (
		freeOR   = "test/free-or"
		localGPU = "local/my-coder"
	)
	prevAllow := allowLocalOpenAI
	prevCfg := globalModelsConfig
	t.Cleanup(func() {
		allowLocalOpenAI = prevAllow
		configMutex.Lock()
		globalModelsConfig = prevCfg
		configMutex.Unlock()
	})

	conf := modelsConfig{
		ReliableFree: []string{freeOR},
		LocalOpenAI: []localOpenAIModel{
			{ID: localGPU, Endpoint: "http://127.0.0.1:8080", Model: "upstream-name"},
		},
		MassiveModelPatterns: testMassivePatterns,
	}
	configMutex.Lock()
	globalModelsConfig = conf
	configMutex.Unlock()
	allowLocalOpenAI = true

	ctx := candidateContext{
		role:             "planner",
		conf:             conf,
		isComplexRequest: true,
		allowLocalOpenAI: true,
		isCooldown:       func(m string) bool { return false },
		isExcluded:       isCandidateExcluded,
		models: []openRouterModel{
			{ID: freeOR},
		},
	}

	candidates := selectCandidates(ctx)
	foundLocal := false
	for _, c := range candidates {
		if c == localGPU {
			foundLocal = true
			break
		}
	}
	if !foundLocal {
		t.Fatalf("planner should retain localOpenAI id after massive filter, got %v", candidates)
	}
}

func TestSelectCandidates_CerebrasBeforeNvidiaForPolecat(t *testing.T) {
	const (
		meta70    = "meta/llama-3.3-70b-instruct"
		cb235     = "cerebras/qwen-3-235b-a22b-instruct-2507"
		cb120     = "cerebras/gpt-oss-120b"
		nvidiaBig = "nvidia/llama-3.3-nemotron-super-49b-v1"
		localGPU  = "local/qwen3-coder-30b"
	)
	prevAllow := allowLocalOpenAI
	prevCfg := globalModelsConfig
	t.Cleanup(func() {
		allowLocalOpenAI = prevAllow
		configMutex.Lock()
		globalModelsConfig = prevCfg
		configMutex.Unlock()
	})

	conf := modelsConfig{
		CerebrasBudget: []string{"cerebras/llama3.1-8b", cb120, cb235},
		NvidiaReliable: []string{nvidiaBig, meta70},
		LocalOpenAI: []localOpenAIModel{
			{ID: localGPU, Endpoint: "http://127.0.0.1:8090", Model: "upstream"},
		},
		BlockSmallCloudWhenLocalGPU: blockSmallCloudWhenLocalGPUConfig{
			Models:   []string{"cerebras/llama3.1-8b"},
			Patterns: []string{"nano", "mini"},
		},
		MassiveOnlyRoles:    []string{"polecat"},
		MassiveModelPatterns: testMassivePatterns,
	}
	configMutex.Lock()
	globalModelsConfig = conf
	configMutex.Unlock()
	allowLocalOpenAI = true

	ctx := candidateContext{
		role:             "polecat",
		originalModel:    meta70,
		conf:             conf,
		isComplexRequest: true,
		allowPaid:        true,
		allowLocalOpenAI: true,
		isCooldown:       func(m string) bool { return false },
		isExcluded:       isCandidateExcluded,
	}
	candidates := selectCandidates(ctx)
	if len(candidates) == 0 {
		t.Fatal("expected candidates")
	}
	if !strings.HasPrefix(candidates[0], "cerebras/") {
		t.Fatalf("expected Cerebras first for polecat, got %v", candidates)
	}
	nv := indexOfCandidate(candidates, nvidiaBig)
	cb := indexOfCandidate(candidates, cb235)
	if nv >= 0 && cb >= 0 && cb > nv {
		t.Fatalf("Cerebras should precede NVIDIA, order=%v", candidates)
	}
	for _, c := range candidates {
		if c == "cerebras/llama3.1-8b" {
			t.Fatalf("8b cerebras should be blocked for polecat local-GPU mode, got %v", candidates)
		}
	}
}

func TestSelectCandidates_PolecatCapableCloudBeforeLocal(t *testing.T) {
	const (
		meta70     = "meta/llama-3.3-70b-instruct"
		cb235      = "cerebras/qwen-3-235b-a22b-instruct-2507"
		nvidiaBig  = "nvidia/llama-3.3-nemotron-super-49b-v1"
		localGPU   = "local/qwen3-coder-30b"
		paidClaude = "anthropic/claude-3.5-sonnet"
	)
	prevAllow := allowLocalOpenAI
	prevCfg := globalModelsConfig
	t.Cleanup(func() {
		allowLocalOpenAI = prevAllow
		configMutex.Lock()
		globalModelsConfig = prevCfg
		configMutex.Unlock()
	})

	conf := modelsConfig{
		CerebrasBudget: []string{"cerebras/llama3.1-8b", cb235},
		NvidiaReliable: []string{nvidiaBig},
		ReliableFree:   []string{meta70},
		LocalOpenAI: []localOpenAIModel{
			{ID: localGPU, Endpoint: "http://127.0.0.1:8090", Model: "upstream"},
		},
		BlockSmallCloudWhenLocalGPU: blockSmallCloudWhenLocalGPUConfig{
			Models:   []string{"cerebras/llama3.1-8b"},
			Patterns: []string{"nano", "mini"},
		},
		RolePrepend: map[string][]string{
			"polecat": {paidClaude},
		},
		MassiveOnlyRoles:    []string{"polecat"},
		MassiveModelPatterns: testMassivePatterns,
	}
	configMutex.Lock()
	globalModelsConfig = conf
	configMutex.Unlock()
	allowLocalOpenAI = true

	ctx := candidateContext{
		role:             "polecat",
		originalModel:    meta70,
		conf:             conf,
		isComplexRequest: true,
		allowPaid:        true,
		allowLocalOpenAI: true,
		isCooldown:       func(m string) bool { return false },
		isExcluded:       isCandidateExcluded,
	}
	candidates := selectCandidates(ctx)
	if len(candidates) == 0 {
		t.Fatal("expected candidates")
	}
	if candidates[0] == localGPU {
		t.Fatalf("local must not be first, got %v", candidates)
	}
	if len(candidates) == 1 {
		t.Fatalf("must not be local-only, got %v", candidates)
	}
	if indexOfCandidate(candidates, localGPU) < 0 {
		t.Fatalf("expected local fallback in list, got %v", candidates)
	}
	if indexOfCandidate(candidates, meta70) < 0 {
		t.Fatalf("expected meta/70b-class cloud in list, got %v", candidates)
	}
}

func TestSelectCandidates_RolePrependBeforeOriginal(t *testing.T) {
	const (
		deepseek = "deepseek/deepseek-v4-flash"
		cb120    = "cerebras/gpt-oss-120b"
		gptLuna  = "openai/gpt-5.6-luna"
	)
	prevCfg := globalModelsConfig
	t.Cleanup(func() {
		configMutex.Lock()
		globalModelsConfig = prevCfg
		configMutex.Unlock()
	})

	tests := []struct {
		name           string
		role           string
		originalModel  string
		beforeOriginal []string
		prepend        []string
		allowPaid      bool
		expectedFirst  string
		expectPrepend  bool
	}{
		{
			name:           "Architect prepends before originalModel with allowPaid",
			role:           "architect",
			originalModel:  deepseek,
			beforeOriginal: []string{"architect", "planner", "qa"},
			prepend:        []string{cb120, deepseek},
			allowPaid:      true,
			expectedFirst:  cb120,
			expectPrepend:  true,
		},
		{
			name:           "Polecat keeps originalModel first when not in list",
			role:           "polecat",
			originalModel:  deepseek,
			beforeOriginal: []string{"architect", "planner", "qa"},
			prepend:        []string{cb120},
			allowPaid:      true,
			expectedFirst:  deepseek,
			expectPrepend:  true,
		},
		{
			name:           "Architect prepends skipped without allowPaid",
			role:           "architect",
			originalModel:  cb120,
			beforeOriginal: []string{"architect", "planner", "qa"},
			prepend:        []string{gptLuna},
			allowPaid:      false,
			expectPrepend:  false,
		},
		{
			name:           "QA prepends before originalModel when listed",
			role:           "qa",
			beforeOriginal: []string{"architect", "planner", "qa"},
			prepend:        []string{gptLuna},
			allowPaid:      true,
			expectedFirst:  gptLuna,
			expectPrepend:  true,
		},
		{
			name:           "Polecat with luna prepend goes strictly first when listed",
			role:           "polecat",
			originalModel:  deepseek,
			beforeOriginal: []string{"architect", "planner", "qa", "polecat"},
			prepend:        []string{gptLuna, cb120, deepseek},
			allowPaid:      true,
			expectedFirst:  gptLuna,
			expectPrepend:  true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			conf := modelsConfig{
				RolePrepend:               map[string][]string{tt.role: tt.prepend},
				RolePrependBeforeOriginal: tt.beforeOriginal,
				MassiveModelPatterns:      testMassivePatterns,
			}
			configMutex.Lock()
			globalModelsConfig = conf
			configMutex.Unlock()

			ctx := candidateContext{
				role:             tt.role,
				originalModel:    tt.originalModel,
				conf:             conf,
				isComplexRequest: true,
				allowPaid:        tt.allowPaid,
				isCooldown:       func(m string) bool { return false },
				isExcluded:       func(m string) bool { return false },
			}

			candidates := selectCandidates(ctx)
			if len(candidates) == 0 {
				t.Fatalf("expected candidates, got none")
			}
			for _, p := range tt.prepend {
				if indexOfCandidate(candidates, p) >= 0 != tt.expectPrepend {
					t.Errorf("prepend %s present=%v, want %v. Candidates: %v", p, indexOfCandidate(candidates, p) >= 0, tt.expectPrepend, candidates)
				}
			}
			if tt.expectedFirst != "" && candidates[0] != tt.expectedFirst {
				t.Errorf("expected first candidate %s, got %s", tt.expectedFirst, candidates[0])
			}
			di := indexOfCandidate(candidates, tt.originalModel)
			for _, p := range tt.prepend {
				pi := indexOfCandidate(candidates, p)
				if pi < 0 || di < 0 {
					continue
				}
				if tt.expectPrepend && tt.expectedFirst == p && tt.role != "polecat" && pi > di {
					t.Errorf("prepend %s should come before originalModel for %s, order=%v", p, tt.role, candidates)
				}
				if tt.role == "polecat" && pi < di && p != tt.originalModel {
					inList := false
					for _, b := range tt.beforeOriginal {
						if b == "polecat" {
							inList = true
							break
						}
					}
					if !inList {
						t.Errorf("polecat prepend %s should come after originalModel, order=%v", p, candidates)
					}
				}
			}
		})
	}
}

func TestRolePrependsBeforeOriginal_DefaultAndConfig(t *testing.T) {
	prevCfg := globalModelsConfig
	t.Cleanup(func() {
		configMutex.Lock()
		globalModelsConfig = prevCfg
		configMutex.Unlock()
	})

	configMutex.Lock()
	globalModelsConfig = modelsConfig{}
	configMutex.Unlock()

	if !rolePrependsBeforeOriginal("architect") {
		t.Error("default: architect should prepend before originalModel")
	}
	if !rolePrependsBeforeOriginal("planner") {
		t.Error("default: planner should prepend before originalModel")
	}
	if !rolePrependsBeforeOriginal("qa") {
		t.Error("default: qa should prepend before originalModel")
	}
	if rolePrependsBeforeOriginal("polecat") {
		t.Error("default: polecat should keep originalModel first")
	}

	configMutex.Lock()
	globalModelsConfig = modelsConfig{RolePrependBeforeOriginal: []string{"polecat"}}
	configMutex.Unlock()

	if !rolePrependsBeforeOriginal("polecat") {
		t.Error("config override: polecat should prepend before originalModel")
	}
	if rolePrependsBeforeOriginal("architect") {
		t.Error("config override: architect should not prepend when list excludes it")
	}
}

func indexOfCandidate(slice []string, s string) int {
	for i, v := range slice {
		if v == s {
			return i
		}
	}
	return -1
}

func TestAppendLocalOpenAICandidates_ExcludesQARole(t *testing.T) {
	prevCfg := globalModelsConfig
	t.Cleanup(func() {
		configMutex.Lock()
		globalModelsConfig = prevCfg
		configMutex.Unlock()
	})
	conf := modelsConfig{
		LocalOpenAI: []localOpenAIModel{{
			ID:       "local/qwen3-coder-30b",
			Endpoint: "http://127.0.0.1:8090",
			Model:    "m",
		}},
		RoleLocalExclude: []string{"qa"},
	}
	configMutex.Lock()
	globalModelsConfig = conf
	configMutex.Unlock()

	mkCtx := func(role string) candidateContext {
		return candidateContext{
			role:             role,
			conf:             conf,
			allowLocalOpenAI: true,
			isCooldown:       func(string) bool { return false },
			isExcluded:       func(string) bool { return false },
		}
	}

	base := []string{"nvidia/test-70b"}
	qaCands := appendLocalOpenAICandidates(base, mkCtx("qa"))
	for _, c := range qaCands {
		if c == "local/qwen3-coder-30b" {
			t.Fatalf("QA candidates must not include local model, got %v", qaCands)
		}
	}

	polecatCands := appendLocalOpenAICandidates(base, mkCtx("polecat"))
	found := false
	for _, c := range polecatCands {
		if c == "local/qwen3-coder-30b" {
			found = true
		}
	}
	if !found {
		t.Fatalf("polecat candidates should include local model, got %v", polecatCands)
	}
}

func TestRoleExcludedFromLocal_DefaultsEmpty(t *testing.T) {
	prevCfg := globalModelsConfig
	t.Cleanup(func() {
		configMutex.Lock()
		globalModelsConfig = prevCfg
		configMutex.Unlock()
	})
	configMutex.Lock()
	globalModelsConfig = modelsConfig{}
	configMutex.Unlock()
	if roleExcludedFromLocal("qa") {
		t.Fatal("expected qa not excluded when roleLocalExclude unset")
	}
	if roleExcludedFromLocal("polecat") {
		t.Fatal("expected polecat not excluded when roleLocalExclude unset")
	}
}

func TestModelClassificationMarkers(t *testing.T) {
	if !matchesAnyMarker("meta/llama-3.2-70b-instruct", []string{"llama-3.2+70b"}) {
		t.Error("compound marker llama-3.2+70b should match llama-3.2-70b model")
	}
	if matchesAnyMarker("meta/llama-3.2-11b-instruct", []string{"llama-3.2+70b"}) {
		t.Error("compound marker llama-3.2+70b must NOT match llama-3.2-11b")
	}
	if !matchesAnyMarker("nvidia/nemotron-3-super", []string{"nemotron"}) {
		t.Error("simple marker nemotron should match")
	}
	if matchesAnyMarker("deepseek/deepseek-v4-flash", []string{}) {
		t.Error("empty marker list should match nothing")
	}
	if matchesAnySubstring("google/gemini-3.5-flash", nil) {
		t.Error("nil substring list should match nothing")
	}
	if !matchesAnySubstring("cerebras/gpt-oss-120b", []string{"oss"}) {
		t.Error("substring oss should match gpt-oss-120b")
	}
}

func TestModelClassificationConfigEmptySafe(t *testing.T) {
	prevCfg := globalModelsConfig
	t.Cleanup(func() {
		configMutex.Lock()
		globalModelsConfig = prevCfg
		configMutex.Unlock()
	})
	configMutex.Lock()
	globalModelsConfig = modelsConfig{}
	configMutex.Unlock()

	if configNvidiaChatPrefixes() != nil {
		t.Error("empty config should yield nil chat prefixes (match nothing)")
	}
	if configToolSupportMarkers() != nil {
		t.Error("empty config should yield nil tool-support markers")
	}
	if configComplexModelHints() != nil {
		t.Error("empty config should yield nil complex hints")
	}
	if configOpenRouterExcluded() != nil {
		t.Error("empty config should yield nil OpenRouter exclusions")
	}
	if configCerebrasBudgetMarkers() != nil {
		t.Error("empty config should yield nil Cerebras budget markers")
	}
	if configWeakModelMarkers() != nil {
		t.Error("empty config should yield nil weak-model markers")
	}
	if matchesAnySubstring("anything", configComplexModelHints()) {
		t.Error("nil hints should match nothing")
	}
}
