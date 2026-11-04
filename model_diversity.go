package main

import (
	"strings"
	"sync"
)

// Requests currently identify the role, not the rig. Keep a conservative,
// process-wide history so reviews avoid models used by any of the paired roles.
// Only roles named in the diversity policy are tracked; history resets on restart.
var roleModelHistory = struct {
	sync.RWMutex
	models map[string]map[string]bool
}{models: make(map[string]map[string]bool)}

// modelIdentity treats the same model through different inference providers as
// shared. Known vendor namespaces are normalized; distinct versions stay distinct.
func modelIdentity(model string) string {
	model = strings.ToLower(strings.TrimSpace(model))
	for _, prefix := range []string{"groq/", "cerebras/", "ollama/"} {
		model = strings.TrimPrefix(model, prefix)
	}
	model = strings.TrimSuffix(model, ":free")
	for _, vendor := range []string{"openai/", "google/", "deepseek/", "qwen/", "mistralai/", "meta-llama/", "meta/", "nvidia/"} {
		model = strings.TrimPrefix(model, vendor)
	}
	return model
}

func recordRoleModel(role, model string, conf modelsConfig) {
	tracked := false
	for _, roles := range conf.RolePreferDifferentFrom {
		if candidateListContains(roles, role) {
			tracked = true
			break
		}
	}
	if !tracked || model == "" {
		return
	}
	roleModelHistory.Lock()
	defer roleModelHistory.Unlock()
	if roleModelHistory.models[role] == nil {
		roleModelHistory.models[role] = make(map[string]bool)
	}
	roleModelHistory.models[role][modelIdentity(model)] = true
}

// preferDifferentRoleModels runs AFTER all candidate construction and capability
// filtering, including emergency fallbacks. It only reorders eligible candidates:
// independent models first, shared models last. Explicit role-prepend choices
// lead the independent tier when paid-first routing is enabled.
func preferDifferentRoleModels(candidates []string, ctx candidateContext) ([]string, map[string]bool) {
	roles := ctx.conf.RolePreferDifferentFrom[ctx.role]
	if len(roles) == 0 {
		return candidates, nil
	}
	shared := make(map[string]bool)
	roleModelHistory.RLock()
	for _, role := range roles {
		for model := range roleModelHistory.models[role] {
			shared[model] = true
		}
		// Before any successful calls (including after a restart), avoid the
		// configured paid-first primary of the implementation roles as well.
		if ctx.allowPaid && rolePrependsBeforeOriginal(role) {
			if primary := ctx.conf.RolePrepend[role]; len(primary) > 0 {
				shared[modelIdentity(primary[0])] = true
			}
		}
	}
	roleModelHistory.RUnlock()
	independent := make([]string, 0, len(candidates))
	var fallback []string
	preferred := make(map[string]bool)
	if ctx.allowPaid && rolePrependsBeforeOriginal(ctx.role) {
		for _, model := range ctx.conf.RolePrepend[ctx.role] {
			if !shared[modelIdentity(model)] && candidateListContains(candidates, model) && !preferred[model] {
				independent = append(independent, model)
				preferred[model] = true
			}
		}
	}
	for _, model := range candidates {
		if shared[modelIdentity(model)] {
			fallback = append(fallback, model)
		} else if !preferred[model] {
			independent = append(independent, model)
		}
	}
	return append(independent, fallback...), shared
}
