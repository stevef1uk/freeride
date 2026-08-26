package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// Model doctor: verify every model referenced by models.yaml against each
// provider's live catalog. All major providers retire/add models constantly
// (OpenRouter, Groq, Cerebras, NVIDIA NIM), and a retired id sitting in a
// rolePrepend chain silently burns seconds per request on 404/429 fallbacks.
//
//	freeride -check-models                  # report only
//	freeride -check-models -probe-models    # + tiny chat ping per model
//	freeride -check-models -apply-models    # back up models.yaml, propose fixes, ask y/N
//	freeride -check-models -apply-models -yes
type modelCheck struct {
	id          string   // id exactly as written in models.yaml
	lookup      string   // id as the provider catalog serves it
	provider    string   // openrouter | groq | cerebras | nvidia | gemini | local
	sources     []string // yaml sections referencing it
	status      string   // OK | MISSING | DEAD | RATE-LIMITED | SKIP
	detail      string
	replacement string
}

var modelDoctorSkipPrefixes = []string{"ollama/"}

func classifyModelProvider(id string) (provider, lookup string) {
	switch {
	case strings.HasPrefix(id, "cerebras/"):
		return "cerebras", strings.TrimPrefix(id, "cerebras/")
	case strings.HasPrefix(id, "groq/"):
		return "groq", strings.TrimPrefix(id, "groq/")
	case strings.HasPrefix(id, "nvidia/"), strings.HasPrefix(id, "meta/"):
		return "nvidia", id
	default:
		if !strings.Contains(id, "/") {
			return "", "" // patterns like "671b" or "nano" — not model ids
		}
		return "openrouter", id
	}
}

// collectReferencedModels walks the typed config so new yaml sections are
// picked up automatically as fields are added to modelsConfig.
func collectReferencedModels() []modelCheck {
	configMutex.RLock()
	cfg := globalModelsConfig
	configMutex.RUnlock()

	var out []modelCheck
	seen := map[string]int{}
	add := func(section, id string) {
		id = strings.TrimSpace(id)
		if id == "" || strings.Contains(id, "://") {
			return
		}
		for _, p := range modelDoctorSkipPrefixes {
			if strings.HasPrefix(id, p) {
				return
			}
		}
		provider, lookup := classifyModelProvider(id)
		if provider == "" {
			return
		}
		if idx, ok := seen[id]; ok {
			out[idx].sources = append(out[idx].sources, section)
			return
		}
		seen[id] = len(out)
		out = append(out, modelCheck{id: id, lookup: lookup, provider: provider, sources: []string{section}})
	}

	list := func(section string, ids []string) {
		for _, id := range ids {
			add(section, id)
		}
	}
	list("cerebrasBudget", cfg.CerebrasBudget)
	list("cerebrasPerformance", cfg.CerebrasPerformance)
	list("groqBudget", cfg.GroqBudget)
	list("groqPerformance", cfg.GroqPerformance)
	list("reliableFree", cfg.ReliableFree)
	list("nvidiaReliable", cfg.NvidiaReliable)
	list("nvidiaComplex", cfg.NvidiaComplex)
	list("curatedPaid", cfg.CuratedPaid)
	list("excludeModels", cfg.ExcludeModels)
	list("defaultResponseModel", []string{cfg.DefaultResponseModel})
	list("anthropicResponseModel", []string{cfg.AnthropicResponseModel})
	for role, ids := range cfg.RolePrepend {
		list("rolePrepend:"+role, ids)
	}
	for role, ids := range cfg.RoleLocalFirst {
		list("roleLocalFirst:"+role, ids)
	}
	list("blockSmallCloudWhenLocalGPU", cfg.BlockSmallCloudWhenLocalGPU.Models)

	sort.Slice(out, func(i, j int) bool { return out[i].id < out[j].id })
	return out
}

type providerCatalog struct {
	provider string
	ids      map[string]bool
	err      error
}

