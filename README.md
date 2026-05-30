# Freeride Proxy (v1.3.0)

A stand-alone, Ollama-compatible proxy that dynamically fetches and serves free models from **Google Gemini API**, **Cerebras**, **OpenRouter**, **NVIDIA (NIM)**, and **Ollama Cloud**. Runs locally on port `:11434`, intercepting requests to the OpenAI-compatible endpoint (`/v1/chat/completions`) and the Ollama native model listing endpoint (`/api/tags`).

Use it **standalone** with any OpenAI-compatible client, or as the LLM backend for [Gas Town](https://github.com/gastownhall/gastown) multi-agent orchestration.

---

## What's New (v1.3.0)

- **Cloud-first local GPU**: With `--allow-local-openai`, capable cloud models (70B NVIDIA, OpenRouter free tiers, large Cerebras, etc.) are tried first; `localOpenAI` (llama-server on `:8080`) is **last-resort fallback** only when cloud routes fail or are in cooldown.
- **Weak-cloud blocking**: `blockSmallCloudWhenLocalGPU` in `models.yaml` skips nano/mini/8B cloud models when local GPU mode is on — so fallback does not mean “downgrade to 8B.”
- **Config-only model IDs**: Routing lists, role prepends, compat model stubs, and score boosts live in `models.yaml` — not hardcoded in Go.
- **Request sanitization**: Anthropic `tools` / `system` payloads are normalized before upstream forwarding (`sanitizeBody`).
- **Faster default tests**: `go test ./...` runs unit and in-process httptest suites (including Gemini direct routing tests); live proxy tests require `FREERIDE_INTEGRATION=1`. The `scratch/` tree is dev-only (`//go:build ignore`) and is not part of the test run.

- **Google Gemini API (direct)**: Free-tier Flash models via `GEMINI_API_KEY` and `geminiModels` in `models.yaml` — no OpenRouter markup. Gas Town **polecat** defaults to `gt-agent-gemini` (`google/gemini-3.5-flash`).

Earlier (v1.2.0): headless `gt-agent`, NATS transport, Proxy-Magic tool extraction, strict zero-cost mode (503 when free tier exhausted).

### Routing order (summary)

1. Cerebras budget / performance (from `models.yaml`)
2. **Gemini API direct** (`geminiModels` in `models.yaml`, requires `GEMINI_API_KEY`)
3. **Groq API direct** (`groqBudget` / `groqPerformance` in `models.yaml`, requires `GROQ_API_KEY`)
4. Role prepends (`rolePrepend`, when `--allow-paid`)
5. Reliable free + NVIDIA lists, Ollama cloud, original model
6. Curated paid (`curatedPaid`, when `--allow-paid` + complex)
7. IDE bridges (`--allow-ide`)
8. **Local llama-server** (`localOpenAI`, `--allow-local-openai`) — **after** capable cloud

---

## Prerequisites

- Go 1.18+ (for building from source)
- A **Cerebras API key** (Optional, for fastest inference)
- A **Groq API key** (Optional, for fast inference fallback)
- A **Gemini API key** (optional, for free Google Flash models via `geminiModels` in `models.yaml`)
- An **OpenRouter API key** (for free OpenRouter models)
- An **NVIDIA API key** (for highest-performance free NIM models)
- An **Ollama API key** (for Ollama Cloud models like Qwen3 480B and DeepSeek V4)

## Cloning freeride (Gas Town submodule)

This repository includes **[Gas Town](https://github.com/gastownhall/gastown)** as a **git submodule** at [`gastown/`](gastown/). The submodule is pinned to the [stevef1uk/gastown](https://github.com/stevef1uk/gastown) fork (see [`.gitmodules`](.gitmodules)).

A plain `git clone` **does not** download submodule contents — `gastown/` will be empty until you initialize it.

### First-time clone (recommended)

```bash
git clone --recurse-submodules https://github.com/stevef1uk/freeride.git
cd freeride
```

### Already cloned without submodules

From the freeride repo root:

```bash
git submodule update --init --recursive
```

### Verify and install Gas Town binaries

```bash
test -f gastown/go.mod && echo "gastown submodule OK"

cd gastown
make install    # builds gt, gt-agent, … → ~/.local/bin
```

Ensure `~/.local/bin` is on your `PATH`. After you change the submodule commit or pull freeride updates, refresh the submodule and reinstall:

```bash
git pull
git submodule update --init --recursive
cd gastown && git pull && make install
```

**Town root (`~/gt`)** is separate: create it with `gt install` / `gt rig add` per Gas Town docs. Freeride only supplies the proxy; the submodule supplies the `gt` and `gt-agent` binaries and orchestrator templates synced by `make install`.

## Building and Running

### Standalone Use

1. Build:
   ```bash
   make build
   # Or manually: go build -o freeride .
   ```

2. Configure API keys in a `.env` file in the directory where you start Freeride:
   ```bash
   cp .env.template .env
   # edit .env — set keys for the providers you use in models.yaml
   ```
   ```env
   OPENROUTER_API_KEY=sk-or-v1-...
   GEMINI_API_KEY=...          # from https://aistudio.google.com/apikey
   NVIDIA_API_KEY=nvapi-...
   OLLAMA_API_KEY=1b18...
   CEREBRAS_API_KEY=csk-...
   GROQ_API_KEY=gsk_...
   ```
   On startup, Freeride reads `.env` from the **current working directory** (`KEY=value` lines; `#` comments are ignored). You can use shell exports instead if you prefer.

3. Run (add `--allow-local-openai` if you use `localOpenAI` in `models.yaml`):
   ```bash
   make run
   # Or manually: ./freeride --debug --allow-local-openai > freeride_live.log 2>&1 &
   ```

4. Test:
   ```bash
   make test
   # Or manually: go test ./... -v -count=1
   ```
   To verify the proxy is running locally, use:
   ```bash
   curl -s http://localhost:11434/v1/models | head -5
   ```

### CLI Flags

- `--debug`: Verbose logging of requests, routing decisions, and API responses.
- `--allow-paid`: Allows paid models as fallback. **Disabled by default** (strict zero-cost mode).
- `--allow-ide`: Allows `ideModels` entries in `models.yaml` (local IDE bridges) as a last-resort fallback. **Disabled by default**.
- `--allow-local-openai`: Enables `localOpenAI` fallback (after capable cloud) and applies `blockSmallCloudWhenLocalGPU` from `models.yaml`. **Disabled by default**. Does **not** force local over cloud — keep Gas Town `LLM_MODEL` on a strong cloud id (e.g. `meta/llama-3.3-70b-instruct`).

---

## Google Gemini API (direct)

Freeride can call **Google’s Gemini API** directly using its [OpenAI-compatible endpoint](https://ai.google.dev/gemini-api/docs/openai). This is separate from OpenRouter’s `google/*` models: traffic goes to `generativelanguage.googleapis.com`, uses your **AI Studio** key, and qualifies for Google’s **free tier** (rate-limited; see [Gemini API pricing](https://ai.google.dev/gemini-api/docs/pricing)).

OpenRouter no longer offers free Gemini Flash tiers; direct routing is the practical way to use Flash at zero token cost.

### Setup

1. Create an API key at [Google AI Studio](https://aistudio.google.com/apikey).
2. Add to Freeride’s `.env` (repo root, same directory you start `./freeride` from):

   ```env
   GEMINI_API_KEY=your-key-here
   ```

   `GOOGLE_API_KEY` is also accepted. Freeride trims whitespace around `=` (bash `source .env` does not — use `KEY=value` with no space after `=` if you source manually).

3. Ensure `geminiModels` in `models.yaml` lists the routes you want (defaults ship in-repo):

   ```yaml
   geminiModels:
     - id: "google/gemini-3.5-flash"      # Freeride / client model name
       model: "gemini-3.5-flash"          # exact name sent to Google
     - id: "google/gemini-2.5-flash-lite"
       model: "gemini-2.5-flash-lite"
   ```

4. Restart Freeride. On startup you should see: `Gemini API direct routing enabled`.

If the key is missing, Gemini routes are skipped (other providers still work).

### How routing works

| Concept | Value |
|--------|--------|
| Client / Gas Town model id | `google/gemini-3.5-flash` (from `geminiModels[].id`) |
| Upstream API model | `gemini-3.5-flash` (from `geminiModels[].model`) |
| Upstream URL | `https://generativelanguage.googleapis.com/v1beta/openai/chat/completions` |
| Candidate tier | **0.15** — after Cerebras budget, before OpenRouter/NVIDIA fallbacks |
| Treated as “free” | Yes (for zero-cost mode; does not use OpenRouter pricing) |

Freeride tries candidates in order; the first non-cooldown upstream success wins. If the client requests `google/gemini-3.5-flash`, that id is tried early (Tier 0 when configured and not in cooldown).

**Role: polecat** — `polecat` is in `massiveOnlyRoles`; ids containing `gemini` pass the “massive model” filter. With default Gas Town config, polecat uses `gt-agent-gemini` so **`google/gemini-3.5-flash` is the primary model**, with Freeride fallbacks (e.g. `google/gemini-2.5-flash-lite`, then NVIDIA/OpenRouter 70B-class models) on errors or rate limits.

**Role: architect / mayor / planner** — Gemini is still in the waterfall unless excluded; set `LLM_MODEL` to `google/gemini-3.5-flash` on the agent profile to prefer it.

### Verify the key

From the Freeride repo:

```bash
python3 scratch/test_gemini_key.py
```

Expect `PASS` lines for the configured models. A `400 API_KEY_INVALID` means regenerate the key in AI Studio; `429` means free-tier quota — wait or use fallbacks.

Direct curl (replace `$GEMINI_API_KEY`):

```bash
curl -s "https://generativelanguage.googleapis.com/v1beta/openai/chat/completions" \
  -H "Authorization: Bearer $GEMINI_API_KEY" \
  -H "Content-Type: application/json" \
  -d '{"model":"gemini-3.5-flash","messages":[{"role":"user","content":"Say GEMINI_OK"}],"max_tokens":32}'
```

### Gas Town: `gt-agent-gemini`

Fresh `gt install` maps **polecat → `gt-agent-gemini`** (`LLM_MODEL=google/gemini-3.5-flash`). See [Gas Town Integration](#gas-town-integration) for full `settings/config.json`. Existing towns must add the `gt-agent-gemini` agent block and set `"polecat": "gt-agent-gemini"` in `role_agents`, then restart polecat sessions.

### Adding or changing models

Edit `models.yaml` only — no Go changes required:

```yaml
geminiModels:
  - id: "google/gemini-2.5-flash"   # what clients pass as model=
    model: "gemini-2.5-flash"       # must match a Google API model id
    cooldown: "30s"                 # optional per-model cooldown override
```

Use model ids from [Google’s model docs](https://ai.google.dev/gemini-api/docs/models). Not every id works on every account; test with `scratch/test_gemini_key.py` before relying on a route.

### OpenRouter `google/*` vs direct

| Path | Key | Example id | Cost |
|------|-----|------------|------|
| **Direct (`geminiModels`)** | `GEMINI_API_KEY` | `google/gemini-3.5-flash` | Free tier (Google limits) |
| **OpenRouter** | `OPENROUTER_API_KEY` | `google/gemini-2.0-flash-001` | Paid per token (or `--allow-paid`) |

Only ids listed under `geminiModels` use the direct API. Other `google/*` requests still go to OpenRouter.

### Notes

- **Thinking models**: `gemini-3.5-flash` may use internal reasoning tokens; use adequate `max_tokens` for short replies in tests.
- **Tools**: Gemini supports tool-style requests; Freeride does not apply NVIDIA-style `tool_choice` stripping to direct Gemini routes.
- **Discovery**: When the key is set, `google/gemini-*` ids appear on `/v1/models` and `/api/tags` with `owned_by: google-gemini`.

---

## Groq API (direct)

Freeride supports routing to **Groq** for fast inference, typically used as a fallback when Cerebras is unavailable or rate-limited. 

### Setup

1. Create an API key at [GroqCloud](https://console.groq.com/keys).
2. Add it to Freeride's `.env`:
   ```env
   GROQ_API_KEY=your-key-here
   ```
3. Ensure your `models.yaml` includes the Groq routes under `groqBudget` or `groqPerformance` (defaults ship in-repo).

When configured, Freeride will prioritize Groq models as defined in your routing order (e.g., Tier 0.6).

---

## Integration with AI Tools

### OpenCode (TUI / Interactive)

For interactive terminal use with a TUI:

```bash
export OPENAI_BASE_URL="http://localhost:11434/v1"
export OPENAI_API_KEY="dummy"
opencode --model openai/gpt-4o
```

### Any OpenAI-Compatible Client

Point any client that supports OpenAI-compatible endpoints to:
- **Base URL**: `http://localhost:11434/v1`
- **API Key**: `dummy` (or any non-empty string)
- **Model**: `gpt-4o` or `claude-3-5-sonnet-20241022` (see Power Model Spoofing below)

---

## Gas Town Integration

Freeride serves as the LLM backend for [Gas Town](https://github.com/gastownhall/gastown) multi-agent orchestration. The proxy runs on `:11434`; agents communicate via NATS on `:4222`.

**Submodule required:** Install the `gastown/` tree first — see [Cloning freeride (Gas Town submodule)](#cloning-freeride-gas-town-submodule) above (`git clone --recurse-submodules` or `git submodule update --init --recursive`, then `cd gastown && make install`).

**Freeride `.env`:** Gas Town `gt-agent` sessions only need `LLM_ENDPOINT` / `LLM_MODEL` in `settings/config.json` (see below). Provider API keys live in **Freeride’s** `.env` — start the proxy from your Freeride repo (or any directory that contains a `.env` with `GEMINI_API_KEY`, `OPENROUTER_API_KEY`, `NVIDIA_API_KEY`, etc.). `gt install` does not create this file; copy `.env.template` → `.env` in the Freeride tree before `gt up`. Polecat implementation work expects **`GEMINI_API_KEY`** when using `gt-agent-gemini` (see [Google Gemini API](#google-gemini-api-direct)).

### Architecture Overview

```
┌─────────────┐     ┌──────────────┐     ┌─────────────────┐
│  Gas Town   │────▶│   NATS       │────▶│  gt-agent       │
│  (Mayor,    │     │  (port 4222) │     │  (headless)     │
│   Witness,  │     └──────────────┘     │  Drains nudges  │
│   Deacon)   │                          │  Calls LLM      │
└─────────────┘                          │  Executes work  │
                                         │  Exits cleanly  │
                                         └────────┬────────┘
                                                  │
                                                  ▼
                                         ┌─────────────────┐
                                         │  Freeride Proxy │
                                         │  (port 11434)   │
                                         │  Routes to free │
                                         │  Gemini/Cerebras│
                                         │  Groq/OpenRouter│
                                         │  NIM/Ollama     │
                                         └─────────────────┘
```

### Agent Types

| Agent | Role | Transport | Use Case |
|-------|------|-----------|----------|
| **gt-agent** | Headless worker | NATS | Automated background tasks (Mayor, Deacon, Witness, Refinery, Polecats) |
| **opencode** | Interactive TUI | NATS | Interactive crew members requiring human oversight |

### Configuration

Create or update `settings/config.json` in your Gas Town project root:

```json
{
  "type": "town-settings",
  "version": 1,
  "session_transport": "nats",
  "default_agent": "gt-agent-local",
  "role_agents": {
    "polecat": "gt-agent-gemini",
    "mayor": "gt-agent-local",
    "deacon": "gt-agent-local",
    "witness": "gt-agent-local",
    "refinery": "gt-agent-local",
    "crew": "opencode"
  },
  "agents": {
    "opencode": {
      "command": "/home/YOURNAME/.opencode/bin/opencode",
      "args": ["--model", "openai/gpt-4o"],
      "env": {
        "OPENAI_BASE_URL": "http://localhost:11434/v1",
        "OPENAI_API_BASE": "http://localhost:11434/v1",
        "OPENAI_API_KEY": "dummy",
        "OPENCODE_PERMISSION": "{\"*\":\"allow\"}"
      }
    },
    "gt-agent-local": {
      "command": "/home/YOURNAME/.local/bin/gt-agent",
      "args": [],
      "env": {
        "LLM_ENDPOINT": "http://localhost:11434/v1/chat/completions",
        "LLM_MODEL": "meta/llama-3.2-11b-vision-instruct"
      }
    },
    "gt-agent-gemini": {
      "command": "/home/YOURNAME/.local/bin/gt-agent",
      "args": [],
      "env": {
        "LLM_ENDPOINT": "http://localhost:11434/v1/chat/completions",
        "LLM_MODEL": "google/gemini-3.5-flash"
      }
    }
  }
}
```

Fresh `gt install` defaults map **polecat → gt-agent-gemini** (`google/gemini-3.5-flash` via Freeride direct Gemini routing). Requires `GEMINI_API_KEY` in Freeride’s `.env` — see [Google Gemini API](#google-gemini-api-direct). Existing towns: set `"polecat": "gt-agent-gemini"` in `role_agents` and add the `gt-agent-gemini` agent block above (or re-merge from `DefaultFreerideAgents()` in `gastown/internal/config/freeride_agents.go`).

**Key settings:**
- `session_transport`: Set to `"nats"` for NATS-based session management (no tmux required)
- `default_agent`: `"gt-agent-local"` for headless automated work
- `role_agents.crew`: `"opencode"` for interactive TUI-based crew members
- `gt-agent-local.env.LLM_ENDPOINT`: Points directly to Freeride's OpenAI-compatible endpoint

### Running Gas Town

```bash
# 0. Gas Town CLI from the submodule (once per machine / after submodule updates)
git submodule update --init --recursive
cd gastown && make install && cd ..

# 1. Ensure Freeride has API keys (.env in the directory you start it from)
cp .env.template .env   # once, in the freeride repo; edit with your keys

# 2. Ensure Freeride proxy is running (from that repo so .env is loaded)
./freeride --debug > freeride_live.log 2>&1 &

# 3. Ensure NATS is available (Docker or standalone)
# Docker: docker run -d --name gt-nats -p 4222:4222 nats:latest

# 4. Start Gas Town services (from your town root, e.g. ~/gt)
gt up

# 5. Check status
gt status

# 6. Assign work to a polecat (automatic via Mayor, or manual)
gt hook de-123 defender/polecats/obsidian
```

### Orchestrator (rig-flow pipeline)

For **structured rig delivery** (SPEC → design → plan → implement → QA), Gas Town runs a central workflow FSM (`rig-flow`) instead of each agent self-dispatching via hooks and mail. Agents use **NATS** sessions with `gt-agent --orchestrated`; there is no tmux requirement when `session_transport` is `"nats"`.

Full documentation lives in the **gastown** submodule:

- [Orchestrator (concept)](gastown/docs/concepts/orchestrator.md) — quickstart, QA outcomes, reset
- [rig-flow operator notes](gastown/internal/orchestrator/town/README.md) — YAML hooks, timeouts, stall recovery
- [Orchestrator (design)](gastown/docs/design/orchestrator.md) — implementation detail

**Town root** (`~/gt` or your `GT_ROOT`) is separate from this freeride repo. After submodule install, sync templates into the town:

```bash
cd gastown && make install          # builds gt, gt-agent; copies orchestrator/templates + prompts
cd ~/gt                             # your town root
gt orchestrator sync --update-changed
```

**Bring up services and the orchestrator MCP process** (`gt up` starts NATS, daemon, and `gt orchestrator run`; PID in `daemon/orchestrator.pid`):

```bash
./freeride --debug > freeride_live.log 2>&1 &   # from freeride repo (API keys in .env)
cd ~/gt
gt up
gt orchestrator status    # running PID + MCP ping
gt status -v              # NATS sessions (e.g. te-<rig>-polecat), not tmux
```

**Start a rig pipeline** (example rig `testgt3`):

```bash
gt mayor workflow start rig-flow --rig testgt3
gt mayor workflow status
```

**Rig agents only when a workflow is running** — use orchestrator-only mode to skip legacy town `hq-architect` / `hq-qa` / `hq-polecat`:

```bash
gt up --orchestrator-only
```

Per-rig pipeline sessions (NATS) include `te-<rig>-polecat`, `te-<rig>-architect`, `te-<rig>-qa`, plus town `hq-planner` / `hq-setup` for planning and project setup. Tail logs under `~/gt/<rig>/polecat/typescript`, etc. (see the concept doc table).

**Incremental bug fixes:** For existing source files, the polecat must use native **`EDIT:`** / **`WRITE:`** (or **`sed -i`** / **`patch`** as fallback — not full-file `cat > … <<'EOF'` rewrites). gt-agent enforces this automatically when the file is already on disk; new files still use heredoc.

### Polecat host tools (optional)

Install on the **same machine** that runs `gt-agent` for rig-flow **implementation** (the per-rig polecat NATS session, e.g. `~/gt/<rig>/polecat/`). These are not part of planner **project_setup** (module/venv only); they help the polecat during implement.

#### Codeindex (dependency blast radius)

[codeindex](https://github.com/scheidydude/codeindex) is optional. When the `codeindex` CLI is on `PATH`, gt-agent:

1. Runs **`refresh_codeindex`** at the start of each implementation task (`rig-flow` `pre_run`).
2. Builds or refreshes **`{rig}/mayor/rig/codeindex.json`** from the profile `layout_root` (e.g. `linkshelf/`).
3. Injects a **Codeindex blast radius** section into each implement-bead prompt (`codeindex impact` on the active file).

**One-time install:**

```bash
pip install codeindex
# or: pipx install codeindex

codeindex --help   # must succeed; gt-agent uses exec.LookPath("codeindex")
```

**Disable** without uninstalling:

```bash
export GT_CODEINDEX=0   # polecat agent env in settings/config.json, or shell before gt up
```

**Manual refresh** (debugging a rig worktree):

```bash
export GT_ROOT=~/gt
RIG=testgt3
cd "$GT_ROOT/$RIG/mayor/rig"
codeindex analyze linkshelf --output codeindex.json
codeindex symbols linkshelf --inline --index codeindex.json
codeindex impact internal/api/handlers.go --index codeindex.json
```

Use paths **relative to the layout root** passed to `analyze` (here `linkshelf/`, so `internal/api/handlers.go` not `linkshelf/internal/...`). If impact says the file is missing, re-run `analyze` after pulling polecat edits or check that `layout_root` in `{rig}/mayor/rig/.gastown/workflow-profile.json` matches your tree.

After updating gastown templates or this integration, sync into the town and reinstall binaries:

```bash
cd gastown && make install
cd ~/gt && gt orchestrator sync --update-changed
```

More detail: [rig-flow operator notes — Codeindex](gastown/internal/orchestrator/town/README.md) (native EDIT tools section).

#### goimports (Go rigs)

When `goimports` is on `PATH`, gt-agent runs it on the **package** after native **EDIT:**/**WRITE:** if verify reports unused imports (common on `*_test.go` while another bead owns production code).

```bash
go install golang.org/x/tools/cmd/goimports@latest
```

#### Orchestrator git checkpoints (`GT_*` env)

On each rig-flow FSM transition, **`gt orchestrator run`** may `git commit` dirty files in `{rig}/mayor/rig/`; on **`completed`** it **`git push`**es to `origin`. Polecat/QA do not push — the orchestrator does.

| Variable | Where to set | Effect |
|----------|----------------|--------|
| `GT_SKIP_WORKFLOW_GIT_COMMIT=1` | Environment of **`gt orchestrator run`** (or parent of `gt up` auto-start) | No checkpoint commits |
| `GT_WORKFLOW_SKIP_PUSH=1` | Same | Still commit locally; no push on `completed` |

`gt rig add` / `gt rig add --adopt` append mayor/rig ignore rules (`.beads/`, `*.db`, `codeindex.json`, build `server`, QA progress JSON) so checkpoints stay source-only. For an existing rig: `gt rig add <name> --adopt` refreshes rules, then `git rm -r --cached` any junk still tracked. Details: `gastown/docs/design/orchestrator.md`.

**Parked rigs do not start agents.** If `gt up` reports `skipped (rig parked)`, the rig was intentionally paused:

```bash
gt rig status testgt3
gt rig unpark testgt3
gt up --orchestrator-only
# or: gt rig start testgt3
```

**Useful checks when work stalls:**

```bash
gt orchestrator status
gt mayor workflow status
export BEADS_DIR=~/gt/testgt3/.beads && cd ~/gt/testgt3/mayor/rig && bd list --status=in_progress
tail -f ~/gt/logs/orchestrator.log
```

**Full rig rewind** (beads, instances, dev servers): `bash gastown/scripts/reset-rig-orchestrator.sh --force` from a checkout that includes the submodule (see gastown script header for `GT_ROOT` / `RIG`).

### Verification

#### Check the proxy is running:
```bash
curl -s http://localhost:11434/v1/models | head -5
```

#### Check requests are routing through the proxy:
```bash
tail -f freeride_live.log | grep -E "Attempting|\[LOCAL\]|succeeded|ERROR"

# Table of attempts vs completions (handles role= and [LOCAL] log lines):
python3 scripts/freeride_proxy_model_stats.py freeride_live.log
python3 scripts/freeride_proxy_model_stats.py freeride_live.log --roles --watch 5
```

#### Check agent processes:
```bash
# Should show gt-agent processes for headless agents
ps aux | grep gt-agent | grep -v grep

# Should show opencode only for interactive crew
ps aux | grep opencode | grep -v grep
```

### Troubleshooting

| Symptom | Cause | Fix |
|:---|:---|:---|
| `User not found` (401) | Invalid OpenRouter key | Generate a new key at openrouter.ai/settings/keys |
| `Insufficient credits` (402) | OpenRouter balance is $0 | Add credits at openrouter.ai/settings/credits |
| `Rate limited` (429) | Free model overloaded | Wait 10s or use NVIDIA models only |
| `No models available` | All models in cooldown | Check `cooldowns.json` and restart proxy |
| `404 Not Found` | Model no longer exists | Update `models.yaml` to remove deprecated models |
| Agent not starting | NATS unavailable | Ensure NATS container/server is running on port 4222 |
| Proxy not responding | Freeride not running | Run `./freeride --debug > freeride_live.log 2>&1 &` |

---

## Model Configuration

**All model IDs and routing policy are in `models.yaml`** — edit that file to add/remove models, role overrides, or local GPU settings. Go code only implements tier logic and provider prefixes.

### Local llama-server (optional)

Typical setup with [llama.cpp](https://github.com/ggerganov/llama.cpp) `llama-server` on port `8080`:

```bash
# Terminal 1 — llama-server (example)
cd ~/dev/llama.cpp/build/bin
./llama-server --hf-repo unsloth/Qwen3-Coder-30B-A3B-Instruct-GGUF \
  --hf-file Qwen3-Coder-30B-A3B-Instruct-Q4_K_M.gguf \
  -ngl 30 --no-mmap -c 8192 -fa on --host 0.0.0.0 --port 8080

# Get the upstream JSON model name (not the GGUF filename):
curl -s http://127.0.0.1:8080/v1/models | jq -r '.data[0].id'
```

In `models.yaml`, `localOpenAI.id` is the route name clients may use; `localOpenAI.model` must match the server's `/v1/models` id:

```yaml
localOpenAI:
  - id: "local/qwen3-coder-30b"
    endpoint: "http://127.0.0.1:8080"
    model: "unsloth/Qwen3-Coder-30B-A3B-Instruct-GGUF"  # from curl above
    cooldown: "30m"
```

Run Freeride with `./freeride --debug --allow-local-openai`. Traffic uses cloud first; local GPU only when cloud is unavailable or cooled down.

### Example `models.yaml` (abbreviated)

```yaml
# Freeride Model Configuration

massiveOnlyRoles: [architect, mayor, planner, polecat]
rolePrepend:
  polecat: ["anthropic/claude-3.5-sonnet"]  # only with --allow-paid

# Priority 0.15: Google Gemini API free tier (requires GEMINI_API_KEY)
geminiModels:
  - id: "google/gemini-3.5-flash"
    model: "gemini-3.5-flash"
  - id: "google/gemini-2.5-flash-lite"
    model: "gemini-2.5-flash-lite"

# Priority 0.1: Free/Ultra-cheap Cerebras (High speed, zero/low cost)
cerebrasBudget:
  - "cerebras/llama3.1-8b"
  - "cerebras/gpt-oss-120b"
  - "cerebras/qwen-3-235b-a22b-instruct-2507"

# Priority 0.5: Paid Cerebras Performance (Used for COMPLEX only)
# Note: These require the --allow-paid flag to be used.
cerebrasPerformance:
  - "cerebras/llama3.3-70b"
  - "cerebras/llama3.1-70b"

# Priority 0.6: Groq (Used for fast inference when Cerebras fails or is missing)
# Note: Requires GROQ_API_KEY environment variable.
groqBudget:
  - "groq/llama-3.1-8b-instant"

groqPerformance:
  - "groq/llama-3.3-70b-versatile"
  - "groq/qwen3-32b"

# Priority 1: Specifically requested reliable free models (OpenRouter)
reliableFree:
  - "meta-llama/llama-3.3-70b-instruct:free"
  - "deepseek/deepseek-v3:free"

# Priority 2: Reliable NVIDIA models
nvidiaReliable:
  - "meta/llama-3.3-70b-instruct"
  - "nvidia/llama-3.1-70b-instruct"

# Models that should be tried even if they are paid (if --allow-paid is set)
curatedPaid:
  - "openai/gpt-4o-mini"
  - "google/gemini-2.0-flash-001"
  - "anthropic/claude-3.5-sonnet"

# Models to exclude even if they are free
excludeModels:
  - "google/gemma-4-26b-a4b-it:free" # Currently broken 401

blockSmallCloudWhenLocalGPU:
  models: ["cerebras/llama3.1-8b", ...]
  patterns: ["nano", "mini", "-8b", ...]

localOpenAI: []   # or see local llama-server section above
```

**Verified working models (NVIDIA):**
- `meta/llama-3.3-70b-instruct` ✅
- `meta/llama-3.2-11b-vision-instruct` ✅
- `ollama/qwen3-coder:480b` ✅
- `ollama/deepseek-v4-pro` ✅

The proxy auto-discovers models from OpenRouter, NVIDIA, and Ollama Cloud; **`geminiModels`** are listed when `GEMINI_API_KEY` is set. `models.yaml` prioritizes the listed models. Optional **`localOpenAI`** backends are not used unless you pass **`--allow-local-openai`**.

---

## Power Model Spoofing (Tool Support)

Freeride advertises compat model ids from `compatModels` in `models.yaml` (defaults include `claude-3-5-sonnet-20241022` and `gpt-4o`). **Select those names** in clients that gate tools by model name. The proxy still routes to free or configured backends under the hood.

---

## Auto-Recovery & Cooldown

The proxy features transparent, proxy-level auto-recovery. If a request to a free model returns a rate limit (429) or server error (5xx), the proxy automatically intercepts the failure, places the failing model in cooldown, and retries the exact same request using the next highest-ranked free model.

**State Persistence**: Cooldowns are saved to `cooldowns.json` and persist across restarts.

---

## Proxy-Magic (Resilient Tool-Use)

Free models (like Llama 3.3 or Mixtral) often struggle with strict JSON-based tool calling, frequently choosing to "talk about" running a command instead of actually triggering the tool.

Freeride solves this by implementing **Proxy-Magic**:
1. **Markdown Extraction**: If a model returns a markdown bash block (```bash ... ```), the proxy automatically converts it into a `run_terminal_command` tool call.
2. **Conversational Extraction**: The proxy uses aggressive regex to catch conversational intent, such as:
   - "I will now run `gt hook`"
   - "I'm going to run `bd list`"
3. **Deduplication**: If a model both talks about and uses the tool, the proxy deduplicates the requests to prevent double-execution.

This mechanism ensures that the **Gas Town** agent ecosystem remains fully autonomous even when running on zero-cost models.

---

## Supported Endpoints

- `GET /api/tags`: Lists available free models (formatted as Ollama tags).
- `POST /v1/chat/completions`: Standard OpenAI chat endpoint.
- `POST /v1/messages`: Anthropic Messages endpoint (with full SSE translation).
- `GET /v1/models`: Returns the current ranked list of all available free models.

---

## Testing

### Default (fast, no live proxy)

```bash
go test ./... -v -count=1
```

Runs in milliseconds:
- **`candidate_test.go`** — cloud-first candidate order, local last, weak-cloud blocking
- **`gemini_direct_test.go`** — Gemini API routing, polecat candidate order, `/api/tags` discovery, auth env keys
- **`local_openai_test.go`** — local route hits upstream, maps `model` field
- **`proxy_protocol_test.go`** — Anthropic tools, large system prompts, role routing (httptest mocks)

### Live integration (optional)

Requires Freeride listening (default `http://localhost:11434`):

```bash
./freeride --debug --allow-local-openai &
FREERIDE_INTEGRATION=1 go test . -v -count=1
```

Optional: `FREERIDE_TEST_URL=http://localhost:11434`

Live tests in `proxy_test.go` are skipped without `FREERIDE_INTEGRATION=1` so CI and offline runs stay fast. They may also skip if all cloud models are in cooldown (see `cooldowns.json`).

---

## License

MIT License.
