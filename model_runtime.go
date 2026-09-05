package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io/ioutil"
	"log"
	"net/http"
	"os"
	"os/signal"
	"regexp"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"

	"gopkg.in/yaml.v3"
)

type openRouterModel struct {
	ID            string `json:"id"`
	Name          string `json:"name"`
	ContextLength int    `json:"context_length"`
	Pricing       struct {
		Prompt     string `json:"prompt"`
		Completion string `json:"completion"`
	} `json:"pricing"`
	SupportedParameters []string `json:"supported_parameters"`
	Created             int64    `json:"created"`
}

type ideModel struct {
	ID       string `yaml:"id"`
	Cooldown string `yaml:"cooldown"`
	Endpoint string `yaml:"endpoint"`
}

type scoreBoost struct {
	Pattern string  `yaml:"pattern"`
	Boost   float64 `yaml:"boost"`
}

type compatModel struct {
	ID          string `yaml:"id"`
	DisplayName string `yaml:"displayName"`
	OwnedBy     string `yaml:"ownedBy"`
	Created     int64  `yaml:"created"`
}

// localOpenAIModel is an OpenAI-compatible HTTP server (e.g. llama.cpp llama-server)
// reached directly, without an API key unless apiKeyEnv is set.
type localOpenAIModel struct {
	ID           string `yaml:"id"`
	Endpoint     string `yaml:"endpoint"`               // base URL, e.g. http://127.0.0.1:8090
	Model        string `yaml:"model"`                  // exact JSON "model" llama-server expects (see GET /v1/models)
	ContextSlots int    `yaml:"contextSlots,omitempty"` // llama-server -c; caps max_tokens so prompt+gen fits
	Cooldown     string `yaml:"cooldown,omitempty"`
	APIKeyEnv    string `yaml:"apiKeyEnv,omitempty"`    // optional: env var for Bearer token; if set and empty, no Authorization header
	PromptSuffix string `yaml:"promptSuffix,omitempty"` // appended to last user message (e.g. "/no_think")
	// ExtraBody keys are deep-merged into the outbound JSON whenever a request
	// routes to this endpoint — config-driven per-engine requirements such as
	// chat_template_kwargs (e.g. {"enable_thinking": false} for Qwen3 reasoning
	// models served by engines whose templates ignore /no_think suffixes).
	ExtraBody map[string]interface{} `yaml:"extraBody,omitempty"`
	// Priority promotes this endpoint to the FRONT of the candidate chain for
	// roles that may use it (opt-in). Default false = end-of-chain fallback.
	Priority bool `yaml:"priority,omitempty"`
}

// blockSmallCloudWhenLocalGPUConfig lists cloud model ids/patterns to skip when localOpenAI
// is configured and freeride runs with --allow-local-openai (local GPU mode).
type blockSmallCloudWhenLocalGPUConfig struct {
	Models   []string `yaml:"models"`
	Patterns []string `yaml:"patterns"`
}

// modelClassificationConfig holds the model-name heuristics that were previously
// hardcoded in Go. All lists are substrings matched against lowercase model ids,
// so new/changed model names can be configured without a code change. Empty lists
// simply match nothing.
type modelClassificationConfig struct {
	NvidiaChatPrefixes    []string `yaml:"nvidiaChatPrefixes"`
	NvidiaChatExcluded    []string `yaml:"nvidiaChatExcluded"`
	NvidiaChatMarkers     []string `yaml:"nvidiaChatMarkers"`
	ToolSupportMarkers    []string `yaml:"toolSupportMarkers"`
	ComplexModelHints     []string `yaml:"complexModelHints"`
	OpenRouterExcluded    []string `yaml:"openRouterExcluded"`
	CerebrasBudgetMarkers []string `yaml:"cerebrasBudgetMarkers"`
	WeakModelMarkers      []string `yaml:"weakModelMarkers"`
}

type modelsConfig struct {
	CerebrasBudget              []string                          `yaml:"cerebrasBudget"`
	CerebrasPerformance         []string                          `yaml:"cerebrasPerformance"`
	GroqBudget                  []string                          `yaml:"groqBudget"`
	GroqPerformance             []string                          `yaml:"groqPerformance"`
	GeminiModels                []geminiModel                     `yaml:"geminiModels"`
	ReliableFree                []string                          `yaml:"reliableFree"`
	NvidiaReliable              []string                          `yaml:"nvidiaReliable"`
	NvidiaComplex               []string                          `yaml:"nvidiaComplex"`
	CuratedPaid                 []string                          `yaml:"curatedPaid"`
	ExcludeModels               []string                          `yaml:"excludeModels"`
	BlockSmallCloudWhenLocalGPU blockSmallCloudWhenLocalGPUConfig `yaml:"blockSmallCloudWhenLocalGPU"`
	IdeModels                   []ideModel                        `yaml:"ideModels"`
	LocalOpenAI                 []localOpenAIModel                `yaml:"localOpenAI"`
	RolePrepend                 map[string][]string               `yaml:"rolePrepend"`
	RolePrependBeforeOriginal   []string                          `yaml:"rolePrependBeforeOriginal"`
	RoleLocalFirst              map[string][]string               `yaml:"roleLocalFirst"`
	RoleLocalOnly               []string                          `yaml:"roleLocalOnly"`
	RoleLocalExclude            []string                          `yaml:"roleLocalExclude"`
	MassiveOnlyRoles            []string                          `yaml:"massiveOnlyRoles"`
	MassiveModelPatterns        []string                          `yaml:"massiveModelPatterns"`
	FreeModelScoreBoost         []scoreBoost                      `yaml:"freeModelScoreBoost"`
	TrustedScoringNames         []string                          `yaml:"trustedScoringNames"`
	CompatModels                []compatModel                     `yaml:"compatModels"`
	DefaultResponseModel        string                            `yaml:"defaultResponseModel"`
	AnthropicResponseModel      string                            `yaml:"anthropicResponseModel"`
	ModelClassification         modelClassificationConfig         `yaml:"modelClassification"`
	CloudflareModels            []cloudflareModel                 `yaml:"cloudflareModels"`
}