func fetchCatalog(provider string) providerCatalog {
	cat := providerCatalog{provider: provider, ids: map[string]bool{}}
	var url, key string
	switch provider {
	case "openrouter":
		url = "https://openrouter.ai/api/v1/models" // public, no auth required
	case "groq":
		url = "https://api.groq.com/openai/v1/models"
		key = os.Getenv("GROQ_API_KEY")
	case "cerebras":
		url = "https://api.cerebras.ai/v1/models"
		key = os.Getenv("CEREBRAS_API_KEY")
	case "nvidia":
		url = "https://integrate.api.nvidia.com/v1/models"
		key = os.Getenv("NVIDIA_API_KEY")
	default:
		cat.err = fmt.Errorf("unknown provider %q", provider)
		return cat
	}
	if key == "" && provider != "openrouter" {
		cat.err = fmt.Errorf("no API key (set %s)", strings.ToUpper(provider)+"_API_KEY")
		return cat
	}
	req, err := newAuthorizedRequest("GET", url, key, "")
	if err != nil {
		cat.err = err
		return cat
	}
	client := &http.Client{Timeout: 20 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		cat.err = err
		return cat
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		cat.err = fmt.Errorf("status %d", resp.StatusCode)
		return cat
	}
	var wrapper struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&wrapper); err != nil {
		cat.err = err
		return cat
	}
	for _, m := range wrapper.Data {
		cat.ids[strings.TrimSpace(m.ID)] = true
	}
	return cat
}

// suggestReplacement picks the closest live id from the same provider's catalog,
// skipping non-chat model families (embed/rerank/vision/audio/reward) and any id
// already listed in excludeModels (known-bad), so approval can't reintroduce rot.
func suggestReplacement(catalogs map[string]providerCatalog, mc modelCheck) string {
	cat, ok := catalogs[mc.provider]
	if !ok || cat.err != nil || len(cat.ids) == 0 {
		return ""
	}
	excluded := map[string]bool{}
	for _, e := range globalModelsConfig.ExcludeModels {
		excluded[strings.TrimSpace(e)] = true
	}
	for _, marker := range configWeakModelMarkers() {
		if strings.Contains(strings.ToLower(mc.id), strings.ToLower(marker)) {
			return "" // replacement would inherit the same weakness (e.g. -8b-)
		}
	}
	wantVendor := ""
	if i := strings.Index(mc.id, "/"); i >= 0 {
		wantVendor = mc.id[:i]
	}
	junkRe := strings.Contains(strings.ToLower(mc.id), "vision")
	candidates := make([]string, 0, len(cat.ids))
	for id := range cat.ids {
		candidates = append(candidates, id)
	}
	sort.Strings(candidates) // deterministic suggestions across runs
	best, bestScore, bestBase := "", -1, -1
	for _, id := range candidates {
		if excluded[strings.TrimPrefix(id, mc.provider+"/")] || excluded[id] {
			continue
		}
		lower := strings.ToLower(id)
		if strings.Contains(lower, "embed") || strings.Contains(lower, "riva") ||
			strings.Contains(lower, "whisper") || strings.Contains(lower, "guard") ||
			strings.Contains(lower, "reward") || strings.Contains(lower, "-parse") ||
			strings.Contains(lower, "rerank") || strings.Contains(lower, "tts") ||
			strings.Contains(lower, "safety") || strings.Contains(lower, "moderation") {
			continue
		}
		if !junkRe && strings.Contains(lower, "vision") {
			continue // only suggest vision models when replacing a vision model
		}
		base := tokenOverlapScore(mc.id, id)
		score := base
		if vendorOf(id) == wantVendor {
			score += 100 // strong preference: same vendor namespace
		}
		if score > bestScore {
			best, bestScore, bestBase = id, score, base
		}
	}
	// Require real name overlap — vendor match alone must not produce a wild
	// suggestion (e.g. nemotron-super -> llama3-chatqa, or unrelated Groq
	// "compound" agentic systems). Four shared/prefix tokens means the ids are
	// recognisably the same model lineage; anything weaker deserves human review.
	if bestScore <= 0 || bestBase < 4 {
		return ""
	}
	switch mc.provider {
	case "cerebras":
		if !strings.HasPrefix(best, "cerebras/") {
			return "cerebras/" + best
		}
	case "groq":
		if !strings.HasPrefix(best, "groq/") {
			return "groq/" + best
		}
	}
	return best
}

