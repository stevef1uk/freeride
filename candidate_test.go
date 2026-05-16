package main

import (
	"testing"
)

func TestSelectCandidates_CerebrasSensibleRouting(t *testing.T) {
	const (
		budgetSmall  = "cerebras/budget-small"
		budgetLarge  = "cerebras/budget-large"
		perf70       = "cerebras/perf-70b"
		nvidiaBig    = "test/nvidia-big-70b"
		cbPreviewNew = "cerebras/preview-new"
	)
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
	}

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
			name:             "Complex request prioritizes performance after budget",
			role:             "architect",
			isComplexRequest: true,
			expectedFirst:    budgetLarge,
			contains:         perf70,
			allowPaid:        true,
		},
		{
			name:             "Cooldown skips model",
			role:             "user",
			isComplexRequest: false,
			cooldowns:        map[string]bool{budgetSmall: true},
			expectedFirst:    budgetLarge,
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
	conf := modelsConfig{
		ReliableFree: []string{
			smallFree,
			big70Free,
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
			Patterns: []string{"nano"},
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