var (
	globalModelsConfig modelsConfig
	configMutex        sync.RWMutex
)

func loadModelsConfig() {
	configMutex.Lock()
	defer configMutex.Unlock()

	data, err := ioutil.ReadFile("models.yaml")
	if err != nil {
		log.Printf("[WARN] Failed to read models.yaml: %v. Proxy will not route until config exists.", err)
		globalModelsConfig = modelsConfig{}
		return
	}

	if err := yaml.Unmarshal(data, &globalModelsConfig); err != nil {
		log.Printf("[ERROR] Failed to parse models.yaml: %v", err)
		return
	}
	log.Printf("[INFO] Loaded %d Cerebras Budget, %d Cerebras Performance, %d Gemini direct, %d reliable free, %d NVIDIA, %d curated paid, %d IDE models, %d local OpenAI endpoints, %d role prepends, %d role local-first, %d local-excluded roles, %d local-GPU block ids, %d local-GPU block patterns, %d chat prefixes, %d tool-support markers, %d complex hints from config",
		len(globalModelsConfig.CerebrasBudget), len(globalModelsConfig.CerebrasPerformance), len(globalModelsConfig.GeminiModels), len(globalModelsConfig.ReliableFree), len(globalModelsConfig.NvidiaReliable), len(globalModelsConfig.CuratedPaid), len(globalModelsConfig.IdeModels), len(globalModelsConfig.LocalOpenAI),
		len(globalModelsConfig.RolePrepend), len(globalModelsConfig.RoleLocalFirst), len(globalModelsConfig.RoleLocalExclude), len(globalModelsConfig.BlockSmallCloudWhenLocalGPU.Models), len(globalModelsConfig.BlockSmallCloudWhenLocalGPU.Patterns),
		len(globalModelsConfig.ModelClassification.NvidiaChatPrefixes), len(globalModelsConfig.ModelClassification.ToolSupportMarkers), len(globalModelsConfig.ModelClassification.ComplexModelHints))
	if len(globalModelsConfig.GeminiModels) > 0 {
		if resolveGeminiAPIKey() == "" {
			log.Printf("[WARN] geminiModels configured but GEMINI_API_KEY (or GOOGLE_API_KEY) is unset — Gemini direct routes will be skipped")
		} else {
			log.Printf("[INFO] Gemini API direct routing enabled (%d model(s))", len(globalModelsConfig.GeminiModels))
		}
	}
	// Do not call localGPUEnabled() here — it would RLock while we hold Lock (deadlock).
	if allowLocalOpenAI && len(globalModelsConfig.LocalOpenAI) > 0 {
		log.Printf("[INFO] Local GPU mode: %d localOpenAI fallback endpoint(s); small-cloud block list active", len(globalModelsConfig.LocalOpenAI))
	}
}

// modelsConfigReloadPoll is how often the background watcher checks models.yaml
// for changes. Polling avoids pulling in a filesystem-notify dependency.
const modelsConfigReloadPoll = 2 * time.Second

// startModelsConfigWatcher watches models.yaml and hot-reloads it into
// globalModelsConfig whenever the file changes. A SIGHUP also triggers an
// immediate reload. Only config that loads cleanly is applied — a broken
// models.yaml leaves the previously-good config in place (loadModelsConfig does
// not reset globalModelsConfig on a parse failure).
func startModelsConfigWatcher() {
	path := "models.yaml"
	var lastMod time.Time
	if fi, err := os.Stat(path); err == nil {
		lastMod = fi.ModTime()
	}

	reload := func(source string) {
		before := configRevision()
		loadModelsConfig()
		after := configRevision()
		if before != after {
			log.Printf("[INFO] models.yaml hot-reloaded (%s): config changed", source)
		}
	}

	// SIGHUP -> immediate reload (handy in scripts / service managers).
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGHUP)
	go func() {
		for range sig {
			log.Printf("[INFO] Received SIGHUP; reloading models.yaml")
			reload("SIGHUP")
		}
	}()

	go func() {
		for {
			time.Sleep(modelsConfigReloadPoll)
			fi, err := os.Stat(path)
			if err != nil {
				continue
			}
			if fi.ModTime().Equal(lastMod) {
				continue
			}
			lastMod = fi.ModTime()
			log.Printf("[INFO] models.yaml changed; hot-reloading")
			reload("change")
		}
	}()
}

// configRevision returns a cheap fingerprint of the loaded config so the
// watcher can tell whether a reload actually changed anything.
func configRevision() string {
	configMutex.RLock()
	defer configMutex.RUnlock()
	return fmt.Sprintf("%d:%d:%d:%d:%d",
		len(globalModelsConfig.RolePrependBeforeOriginal),
		len(globalModelsConfig.RolePrepend),
		len(globalModelsConfig.GeminiModels),
		len(globalModelsConfig.ReliableFree),
		len(globalModelsConfig.CuratedPaid))
}

func localGPUEnabled() bool {
	if !allowLocalOpenAI {
		return false
	}
	configMutex.RLock()
	defer configMutex.RUnlock()
	return len(globalModelsConfig.LocalOpenAI) > 0
}

func configDefaultResponseModel() string {
	configMutex.RLock()
	defer configMutex.RUnlock()
	return globalModelsConfig.DefaultResponseModel
}

func configAnthropicResponseModel() string {
	configMutex.RLock()
	defer configMutex.RUnlock()
	return globalModelsConfig.AnthropicResponseModel
}

