# Freeride Proxy (v1.3.0)

A stand-alone, Ollama-compatible proxy that dynamically fetches and serves free models from **Cerebras**, **OpenRouter**, **NVIDIA (NIM)**, and **Ollama Cloud**. Runs locally on port `:11434`, intercepting requests to the OpenAI-compatible endpoint (`/v1/chat/completions`) and the Ollama native model listing endpoint (`/api/tags`).

Use it **standalone** with any OpenAI-compatible client, or as the LLM backend for [Gas Town](https://github.com/gastownhall/gastown) multi-agent orchestration.

---

## What's New (v1.3.0)

- **Cloud-first local GPU**: With `--allow-local-openai`, capable cloud models (70B NVIDIA, OpenRouter free tiers, large Cerebras, etc.) are tried first; `localOpenAI` (llama-server on `:8080`) is **last-resort fallback** only when cloud routes fail or are in cooldown.
- **Weak-cloud blocking**: `blockSmallCloudWhenLocalGPU` in `models.yaml` skips nano/mini/8B cloud models when local GPU mode is on — so fallback does not mean “downgrade to 8B.”
- **Config-only model IDs**: Routing lists, role prepends, compat model stubs, and score boosts live in `models.yaml` — not hardcoded in Go.
- **Request sanitization**: Anthropic `tools` / `system` payloads are normalized before upstream forwarding (`sanitizeBody`).
- **Faster default tests**: `go test .` runs unit and in-process httptest suites; live proxy tests require `FREERIDE_INTEGRATION=1`.

Earlier (v1.2.0): headless `gt-agent`, NATS transport, Proxy-Magic tool extraction, strict zero-cost mode (503 when free tier exhausted).

### Routing order (summary)

1. Cerebras budget / performance (from `models.yaml`)
2. Role prepends (`rolePrepend`, when `--allow-paid`)
3. Reliable free + NVIDIA lists, Ollama cloud, original model
4. Curated paid (`curatedPaid`, when `--allow-paid` + complex)
5. IDE bridges (`--allow-ide`)
6. **Local llama-server** (`localOpenAI`, `--allow-local-openai`) — **after** capable cloud

---

## Prerequisites

- Go 1.18+ (for building from source)
- A **Cerebras API key** (Optional, for fastest inference)
- An **OpenRouter API key** (for free OpenRouter models)
- An **NVIDIA API key** (for highest-performance free NIM models)
- An **Ollama API key** (for Ollama Cloud models like Qwen3 480B and DeepSeek V4)

## Building and Running

### Standalone Use

1. Build:
   ```bash
   go build -o freeride .
   ```

2. Configure API keys in a `.env` file in the directory where you start Freeride:
   ```bash
   cp .env.template .env
   # edit .env — set keys for the providers you use in models.yaml
   ```
   ```env
   OPENROUTER_API_KEY=sk-or-v1-...
   NVIDIA_API_KEY=nvapi-...
   OLLAMA_API_KEY=1b18...
   CEREBRAS_API_KEY=csk-...
   ```
   On startup, Freeride reads `.env` from the **current working directory** (`KEY=value` lines; `#` comments are ignored). You can use shell exports instead if you prefer.

3. Run (add `--allow-local-openai` if you use `localOpenAI` in `models.yaml`):
   ```bash
   ./freeride --debug --allow-local-openai > freeride_live.log 2>&1 &
   ```

4. Test:
   ```bash
   curl -s http://localhost:11434/v1/models | head -5
   ```

### CLI Flags

- `--debug`: Verbose logging of requests, routing decisions, and API responses.
- `--allow-paid`: Allows paid models as fallback. **Disabled by default** (strict zero-cost mode).
- `--allow-ide`: Allows `ideModels` entries in `models.yaml` (local IDE bridges) as a last-resort fallback. **Disabled by default**.
- `--allow-local-openai`: Enables `localOpenAI` fallback (after capable cloud) and applies `blockSmallCloudWhenLocalGPU` from `models.yaml`. **Disabled by default**. Does **not** force local over cloud — keep Gas Town `LLM_MODEL` on a strong cloud id (e.g. `meta/llama-3.3-70b-instruct`).

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

**Freeride `.env`:** Gas Town `gt-agent` sessions only need `LLM_ENDPOINT` / `LLM_MODEL` in `settings/config.json` (see below). Provider API keys live in **Freeride’s** `.env` — start the proxy from your Freeride repo (or any directory that contains a `.env` with `OPENROUTER_API_KEY`, `NVIDIA_API_KEY`, etc.). `gt install` does not create this file; copy `.env.template` → `.env` in the Freeride tree before `gt up`.

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
                                         │  OpenRouter/NIM │
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
    "polecat": "gt-agent-local",
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
    }
  }
}
```

**Key settings:**
- `session_transport`: Set to `"nats"` for NATS-based session management (no tmux required)
- `default_agent`: `"gt-agent-local"` for headless automated work
- `role_agents.crew`: `"opencode"` for interactive TUI-based crew members
- `gt-agent-local.env.LLM_ENDPOINT`: Points directly to Freeride's OpenAI-compatible endpoint

### Running Gas Town

```bash
# 0. Ensure Freeride has API keys (.env in the directory you start it from)
cp .env.template .env   # once, in the freeride repo; edit with your keys

# 1. Ensure Freeride proxy is running (from that repo so .env is loaded)
./freeride --debug > freeride_live.log 2>&1 &

# 2. Ensure NATS is available (Docker or standalone)
# Docker: docker run -d --name gt-nats -p 4222:4222 nats:latest

# 3. Start Gas Town services
gt up

# 4. Check status
gt status

# 5. Assign work to a polecat (automatic via Mayor, or manual)
gt hook de-123 defender/polecats/obsidian
```

### Verification

#### Check the proxy is running:
```bash
curl -s http://localhost:11434/v1/models | head -5
```

#### Check requests are routing through the proxy:
```bash
tail -f freeride_live.log | grep -E "Attempting|succeeded|ERROR"
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

# Priority 1: Specifically requested reliable free models
reliableFree:
  - "google/gemini-2.0-flash-exp:free"
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

The proxy auto-discovers models from OpenRouter, NVIDIA, and Ollama Cloud, but `models.yaml` prioritizes the listed models. Optional **`localOpenAI`** backends are not used unless you pass **`--allow-local-openai`**.

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
go test . -v -count=1
```

Runs in milliseconds:
- **`candidate_test.go`** — cloud-first candidate order, local last, weak-cloud blocking
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