func vendorOf(id string) string {
	if i := strings.Index(id, "/"); i >= 0 {
		return id[:i]
	}
	return ""
}

func tokenOverlapScore(a, b string) int {
	split := func(s string) map[string]bool {
		m := map[string]bool{}
		for _, tok := range strings.FieldsFunc(strings.ToLower(s), func(r rune) bool {
			return r == '-' || r == '/' || r == '.' || r == ':' || r == '_'
		}) {
			if len(tok) > 1 {
				m[tok] = true
			}
		}
		return m
	}
	at, bt := split(a), split(b)
	score := 0
	for t := range at {
		if bt[t] {
			score += 2
			continue
		}
		for u := range bt {
			if strings.HasPrefix(t, u) || strings.HasPrefix(u, t) {
				score++
				break
			}
		}
	}
	return score
}

// probeModel sends a minimal chat completion to see whether the model actually
// answers — catches announced retirements still present in the catalog.
func probeModel(provider, lookup string) (status, detail string) {
	var url, key string
	switch provider {
	case "openrouter":
		url = "https://openrouter.ai/api/v1/chat/completions"
		key = os.Getenv("OPENROUTER_API_KEY")
	case "groq":
		url = "https://api.groq.com/openai/v1/chat/completions"
		key = os.Getenv("GROQ_API_KEY")
	case "cerebras":
		url = "https://api.cerebras.ai/v1/chat/completions"
		key = os.Getenv("CEREBRAS_API_KEY")
	case "nvidia":
		url = "https://integrate.api.nvidia.com/v1/chat/completions"
		key = os.Getenv("NVIDIA_API_KEY")
	default:
		return "SKIP", "no probe for provider"
	}
	if key == "" {
		return "SKIP", "no API key"
	}
	body := fmt.Sprintf(`{"model":%q,"messages":[{"role":"user","content":"ping"}],"max_tokens":1}`, lookup)
	req, err := newAuthorizedRequest("POST", url, key, body)
	if err != nil {
		return "ERROR", err.Error()
	}
	req.Header.Set("Content-Type", "application/json")
	client := &http.Client{Timeout: 20 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return "ERROR", err.Error()
	}
	defer resp.Body.Close()
	switch {
	case resp.StatusCode == http.StatusOK:
		return "OK", "answered probe"
	case resp.StatusCode == http.StatusTooManyRequests:
		return "RATE-LIMITED", "alive but rate-limited"
	case resp.StatusCode == 404 || resp.StatusCode == 400:
		snippet := readErrSnippet(resp)
		return "DEAD", fmt.Sprintf("HTTP %d: %s", resp.StatusCode, snippet)
	default:
		return "ERROR", fmt.Sprintf("HTTP %d", resp.StatusCode)
	}
}

func readErrSnippet(resp *http.Response) string {
	buf := make([]byte, 160)
	n, _ := resp.Body.Read(buf)
	return strings.ReplaceAll(string(buf[:n]), "\n", " ")
}