func configModelClassification() modelClassificationConfig {
	configMutex.RLock()
	defer configMutex.RUnlock()
	return globalModelsConfig.ModelClassification
}

func configNvidiaChatPrefixes() []string {
	return configModelClassification().NvidiaChatPrefixes
}

func configNvidiaChatExcluded() []string {
	return configModelClassification().NvidiaChatExcluded
}

func configNvidiaChatMarkers() []string {
	return configModelClassification().NvidiaChatMarkers
}

func configToolSupportMarkers() []string {
	return configModelClassification().ToolSupportMarkers
}

func configComplexModelHints() []string {
	return configModelClassification().ComplexModelHints
}

func configOpenRouterExcluded() []string {
	return configModelClassification().OpenRouterExcluded
}

func configCerebrasBudgetMarkers() []string {
	return configModelClassification().CerebrasBudgetMarkers
}

func configWeakModelMarkers() []string {
	return configModelClassification().WeakModelMarkers
}

// matchesAnySubstring reports whether lower contains any of the given substrings.
func matchesAnySubstring(lower string, subs []string) bool {
	for _, s := range subs {
		if s != "" && strings.Contains(lower, s) {
			return true
		}
	}
	return false
}

// matchesMarker reports whether lower satisfies a single config marker. A marker
// may be a plain substring or a "+"-joined list of substrings that must ALL be
// present (e.g. "llama-3.2+70b" means both "llama-3.2" and "70b" must match).
func matchesMarker(lower string, marker string) bool {
	if marker == "" {
		return false
	}
	parts := strings.Split(marker, "+")
	for _, p := range parts {
		if !strings.Contains(lower, p) {
			return false
		}
	}
	return true
}

// matchesAnyMarker reports whether lower satisfies any marker in the config list.
func matchesAnyMarker(lower string, markers []string) bool {
	for _, m := range markers {
		if matchesMarker(lower, m) {
			return true
		}
	}
	return false
}

// isNvidiaNIMModel reports whether a model ID should be routed to the NVIDIA
// NIM API (integrate.api.nvidia.com). Models with a :free suffix are always
// routed via OpenRouter instead, even if they carry an nvidia/ prefix.
func isNvidiaNIMModel(model string) bool {
	if strings.HasSuffix(model, ":free") {
		return false
	}
	return strings.HasPrefix(model, "nvidia/") ||
		strings.HasPrefix(model, "meta/") ||
		strings.HasPrefix(model, "mistralai/") ||
		strings.HasPrefix(model, "microsoft/") ||
		strings.HasPrefix(model, "qwen/") ||
		strings.HasPrefix(model, "abacusai/") ||
		strings.HasPrefix(model, "ai21labs/") ||
		strings.HasPrefix(model, "01-ai/")
}

func roleRequiresMassiveModel(role string) bool {
	configMutex.RLock()
	roles := globalModelsConfig.MassiveOnlyRoles
	configMutex.RUnlock()
	if len(roles) == 0 {
		return role == "architect" || role == "mayor" || role == "planner" || role == "polecat"
	}
	for _, r := range roles {
		if r == role {
			return true
		}
	}
	return false
}

// rolePrependsBeforeOriginal reports whether a role's rolePrepend models are
// tried before its originalModel (only when --allow-paid). Configurable via
// models.yaml rolePrependBeforeOriginal; defaults to architect/planner/qa so
// polecat keeps its tuned model (e.g. deepseek-v4-flash) first.
func rolePrependsBeforeOriginal(role string) bool {
	configMutex.RLock()
	roles := globalModelsConfig.RolePrependBeforeOriginal
	configMutex.RUnlock()
	if len(roles) == 0 {
		return role == "architect" || role == "planner" || role == "qa"
	}
	for _, r := range roles {
		if r == role {
			return true
		}
	}
	return false
}

// selectCandidates promotes localOpenAI entries marked priority:true to the
// front of the otherwise cloud-first candidate chain. Config-driven opt-in:
// benchmarked engines can serve as primary while every other entry keeps its
// fallback order.
func selectCandidates(ctx candidateContext) []string {
	candidates := selectCandidatesBase(ctx)
	prio := map[string]bool{}
	for _, m := range ctx.conf.LocalOpenAI {
		if m.Priority && !ctx.isCooldown(m.ID) && !ctx.isExcluded(m.ID) && !roleExcludedFromLocal(ctx.role) {
			prio[m.ID] = true
		}
	}
	if len(prio) == 0 {
		return candidates
	}
	var front, rest []string
	for _, c := range candidates {
		if prio[c] {
			front = append(front, c)
			delete(prio, c)
		} else {
			rest = append(rest, c)
		}
	}
	return append(front, rest...)
}

// mergeExtraBody deep-merges configured extra body keys into the outbound
// request. Nested maps merge key-by-key so a client-supplied value under the
// same top-level key (e.g. chat_template_kwargs) isn't clobbered wholesale;
// scalars and new keys override/append.
func mergeExtraBody(dst map[string]interface{}, extra map[string]interface{}) {
	for k, v := range extra {
		if vm, ok := v.(map[string]interface{}); ok {
			if dm, ok := dst[k].(map[string]interface{}); ok {
				mergeExtraBody(dm, vm)
				continue
			}
			cp := make(map[string]interface{}, len(vm))
			for kk, vv := range vm {
				cp[kk] = vv
			}
			dst[k] = cp
			continue
		}
		dst[k] = v
	}
}

func isLocalOpenAIModelID(id string) bool {
	configMutex.RLock()
	defer configMutex.RUnlock()
	for _, m := range globalModelsConfig.LocalOpenAI {
		if m.ID == id {
			return true
		}
	}
	return false
}

func localContextSlots(configured int) int {
	if configured > 0 {
		return configured
	}
	return 16384
}

