# Freeride Proxy (v1.1.0)

This is a stand-alone, Ollama-compatible proxy that dynamically fetches and serves free models from both **OpenRouter** and **NVIDIA (NIM)** using the `freeride` capability logic.

It runs locally on port `:11434` (Ollama's default port), intercepting requests to the OpenAI-compatible endpoint (`/v1/chat/completions`) and the Ollama native model listing endpoint (`/api/tags`). 

## Latest Changes (v1.1.0)

- **Direct NVIDIA NIM Integration**: Now supports direct, zero-cost routing to `integrate.api.nvidia.com` for high-performance partner models (Meta Llama, Mistral, Google Gemma, etc.).
- **Strict Cost Optimization**: Eliminated unintended paid fallbacks. If free models are exhausted or in cooldown, the proxy refuses to route to paid models, returning a 503 error instead to protect your credits.
- **Improved Prefix Handling**: Automatically manages model prefixes (e.g., `meta/`, `google/`) for direct API compatibility, ensuring requests to partner models route correctly.
- **Proxy-Magic (Resilient Tool-Use)**: Implemented a fallback translation layer that intercepts conversational command mentions (e.g., "I will now run `gt hook`") and markdown code blocks. These are automatically converted into official tool calls, enabling autonomous tool-use for free-tier models (Mixtral/Llama) that fail to adhere to the standard JSON tool API.
- **Robust Tiered Selection**:
  - **Tier 1**: Prioritizes the original requested model if it is confirmed free.
  - **Tier 2**: Falls back to tool-capable NVIDIA NIM models (highly reliable and fast).
  - **Tier 3**: Falls back to reliable OpenRouter free models.

## Prerequisites

- Go 1.18+ (The proxy builds cleanly on modern Linux/macOS environments).
- A **.env file** or environment variables for your API keys.
- An **OpenRouter API key**.
- An **NVIDIA API key** (Required for the highest performance free models).

## CLI Configuration

The proxy supports the following command-line flags:

- `--debug`: Enables verbose logging of requests, internal routing decisions, and API responses.
- `--allow-paid`: (Disabled by default) Allows the proxy to use paid models (e.g., Claude 3.5 Sonnet) for complex tasks or as a fallback. Without this flag, the proxy operates in **Strict Zero-Cost Mode**.

## Building and Running

1. Build the proxy:
   ```bash
   go build -o freeride main.go
   ```

2. Configure your keys:
   Create a `.env` file in the project root:
   ```env
   OPENROUTER_API_KEY=sk-or-v1-...
   NVIDIA_API_KEY=nvapi-...
   ```

3. Run the proxy:
   ```bash
   ./freeride --debug > freeride_live.log 2>&1 &
   ```

## Testing

A comprehensive integration test suite is included in `proxy_test.go`. It validates:
- **SSE Streaming**: Full protocol translation for Beads and Anthropic.
- **Tool Translation**: JSON schema mapping and tool-use lifecycle.
- **Model Discovery**: Verifies that models from both OpenRouter and NVIDIA are correctly fetched, cached, and routable.

To run the tests:
```bash
go test -v proxy_test.go main.go
```

## Integration with Common AI Tools

### 1. Claude Code
**Status: Fully Supported (with Streaming and Tool-Use)**

Claude Code works perfectly with Freeride by translating Anthropic's Messages API into standard OpenAI chat completions.

#### Quick Start
1. **Bypass Subscription**: Mark onboarding as complete in `~/.claude.json`:
   ```json
   {
     "hasCompletedOnboarding": true,
     "authMethod": "console"
   }
   ```
2. **Set Environment**:
   ```bash
   export ANTHROPIC_BASE_URL="http://localhost:11434"
   export ANTHROPIC_API_KEY="sk-ant-api03-dummy-key-that-is-long-enough-to-pass-validation-abcdefghijklmnopqrstuvwxyz012345"
   ```
3. **Run in Autonomous Mode**:
   ```bash
   claude --dangerously-skip-permissions
   ```

### 2. OpenCode
**Status: Fully Supported**
Point your compatibility variables to the local proxy:
```bash
export OPENAI_BASE_URL="http://localhost:11434/v1"
export OPENAI_API_KEY="dummy_key"
```

## Gas Town Integration

**Freeride works as the LLM backend for Gas Town agents** (Claude, OpenCode, Codex, etc.). The proxy runs on `:11434` and forwards traffic to free models from OpenRouter and NVIDIA NIM.

### Configuration

#### 1. Environment Variables

Before running any `gt` commands that launch agents, **set these in your parent shell** so Gastown passes them through to the agent processes:

```bash
export OPENAI_BASE_URL="http://localhost:11434/v1"
export OPENAI_API_BASE="http://localhost:11434/v1"
export ANTHROPIC_BASE_URL="http://localhost:11434/v1"
export OPENAI_API_KEY="dummy"
export ANTHROPIC_API_KEY="sk-ant-api03-dummy-key-that-is-long-enough-to-pass-validation-abcdefghijklmnopqrstuvwxyz012345"
```

You can persist these in your shell profile (`.bashrc`, `.zshrc`) or source them from a `.env` file before working with Gas Town.

#### 2. GasTown Agent Configuration

Create or update `gt/settings/config.json` in your project root:

```json
{
  "type": "town-settings",
  "version": 1,
  "default_agent": "opencode",
  "crew_agents": {
    "steve": "opencode"
  },
  "agents": {
    "opencode": {
      "command": "/home/stevef/dev/freeride/opencode_wrapper.sh",
      "args": ["--model", "openai/gpt-4o"],
      "env": {
        "OPENAI_BASE_URL": "http://localhost:11434/v1",
        "OPENAI_API_BASE": "http://localhost:11434/v1",
        "ANTHROPIC_BASE_URL": "http://localhost:11434/v1",
        "OPENAI_API_KEY": "dummy",
        "OPENCODE_PERMISSION": "{\"*\":\"allow\"}"
      },
      "tmux": {
        "process_names": ["opencode"]
      }
    }
  }
}
```

**Key settings:**
- `default_agent`: Set to `"opencode"` to use OpenCode as the default
- `command`: Path to the `opencode_wrapper.sh` script
- `args`: Pass `--model openai/gpt-4o` to enable full tool support (the proxy maps this to a free model)
- `env`: These variables override any global settings and ensure routing through Freeride

#### 3. Wrapper Scripts

Two convenience wrappers are included for direct agent usage outside of Gas Town:

- **`claude_wrapper.sh`** — Launches Claude Code pointing at Freeride
- **`opencode_wrapper.sh`** — Launches OpenCode pointing at Freeride with auto-prompt submission

The `opencode_wrapper.sh` script:
- Automatically selects a random ephemeral port for the TUI server
- Starts opencode in the background
- Auto-submits prompts via the TUI server API so agents start working immediately
- Waits for opencode to finish, keeping the tmux session alive

Usage:
```bash
./claude_wrapper.sh
./opencode_wrapper.sh --prompt "your prompt here"
```

### Verification

#### Check the proxy is running:
```bash
curl -s http://localhost:11434/v1/models | head -5
```
You should see a JSON list of available models.

#### Check requests are routing through the proxy:
```bash
# Monitor the proxy log in real-time
tail -f freeride_live.log | grep -E "Attempting|succeeded|ERROR"
```

#### Check the opencode process environment:
```bash
# Find opencode PID
pgrep -f "opencode"

# Check its environment variables
cat /proc/<PID>/environ | tr '\0' '\n' | grep -E "BASE_URL|API_KEY"
```

You should see:
```
OPENAI_BASE_URL=http://localhost:11434/v1
OPENAI_API_KEY=dummy
```

### Troubleshooting

| Symptom | Cause | Fix |
|:---|:---|:---|
| `User not found` (401) | Invalid OpenRouter key | Generate a new key at openrouter.ai/settings/keys |
| `Insufficient credits` (402) | OpenRouter balance is $0 | Add credits at openrouter.ai/settings/credits |
| `Rate limited` (429) | Free model overloaded | Wait 10s or use NVIDIA models only |
| `No models available` | All models in cooldown | Check `cooldowns.json` and restart proxy |
| `404 Not Found` | Model no longer exists | Update `models.yaml` to remove deprecated models |
| Agent timing out during startup | Wrapper script blocking | Use `exec` in your wrapper to replace the shell process |
| Proxy not responding | Freeride not running | Run `./freeride --debug > freeride_live.log 2>&1 &` |

### Model Configuration

Edit `models.yaml` to control which models the proxy uses:

```yaml
# Freeride Model Configuration

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
```

**Verified working models (NVIDIA):**
- `meta/llama-3.3-70b-instruct` ✅
- `meta/llama-3.2-11b-vision-instruct` ✅

**Note:** The proxy auto-discovers models from both OpenRouter and NVIDIA, but the `models.yaml` config prioritizes the listed models.

## Power Model Spoofing (Tool Support)

Freeride automatically advertises `claude-3-5-sonnet-20241022` and `gpt-4o` at the top of its model list. **Always select these names** in your client configuration. Even though the proxy will route to a **free model** (like Llama 3.3 70B), using these names tricks clients into enabling their full suite of tools which are normally disabled for smaller models.

## Auto-Recovery & Cooldown

The proxy features transparent, proxy-level auto-recovery. If a request to a free model returns a rate limit (429) or server error (5xx), the proxy automatically intercepts the failure, places the failing model in cooldown, and retries the exact same request using the next highest-ranked free model.

**State Persistence**: Cooldowns are saved to `cooldowns.json` and persist across restarts.
+
+## Proxy-Magic (Resilient Tool-Use)
+
+Free models (like Llama 3.3 or Mixtral) often struggle with strict JSON-based tool calling, frequently choosing to "talk about" running a command instead of actually triggering the tool.
+
+Freeride solves this by implementing **Proxy-Magic**:
+1. **Markdown Extraction**: If a model returns a markdown bash block (```bash ... ```), the proxy automatically converts it into a `run_terminal_command` tool call.
+2. **Conversational Extraction**: The proxy uses aggressive regex to catch conversational intent, such as:
+   - "I will now run `gt hook`"
+   - "I'm going to run `bd list`"
+3. **Deduplication**: If a model both talks about and uses the tool, the proxy deduplicates the requests to prevent double-execution.
+
+This mechanism ensures that the **Gas Town** agent ecosystem remains fully autonomous even when running on zero-cost models.

## Supported Endpoints

- `GET /api/tags`: Lists available free models (formatted as Ollama tags).
- `POST /v1/chat/completions`: Standard OpenAI chat endpoint.
- `POST /v1/messages`: Anthropic Messages endpoint (with full SSE translation).
- `GET /v1/models`: Returns the current ranked list of all available free models.

## License
MIT License.