func runModelDoctor(apply, autoYes, probe bool) {
	loadModelsConfig()
	models := collectReferencedModels()
	fmt.Printf("Model doctor: %d unique model ids referenced in models.yaml\n\n", len(models))

	needed := map[string]bool{}
	for _, m := range models {
		needed[m.provider] = true
	}
	var mu sync.Mutex
	catalogs := map[string]providerCatalog{}
	var wg sync.WaitGroup
	for provider := range needed {
		wg.Add(1)
		go func(p string) {
			defer wg.Done()
			cat := fetchCatalog(p)
			mu.Lock()
			catalogs[p] = cat
			mu.Unlock()
		}(provider)
	}
	wg.Wait()
	for _, p := range []string{"openrouter", "groq", "cerebras", "nvidia"} {
		if cat, ok := catalogs[p]; ok {
			if cat.err != nil {
				fmt.Printf("catalog %-11s: UNAVAILABLE (%v)\n", p, cat.err)
			} else {
				fmt.Printf("catalog %-11s: %d models live\n", p, len(cat.ids))
			}
		}
	}
	fmt.Println()

	if probe {
		fmt.Println("probing models (tiny chat call each, concurrency 4)...")
		sem := make(chan struct{}, 4)
		var pwg sync.WaitGroup
		for i := range models {
			if catalogs[models[i].provider].err != nil {
				continue
			}
			sem <- struct{}{}
			pwg.Add(1)
			go func(mc *modelCheck) {
				defer pwg.Done()
				defer func() { <-sem }()
				status, detail := probeModel(mc.provider, mc.lookup)
				mu.Lock()
				mc.status, mc.detail = status, detail
				mu.Unlock()
			}(&models[i])
		}
		pwg.Wait()
		fmt.Println()
	}

	missing := 0
	fmt.Printf("%-42s %-11s %-12s %s\n", "MODEL", "PROVIDER", "STATUS", "DETAIL/SOURCES")
	for i := range models {
		m := &models[i]
		cat := catalogs[m.provider]
		// excludeModels entries are deliberate documentation of known-bad ids.
		excludedOnly := true
		for _, s := range m.sources {
			if s != "excludeModels" {
				excludedOnly = false
				break
			}
		}
		if excludedOnly {
			m.status, m.detail = "SKIP", "listed in excludeModels (known-bad)"
			m.replacement = ""
		}
		if m.status == "" {
			switch {
			case cat.err != nil:
				m.status, m.detail = "SKIP", cat.err.Error()
			case cat.ids[m.lookup] || cat.ids[m.id]:
				m.status, m.detail = "OK", ""
			default:
				m.status = "MISSING"
				m.replacement = suggestReplacement(catalogs, *m)
				m.detail = "not in catalog"
				if m.replacement != "" {
					m.detail += fmt.Sprintf(" → suggest %s", m.replacement)
				}
				missing++
			}
		}
		src := strings.Join(m.sources, ",")
		if len(src) > 46 {
			src = src[:43] + "..."
		}
		line := fmt.Sprintf("%-42s %-11s %-12s %s", m.id, m.provider, m.status, m.detail)
		if m.status == "OK" && !probe {
			line += " " + src
		}
		fmt.Println(line)
	}
	fmt.Println()
	if missing == 0 {
		fmt.Println("All referenced models are present in their provider catalogs.")
		return
	}
	fmt.Printf("%d model(s) missing from their provider catalog.\n", missing)

	if !apply {
		fmt.Println("Re-run with -apply-models to back up models.yaml and fix them.")
		return
	}

	backup := fmt.Sprintf("models.yaml.bak-%s", time.Now().Format("20060102-150405"))
	data, err := os.ReadFile(modelsYAMLPath())
	if err != nil {
		fmt.Fprintf(os.Stderr, "read models.yaml: %v\n", err)
		os.Exit(1)
	}
	lines := strings.Split(string(data), "\n")
	inExclude := false
	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "excludeModels:" {
			inExclude = true
			continue
		}
		if inExclude && trimmed != "" && !strings.HasPrefix(trimmed, "-") && !strings.HasPrefix(trimmed, "#") {
			inExclude = false // next top-level key ends the block
		}
		if inExclude {
			lines[i] = "\x00SKIP\x00" + line // masked from replacement, restored below
		}
	}
	masked := strings.Join(lines, "\n")
	updated := masked
	changed := 0
	fmt.Println("\nProposed changes:")
	for _, m := range models {
		if m.status != "MISSING" || m.replacement == "" || m.replacement == m.id {
			continue
		}
		oldQuoted, newQuoted := `"`+m.id+`"`, `"`+m.replacement+`"`
		n := strings.Count(updated, oldQuoted)
		if n == 0 {
			continue
		}
		nMasked := strings.Count(updated, "\x00SKIP\x00"+oldQuoted)
		fmt.Printf("  %s -> %s (%d reference(s))%s\n", m.id, m.replacement, n-nMasked,
			map[bool]string{true: " (excludeModels refs left untouched)", false: ""}[nMasked > 0])
		changed += n - nMasked
		updated = strings.ReplaceAll(updated, oldQuoted, newQuoted)
	}
	// Collect dead ids with no confident replacement — offer to prune them
	// (removing the dead entry lets the chain fall through to the next live
	// candidate instead of burning seconds on a 404 each request).
	var prunable []modelCheck
	for _, m := range models {
		if m.status == "MISSING" && m.replacement == "" {
			prunable = append(prunable, m)
		}
	}
	pruned := 0
	if len(prunable) > 0 {
		fmt.Printf("\n%d dead model(s) have no confident replacement and will be left as dead fallbacks:\n", len(prunable))
		for _, m := range prunable {
			fmt.Printf("  %s  (%s)\n", m.id, strings.Join(m.sources, ","))
		}
		fmt.Println("Tip: remove these ids from their lists so the chain skips them, or pick a live peer manually.")
		if apply {
			// Stage pruning: remove dead ids from non-excluded sections (masked lines are skipped).
			lines2 := strings.Split(updated, "\n")
			keep := make([]string, 0, len(lines2))
			for _, line := range lines2 {
				if strings.HasPrefix(line, "\x00SKIP\x00") {
					keep = append(keep, line)
					continue
				}
				drop := false
				for _, m := range prunable {
					if strings.Contains(line, `"`+m.id+`"`) {
						drop = true
						break
					}
				}
				if drop {
					pruned++
					continue
				}
				keep = append(keep, line)
			}
			updated = strings.Join(keep, "\n")
			if pruned > 0 {
				fmt.Printf("Staged pruning of %d dead entries (outside excludeModels).\n", pruned)
			}
		}
	}
	updated = strings.ReplaceAll(updated, "\x00SKIP\x00", "")
	if changed == 0 && pruned == 0 {
		fmt.Println("No safe textual replacements available (review MISSING list manually).")
		return
	}
	if changed == 0 && !apply {
		fmt.Println("Re-run with -apply-models to prune, or edit manually.")
		return
	}
	fmt.Printf("\nBackup: %s\n", backup)
	if !autoYes {
		if pruned > 0 {
			fmt.Printf("Apply %d replacement(s) and prune %d dead entries? [y/N] ", changed, pruned)
		} else {
			fmt.Print("Apply these changes to models.yaml? [y/N] ")
		}
		reader := bufio.NewReader(os.Stdin)
		answer, _ := reader.ReadString('\n')
		answer = strings.ToLower(strings.TrimSpace(answer))
		if answer != "y" && answer != "yes" {
			fmt.Println("Aborted — models.yaml unchanged.")
			return
		}
	}
	if err := os.WriteFile(backup, data, 0644); err != nil {
		fmt.Fprintf(os.Stderr, "write backup: %v\n", err)
		os.Exit(1)
	}
	if err := os.WriteFile(modelsYAMLPath(), []byte(updated), 0644); err != nil {
		fmt.Fprintf(os.Stderr, "write models.yaml: %v\n", err)
		os.Exit(1)
	}
	if pruned > 0 {
		fmt.Printf("Wrote %d replacement(s) and pruned %d dead entries in %s (old version saved as %s). Restart freeride to reload.\n",
			changed, pruned, filepath.Base(modelsYAMLPath()), backup)
	} else {
		fmt.Printf("Wrote %d replacement(s) to %s (old version saved as %s). Restart freeride to reload.\n",
			changed, filepath.Base(modelsYAMLPath()), backup)
	}
}

func modelsYAMLPath() string { return "models.yaml" }

// newAuthorizedRequest builds an HTTP request with an optional Bearer token and body.
func newAuthorizedRequest(method, url, bearer, body string) (*http.Request, error) {
	var reader io.Reader
	if body != "" {
		reader = strings.NewReader(body)
	}
	req, err := http.NewRequest(method, url, reader)
	if err != nil {
		return nil, err
	}
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	return req, nil
}