// selectPaidCandidates attempts to select paid model candidates when all free models
// are in cooldown and --allow-paid is set. It prioritizes NVIDIA tool-capable models,
// then falls back to other configured paid models.
func selectPaidCandidates(ctx candidateContext, originalModel string) []string {
	var candidates []string

	// Tier 1: NVIDIA tool-capable models (always free, don't require credits)
	for _, nid := range ctx.conf.NvidiaReliable {
		if nid == "" || ctx.isCooldown(nid) || ctx.isExcluded(nid) {
			continue
		}
		if !candidateListContains(candidates, nid) {
			candidates = append(candidates, nid)
			log.Printf("[DEBUG] Paid fallback: using NVIDIA model: %s", nid)
			break
		}
	}

	// Tier 2: Curated paid models from config
	if len(candidates) == 0 && len(ctx.conf.CuratedPaid) > 0 {
		for _, pm := range ctx.conf.CuratedPaid {
			if pm == "" || ctx.isCooldown(pm) || ctx.isExcluded(pm) {
				continue
			}
			if !candidateListContains(candidates, pm) {
				candidates = append(candidates, pm)
				log.Printf("[DEBUG] Paid fallback: using curated paid model: %s", pm)
				break
			}
		}
	}

	// Tier 3: Cloudflare Workers AI models (if configured and enabled)
	if len(candidates) == 0 && cloudflareDirectAvailable() {
		for _, cfm := range ctx.conf.CloudflareModels {
			if cfm.ID == "" || ctx.isCooldown(cfm.ID) || ctx.isExcluded(cfm.ID) {
				continue
			}
			if !candidateListContains(candidates, cfm.ID) {
				candidates = append(candidates, cfm.ID)
				log.Printf("[DEBUG] Paid fallback: using Cloudflare model: %s", cfm.ID)
				break
			}
		}
	}

	// Tier 4: Gemini API direct models (if configured)
	if len(candidates) == 0 && geminiDirectAvailable() {
		for _, gm := range ctx.conf.GeminiModels {
			if gm.ID == "" {
				continue
			}
			if !ctx.isCooldown(gm.ID) && !ctx.isExcluded(gm.ID) {
				if !candidateListContains(candidates, gm.ID) {
					candidates = append(candidates, gm.ID)
					log.Printf("[DEBUG] Paid fallback: using Gemini direct model: %s", gm.ID)
					break
				}
			}
		}
	}

	return candidates
}

// estimatePromptTokens is a conservative upper bound for context budgeting.
// Qwen3-Coder tokenizes code-dense prompts at ~0.9-1.1 chars/token (observed
// 26,969 tokens from a 29,572-char prompt), so we budget 1 char/token — a safe
// bound that never underestimates.
func estimatePromptTokens(body map[string]interface{}) int {
	msgs, ok := body["messages"].([]interface{})
	if !ok {
		return 0
	}
	chars := 0
	for _, m := range msgs {
		mMap, ok := m.(map[string]interface{})
		if !ok {
			continue
		}
		switch c := mMap["content"].(type) {
		case string:
			chars += len(c)
		case []interface{}:
			for _, part := range c {
				if pMap, ok := part.(map[string]interface{}); ok {
					if t, ok := pMap["text"].(string); ok {
						chars += len(t)
					}
				}
			}
		}
	}
	if chars == 0 {
		return 0
	}
	return chars
}

// capLocalRequestContext lowers max_tokens so prompt + generation fits llama-server -c.
// Returns false when the prompt alone (at the conservative 1 char/token bound) already
// exceeds the context, so the caller should skip local and use a cloud model instead.
func capLocalRequestContext(body map[string]interface{}, contextSlots int) bool {
	ctx := localContextSlots(contextSlots)
	promptEst := estimatePromptTokens(body)
	const slack = 384
	room := ctx - promptEst - slack
	if room < 256 {
		log.Printf("[LOCAL] skip local: prompt est ~%d exceeds ctx=%d (no room for max_tokens)", promptEst, ctx)
		return false
	}
	// Polecat replies are usually short CMD/EDIT blocks; avoid reserving 4096 on tight ctx.
	if room > 1024 {
		room = 1024
	}
	var requested float64 = 4096
	if mt, ok := body["max_tokens"].(float64); ok {
		requested = mt
	}
	capped := float64(room)
	if requested < capped {
		capped = requested
	}
	if capped != requested {
		log.Printf("[LOCAL] cap max_tokens %.0f → %.0f (est prompt ~%d, ctx=%d)", requested, capped, promptEst, ctx)
	}
	body["max_tokens"] = capped
	return true
}

func localGPUBlockPatternMatches(lowerModel, pattern string) bool {
	pat := strings.ToLower(strings.TrimSpace(pattern))
	if pat == "" {
		return false
	}
	if !strings.Contains(lowerModel, pat) {
		return false
	}
	// Short patterns like "mini" must not match inside unrelated tokens (e.g. "gemini" → "ini-" looks like "mini-").
	if len(pat) <= 4 && !strings.Contains(pat, "-") && !strings.Contains(pat, "/") {
		const seps = "-/_.:"
		start := 0
		for {
			idx := strings.Index(lowerModel[start:], pat)
			if idx < 0 {
				return false
			}
			idx += start
			beforeOK := idx == 0 || strings.ContainsRune(seps, rune(lowerModel[idx-1]))
			end := idx + len(pat)
			afterOK := end >= len(lowerModel) || strings.ContainsRune(seps, rune(lowerModel[end]))
			if beforeOK && afterOK {
				return true
			}
			start = idx + 1
		}
	}
	return true
}

func isBlockedSmallCloudWhenLocalGPU(model string) bool {
	if !localGPUEnabled() {
		return false
	}
	if isGeminiDirectModelID(model) {
		return false
	}
	configMutex.RLock()
	defer configMutex.RUnlock()
	lowerModel := strings.ToLower(model)
	for _, m := range globalModelsConfig.BlockSmallCloudWhenLocalGPU.Models {
		if strings.ToLower(m) == lowerModel {
			return true
		}
	}
	for _, pat := range globalModelsConfig.BlockSmallCloudWhenLocalGPU.Patterns {
		if localGPUBlockPatternMatches(lowerModel, pat) {
			return true
		}
	}
	return false
}

func isExcluded(model string) bool {
	configMutex.RLock()
	defer configMutex.RUnlock()
	lowerModel := strings.ToLower(model)
	for _, m := range globalModelsConfig.ExcludeModels {
		if strings.ToLower(m) == lowerModel {
			return true
		}
	}
	return false
}

func isCandidateExcluded(model string) bool {
	return isExcluded(model) || isBlockedSmallCloudWhenLocalGPU(model)
}

type nvidiaModel struct {
	ID         string      `json:"id"`
	Object     string      `json:"object"`
	Created    int         `json:"created"`
	OwnedBy    string      `json:"owned_by"`
	Permission interface{} `json:"permission"`
	// Track tool/support capability
	SupportsTools bool `json:"-"`
}

type cerebrasModel struct {
	ID      string `json:"id"`
	Object  string `json:"object"`
	Created int64  `json:"created"`
	OwnedBy string `json:"owned_by"`
}

type groqModel struct {
	ID      string `json:"id"`
	Object  string `json:"object"`
	Created int64  `json:"created"`
	OwnedBy string `json:"owned_by"`
}

type ollamaModelDetails struct {
	Format            string   `json:"format"`
	Family            string   `json:"family"`
	Families          []string `json:"families"`
	ParameterSize     string   `json:"parameter_size"`
	QuantizationLevel string   `json:"quantization_level"`
}

type ollamaModel struct {
	Name       string             `json:"name"`
	Model      string             `json:"model"`
	ModifiedAt string             `json:"modified_at"`
	Size       int64              `json:"size"`
	Digest     string             `json:"digest"`
	Details    ollamaModelDetails `json:"details"`
}

type ollamaTagsResponse struct {
	Models []ollamaModel `json:"models"`
}

var (
	cachedFreeModels     []openRouterModel
	cachedNvidiaModels   []nvidiaModel
	cachedCerebrasModels []cerebrasModel
	cachedOllamaModels   []ollamaModel
	cachedGroqModels     []groqModel
	cacheMutex           sync.RWMutex
	cacheTime            time.Time
	cacheTTL             = 1 * time.Hour

	cooldowns  = make(map[string]*cooldownEntry)
	cooldownMu sync.RWMutex

	debugMode        bool
	traceMode        bool
	allowPaid        bool
	allowIDE         bool
	allowLocalOpenAI bool
	toolRegex        = regexp.MustCompile("(?s)<invoke name=\"([^\"]+)\">(.*?)</invoke>")
	paramRegex       = regexp.MustCompile("(?s)<parameter name=\"([^\"]+)\">(.*?)</parameter>")

	// Optional test overrides (non-nil only in tests) to avoid live API calls.
	fetchFreeModelsHook        func() ([]openRouterModel, error)
	fetchNvidiaFreeModelsHook  func() ([]nvidiaModel, error)
	fetchCerebrasModelsHook    func() ([]cerebrasModel, error)
	fetchGroqModelsHook        func() ([]groqModel, error)
	fetchOllamaCloudModelsHook func() ([]ollamaModel, error)
)

type candidateContext struct {
	originalModel    string
	role             string
	conf             modelsConfig
	models           []openRouterModel
	nvidiaModels     []nvidiaModel
	cerebrasModels   []cerebrasModel
	groqModels       []groqModel
	ollamaModels     []ollamaModel
	allowPaid        bool
	allowIDE         bool
	allowLocalOpenAI bool
	isCooldown       func(string) bool
	isExcluded       func(string) bool
	isComplexRequest bool
}

type cooldownEntry struct {
	ErrorCount  int       `json:"error_count"`
	CooldownEnd time.Time `json:"cooldown_end"`
}

const cooldownsFile = "cooldowns.json"

func saveCooldowns() {
	active := make(map[string]*cooldownEntry)
	now := time.Now()
	for k, v := range cooldowns {
		if v.ErrorCount > 0 && now.Before(v.CooldownEnd) {
			active[k] = v
		}
	}
	data, err := json.MarshalIndent(active, "", "  ")
	if err == nil {
		ioutil.WriteFile(cooldownsFile, data, 0644)
	}
}

func loadCooldowns() {
	cooldownMu.Lock()
	defer cooldownMu.Unlock()
	data, err := ioutil.ReadFile(cooldownsFile)
	if err != nil {
		return
	}
	var loaded map[string]*cooldownEntry
	if err := json.Unmarshal(data, &loaded); err == nil {
		now := time.Now()
		for k, v := range loaded {
			if v.ErrorCount > 0 && now.Before(v.CooldownEnd) {
				cooldowns[k] = v
			}
		}
	}
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func calculateModelCooldown(model string, errorCount int) time.Duration {
	configMutex.RLock()
	conf := globalModelsConfig
	configMutex.RUnlock()

	// Check if this is an IDE model with a custom cooldown
	for _, m := range conf.IdeModels {
		if m.ID == model && m.Cooldown != "" {
			if d, err := time.ParseDuration(m.Cooldown); err == nil {
				return d
			}
		}
	}
	for _, m := range conf.LocalOpenAI {
		if m.ID == model && m.Cooldown != "" {
			if d, err := time.ParseDuration(m.Cooldown); err == nil {
				return d
			}
		}
	}
	for _, m := range conf.GeminiModels {
		if m.ID == model && m.Cooldown != "" {
			if d, err := time.ParseDuration(m.Cooldown); err == nil {
				return d
			}
		}
	}

	// Standard cooldown logic for other models
	n := max(1, errorCount)
	if n == 1 {
		return 10 * time.Second
	} else if n == 2 {
		return 30 * time.Second
	}
	return 60 * time.Second // cap at 1 minute
}

// ... fetchFreeModels, scoreModel, handleTags, handleVersion, handleOllamaChat existing ...

func fetchFreeModels() ([]openRouterModel, error) {
	if fetchFreeModelsHook != nil {
		return fetchFreeModelsHook()
	}
	log.Printf("[DEBUG] fetchFreeModels called")
	cacheMutex.RLock()
	if time.Since(cacheTime) < cacheTTL && len(cachedFreeModels) > 0 {
		models := cachedFreeModels
		cacheMutex.RUnlock()
		return models, nil
	}
	cacheMutex.RUnlock()

	req, err := http.NewRequestWithContext(context.Background(), "GET", "https://openrouter.ai/api/v1/models", nil)
	if err != nil {
		return nil, err
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	log.Printf("[DEBUG] OpenRouter API status: %d", resp.StatusCode)
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("OpenRouter API returned status %d", resp.StatusCode)
	}

	var wrapper struct {
		Data []openRouterModel `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&wrapper); err != nil {
		return nil, err
	}

	var freeModels []openRouterModel
	for _, m := range wrapper.Data {
		isModelFree := m.Pricing.Prompt == "0" || m.Pricing.Prompt == "0.0" || m.Pricing.Prompt == "0.00"
		if isModelFree || allowPaid {
			lowerID := strings.ToLower(m.ID)
			if matchesAnySubstring(lowerID, configOpenRouterExcluded()) {
				continue
			}
			if isModelFree && debugMode {
				log.Printf("[DEBUG] OpenRouter Free Model: %s (Price: %s)", m.ID, m.Pricing.Prompt)
			}
			if isExcluded(m.ID) {
				continue
			}
			freeModels = append(freeModels, m)
		}
	}

	sort.Slice(freeModels, func(i, j int) bool {
		return scoreModel(freeModels[i]) > scoreModel(freeModels[j])
	})

	cacheMutex.Lock()
	cachedFreeModels = freeModels
	cacheTime = time.Now()
	cacheMutex.Unlock()

	log.Printf("[DEBUG] Fetched %d free OpenRouter models", len(freeModels))
	return freeModels, nil
}

func fetchNvidiaFreeModels() ([]nvidiaModel, error) {
	if fetchNvidiaFreeModelsHook != nil {
		return fetchNvidiaFreeModelsHook()
	}
	cacheMutex.RLock()
	if time.Since(cacheTime) < cacheTTL && len(cachedNvidiaModels) > 0 {
		models := cachedNvidiaModels
		cacheMutex.RUnlock()
		return models, nil
	}
	cacheMutex.RUnlock()

	apiKey := os.Getenv("NVIDIA_API_KEY")
	if apiKey == "" {
		log.Printf("[DEBUG] NVIDIA_API_KEY not set, skipping NVIDIA models")
		return nil, nil
	}

	req, err := http.NewRequestWithContext(context.Background(), "GET", "https://integrate.api.nvidia.com/v1/models", nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+apiKey)

	resp, err := http.DefaultClient.Do(req)

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("NVIDIA API returned status %d", resp.StatusCode)
	}

	var wrapper struct {
		Data []nvidiaModel `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&wrapper); err != nil {
		return nil, err
	}

	var freeModels []nvidiaModel
	for _, m := range wrapper.Data {
		if debugMode {
			log.Printf("[DEBUG] NVIDIA Model ID: %s", m.ID)
		}
		lowerID := strings.ToLower(m.ID)

		// Chat-capable vendor namespaces hosted on NVIDIA NIM (config-driven).
		chatPrefixes := configNvidiaChatPrefixes()
		validPrefix := false
		for _, p := range chatPrefixes {
			if strings.HasPrefix(m.ID, p) {
				validPrefix = true
				break
			}
		}

		// Only include chat/instruct models (not embeddings, translators, vision-only, safety, etc)
		isChatModel := validPrefix &&
			!matchesAnySubstring(lowerID, configNvidiaChatExcluded()) &&
			matchesAnySubstring(lowerID, configNvidiaChatMarkers())

		if !isChatModel {
			continue
		}

		// Mark models that support tools/function calling (config-driven markers).
		m.SupportsTools = matchesAnyMarker(lowerID, configToolSupportMarkers())

		freeModels = append(freeModels, m)
	}

	cacheMutex.Lock()
	cachedNvidiaModels = freeModels
	cacheMutex.Unlock()

	log.Printf("[DEBUG] Fetched %d free NVIDIA models (%d with tool support)", len(freeModels), func() int {
		count := 0
		for _, m := range freeModels {
			if m.SupportsTools {
				count++
			}
		}
		return count
	}())
	return freeModels, nil
}

func fetchCerebrasModels() ([]cerebrasModel, error) {
	if fetchCerebrasModelsHook != nil {
		return fetchCerebrasModelsHook()
	}
	cacheMutex.RLock()
	if time.Since(cacheTime) < cacheTTL && len(cachedCerebrasModels) > 0 {
		models := cachedCerebrasModels
		cacheMutex.RUnlock()
		return models, nil
	}
	cacheMutex.RUnlock()

	apiKey := os.Getenv("CEREBRAS_API_KEY")
	if apiKey == "" {
		log.Printf("[DEBUG] CEREBRAS_API_KEY not set, skipping Cerebras models")
		return nil, nil
	}

	req, err := http.NewRequestWithContext(context.Background(), "GET", "https://api.cerebras.ai/v1/models", nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+apiKey)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("Cerebras API returned status %d", resp.StatusCode)
	}

	var wrapper struct {
		Data []cerebrasModel `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&wrapper); err != nil {
		return nil, err
	}

	cacheMutex.Lock()
	cachedCerebrasModels = wrapper.Data
	cacheMutex.Unlock()

	log.Printf("[DEBUG] Fetched %d Cerebras models", len(wrapper.Data))
	return wrapper.Data, nil
}

func fetchGroqModels() ([]groqModel, error) {
	if fetchGroqModelsHook != nil {
		return fetchGroqModelsHook()
	}
	cacheMutex.RLock()
	if time.Since(cacheTime) < cacheTTL && len(cachedGroqModels) > 0 {
		models := cachedGroqModels
		cacheMutex.RUnlock()
		return models, nil
	}
	cacheMutex.RUnlock()

	apiKey := os.Getenv("GROQ_API_KEY")
	if apiKey == "" {
		log.Printf("[DEBUG] GROQ_API_KEY not set, skipping Groq models")
		return nil, nil
	}

	req, err := http.NewRequestWithContext(context.Background(), "GET", "https://api.groq.com/openai/v1/models", nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+apiKey)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("Groq API returned status %d", resp.StatusCode)
	}

	var wrapper struct {
		Data []groqModel `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&wrapper); err != nil {
		return nil, err
	}

	cacheMutex.Lock()
	cachedGroqModels = wrapper.Data
	cacheMutex.Unlock()

	log.Printf("[DEBUG] Fetched %d Groq models", len(wrapper.Data))
	return wrapper.Data, nil
}

func fetchOllamaCloudModels() ([]ollamaModel, error) {
	if fetchOllamaCloudModelsHook != nil {
		return fetchOllamaCloudModelsHook()
	}
	cacheMutex.RLock()
	if time.Since(cacheTime) < cacheTTL && len(cachedOllamaModels) > 0 {
		models := cachedOllamaModels
		cacheMutex.RUnlock()
		return models, nil
	}
	cacheMutex.RUnlock()

	apiKey := os.Getenv("OLLAMA_API_KEY")
	host := os.Getenv("OLLAMA_API_URL")
	myPort := os.Getenv("PORT")
	isLocalLoop := host == "" && (myPort == "" || myPort == "11434")

	// Never GET /api/tags on ourselves — handleTags already aggregates models.
	if isLocalLoop && apiKey == "" {
		log.Printf("[DEBUG] OLLAMA_API_KEY not set and proxy on :11434, skipping Ollama model fetch (loop guard)")
		return nil, nil
	}

	if host == "" {
		if isLocalLoop && apiKey != "" {
			host = "https://ollama.com"
		} else if !isLocalLoop {
			host = "http://localhost:11434"
		}
	}
	if host == "" {
		return nil, nil
	}
	log.Printf("[DEBUG] Fetching Ollama models from %s (key set: %v)", host, apiKey != "")
	url := strings.TrimSuffix(host, "/") + "/api/tags"
	req, err := http.NewRequestWithContext(context.Background(), "GET", url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+apiKey)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("Ollama API returned status %d", resp.StatusCode)
	}

	var wrapper ollamaTagsResponse
	if err := json.NewDecoder(resp.Body).Decode(&wrapper); err != nil {
		return nil, err
	}

	cacheMutex.Lock()
	cachedOllamaModels = wrapper.Models
	cacheMutex.Unlock()

	log.Printf("[DEBUG] Fetched %d Ollama Cloud models", len(wrapper.Models))
	for _, m := range wrapper.Models {
		log.Printf("[DEBUG] Ollama Cloud Model: %s", m.Name)
	}
	return wrapper.Models, nil
}

func scoreModel(m openRouterModel) float64 {
	score := 0.0

	ctxScore := float64(m.ContextLength) / 128000.0
	if ctxScore > 1.0 {
		ctxScore = 1.0
	}
	score += ctxScore * 0.4

	capabilityScore := 0.0
	for _, p := range m.SupportedParameters {
		if p == "tools" {
			capabilityScore += 0.5
		}
		if p == "response_format" {
			capabilityScore += 0.5
		}
	}
	if capabilityScore > 1.0 {
		capabilityScore = 1.0
	}
	score += capabilityScore * 0.3

	twoYearsAgo := time.Now().AddDate(-2, 0, 0).Unix()
	now := time.Now().Unix()
	if m.Created > twoYearsAgo {
		recencyScore := float64(m.Created-twoYearsAgo) / float64(now-twoYearsAgo)
		score += recencyScore * 0.2
	}

	configMutex.RLock()
	trustNames := globalModelsConfig.TrustedScoringNames
	boosts := globalModelsConfig.FreeModelScoreBoost
	configMutex.RUnlock()

	lowerID := strings.ToLower(m.ID)
	for _, name := range trustNames {
		if strings.Contains(lowerID, strings.ToLower(name)) {
			score += 0.1
			break
		}
	}
	for _, b := range boosts {
		if b.Pattern != "" && strings.Contains(lowerID, strings.ToLower(b.Pattern)) {
			score += b.Boost
		}
	}

	return score
}

func handleTags(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	models, err := fetchFreeModels()
	if err != nil {
		log.Printf("Error fetching free models: %v", err)
		http.Error(w, "Internal server error", http.StatusInternalServerError)
		return
	}

	nvidiaModels, _ := fetchNvidiaFreeModels()
	cerebrasModels, _ := fetchCerebrasModels()
	groqModels, _ := fetchGroqModels()
	ollamaModelsList, _ := fetchOllamaCloudModels()

	var ollamaModels []ollamaModel
	for _, m := range models {
		modelName := m.ID
		if !strings.Contains(modelName, ":") {
			modelName = modelName + ":free" // Add a mock tag
		}

		ollamaModels = append(ollamaModels, ollamaModel{
			Name:       modelName,
			Model:      modelName,
			ModifiedAt: time.Unix(m.Created, 0).Format(time.RFC3339),
			Size:       0,
			Digest:     "sha256:freeride",
			Details: ollamaModelDetails{
				Format:            "gguf",
				Family:            "openrouter",
				Families:          []string{"openrouter"},
				ParameterSize:     "unknown",
				QuantizationLevel: "none",
			},
		})
	}

	// Add NVIDIA models to discovery
	for _, m := range nvidiaModels {
		ollamaModels = append(ollamaModels, ollamaModel{
			Name:       m.ID,
			Model:      m.ID,
			ModifiedAt: time.Unix(int64(m.Created), 0).Format(time.RFC3339),
			Size:       0,
			Digest:     "sha256:nvidia",
			Details: ollamaModelDetails{
				Format:            "gguf",
				Family:            "nvidia",
				Families:          []string{"nvidia"},
				ParameterSize:     "unknown",
				QuantizationLevel: "none",
			},
		})
	}

	// Add Cerebras models to discovery
	for _, m := range cerebrasModels {
		ollamaModels = append(ollamaModels, ollamaModel{
			Name:       "cerebras/" + m.ID,
			Model:      "cerebras/" + m.ID,
			ModifiedAt: time.Unix(m.Created, 0).Format(time.RFC3339),
			Size:       0,
			Digest:     "sha256:cerebras",
			Details: ollamaModelDetails{
				Format:            "gguf",
				Family:            "cerebras",
				Families:          []string{"cerebras"},
				ParameterSize:     "unknown",
				QuantizationLevel: "none",
			},
		})
	}

	// Add Groq models to discovery
	for _, m := range groqModels {
		ollamaModels = append(ollamaModels, ollamaModel{
			Name:       "groq/" + m.ID,
			Model:      "groq/" + m.ID,
			ModifiedAt: time.Unix(m.Created, 0).Format(time.RFC3339),
			Size:       0,
			Digest:     "sha256:groq",
			Details: ollamaModelDetails{
				Format:            "gguf",
				Family:            "groq",
				Families:          []string{"groq"},
				ParameterSize:     "unknown",
				QuantizationLevel: "none",
			},
		})
	}

	// Add Ollama Cloud models to discovery
	for _, m := range ollamaModelsList {
		modelName := "ollama/" + m.Name
		ollamaModels = append(ollamaModels, ollamaModel{
			Name:       modelName,
			Model:      modelName,
			ModifiedAt: m.ModifiedAt,
			Size:       m.Size,
			Digest:     m.Digest,
			Details: ollamaModelDetails{
				Format:            m.Details.Format,
				Family:            "ollama-cloud",
				Families:          []string{"ollama-cloud"},
				ParameterSize:     m.Details.ParameterSize,
				QuantizationLevel: m.Details.QuantizationLevel,
			},
		})
	}

	// Add models from models.yaml to discovery if they start with ollama/
	configMutex.RLock()
	for _, m := range globalModelsConfig.ReliableFree {
		if strings.HasPrefix(m, "ollama/") {
			ollamaModels = append(ollamaModels, ollamaModel{
				Name:  m,
				Model: m,
				Details: ollamaModelDetails{
					Format:   "gguf",
					Family:   "ollama-cloud",
					Families: []string{"ollama-cloud"},
				},
			})
		}
	}
	for _, m := range globalModelsConfig.LocalOpenAI {
		if m.ID == "" {
			continue
		}
		ollamaModels = append(ollamaModels, ollamaModel{
			Name:  m.ID,
			Model: m.ID,
			Details: ollamaModelDetails{
				Format:   "gguf",
				Family:   "local-openai",
				Families: []string{"local-openai"},
			},
		})
	}
	if geminiDirectAvailable() {
		for _, m := range globalModelsConfig.GeminiModels {
			if m.ID == "" {
				continue
			}
			ollamaModels = append(ollamaModels, ollamaModel{
				Name:  m.ID,
				Model: m.ID,
				Details: ollamaModelDetails{
					Format:   "gguf",
					Family:   "gemini",
					Families: []string{"gemini"},
				},
			})
		}
	}
	configMutex.RUnlock()

	resp := ollamaTagsResponse{Models: ollamaModels}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}

func handleVersion(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Write([]byte(`{"version":"0.1.34"}`))
}

func markCooldown(model string) {
	cooldownMu.Lock()
	entry, ok := cooldowns[model]
	if !ok {
		entry = &cooldownEntry{}
		cooldowns[model] = entry
	}
	entry.ErrorCount++
	cd := calculateModelCooldown(model, entry.ErrorCount)
	entry.CooldownEnd = time.Now().Add(cd)
	saveCooldowns()
	cooldownMu.Unlock()
	log.Printf("Model %s put in cooldown for %v (ErrorCount: %d)", model, cd, entry.ErrorCount)
}

func markCustomCooldown(model string, cd time.Duration) {
	cooldownMu.Lock()
	entry, ok := cooldowns[model]
	if !ok {
		entry = &cooldownEntry{}
		cooldowns[model] = entry
	}
	entry.ErrorCount++
	entry.CooldownEnd = time.Now().Add(cd)
	saveCooldowns()
	cooldownMu.Unlock()
	log.Printf("Model %s put in CUSTOM cooldown for %v (ErrorCount: %d)", model, cd, entry.ErrorCount)
}

func markSuccess(model string) {
	cooldownMu.Lock()
	if entry, ok := cooldowns[model]; ok {
		entry.ErrorCount = 0
		entry.CooldownEnd = time.Time{}
		saveCooldowns()
	}
	cooldownMu.Unlock()
}

func isCooldown(model string) bool {
	cooldownMu.RLock()
	defer cooldownMu.RUnlock()
	if entry, ok := cooldowns[model]; ok {
		if time.Now().Before(entry.CooldownEnd) {
			return true
		}
	}
	return false
}
