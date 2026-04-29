# Freeride Proxy

This is a stand-alone, Ollama-compatible proxy that dynamically fetches and serves free models from both **OpenRouter** and **NVIDIA (NIM)** using the `freeride` capability logic.

It runs locally on port `:11434` (Ollama's default port), intercepting requests to the OpenAI-compatible endpoint (`/v1/chat/completions`) and the Ollama native model listing endpoint (`/api/tags`). 

## Prerequisites

- Go 1.18+ (The proxy has been adapted to build cleanly with older Go versions, though a modern version is recommended).
- A **.env file** or environment variables for your API keys.
- An **OpenRouter API key**.
- An **NVIDIA API key** (optional, but highly recommended for accessing high-performance NIM models).

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

Because Freeride automatically translates requests and strips out hardcoded model requirements, you can trick most popular AI coding assistants into using it as their backend for 100% free, auto-recovering inference.

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
   Always use the skip-permissions flag to allow agents to work without manual approval in the background:
   ```bash
   claude --dangerously-skip-permissions
   ```

#### Key Translation Features
- **SSE Translation**: Converts OpenAI streams into Anthropic events (`message_start`, `content_block_delta`).
- **Tool Translation**: Maps Anthropic's `input_schema` to OpenAI's `parameters` and handles `tool_use`/`tool_result` translation in conversation history.
- **Path Routing**: Automatically handles both `/v1/messages` and the redundant `/v1/v1/messages` paths.

#### Troubleshooting
- **Hanging/Planning Only**: Ensure your proxy is updated to the latest version (v1.0.5+) which includes the `content_block_stop` fix.
- **Undefined Map Errors**: Ensure `ANTHROPIC_BASE_URL` does **not** end with `/v1`.

### 2. OpenCode
**Status: Fully Supported**
OpenCode uses a proprietary SSE protocol (Beads). Freeride has been specifically updated to translate standard OpenAI streams into the Beads format (`event: response.output_text.delta`).
```bash
export OPENAI_BASE_URL="http://localhost:11434/v1"
export OPENAI_API_KEY="dummy_key"
opencode run "Hello"
```

### 3. Antigravity
**Status: Fully Supported**
Antigravity works perfectly with Freeride by pointing its compatibility variables to the local proxy:
```bash
export OPENAI_BASE_URL="http://localhost:11434/v1"
export OPENAI_API_KEY="dummy_key"
```

### 4. GitHub Copilot (VS Code)
**Status: Not Supported / Unstable**
GitHub Copilot is highly proprietary and uses internal authentication that often rejects local proxy overrides. We recommend using the **Continue.dev** VS Code extension instead, which natively supports `http://localhost:11434/v1` as an OpenAI provider!

### 5. GasTown (via Freeride Proxy)

**Configure GT to use opencode by default:**

```bash
cd /path/to/rig
gt config default-agent opencode
```

**Start the Mayor:**

```bash
gt mayor start      # Uses default_agent from config
gt mayor attach    # Attach to session
```

Or explicitly specify the agent:
```bash
gt mayor start --agent claude
```

### 6. Beads & Dolt Server Optimization

To prevent timeouts and "database locked" errors when running multiple agents (Mayor, Deacon, Witness, Dashboard) in parallel, always use a **centralized Dolt server** instead of the default embedded mode.

1. **Start the Dolt Server** in your Town root:
   ```bash
   cd gt
   mkdir -p .dolt-data
   cd .dolt-data
   dolt sql-server -H 127.0.0.1 -P 3307 &
   ```

2. **Configure Beads** to use the server:
   Run these commands in every Rig and in the Town root:
   ```bash
   bd config set dolt.port 3307
   bd config set dolt.address 127.0.0.1
   bd config set dolt.auto-start false
   bd config set dolt.idle-timeout 0
   ```

3. **Multi-Rig Routing**: If using multiple rigs on one server, specify the database name for each rig:
   ```bash
   bd config set dolt.database <rig_name>
   ```

### 7. Troubleshooting: GT_TOWN_ROOT

If you move your GasTown directory or create a new one, your shell or tmux session might still be pointing to the old one. Always verify your root:
```bash
export GT_TOWN_ROOT=/path/to/your/gt
gt daemon stop
gt daemon start
```

### 8. Hard Reset / Restoration

If your GasTown becomes corrupted or you delete the metadata:
1. **Re-install**: `gt install --force` in the Town root.
2. **Re-initialize Beads**: `bd init --force --prefix gt` (follow prompts).
3. **Reconnect Server**: Re-run the `bd config set dolt.port 3307` commands.
4. **Restore Agents**: `gt doctor --fix` will recreate missing agent beads.

### 9. Power Model Spoofing (Tool Support)

Freeride automatically advertises `claude-3-5-sonnet-20241022` and `gpt-4o` at the top of its model list. **Always select these names** in your client configuration. Even though the proxy will route to a **free model** (like Llama 3.1 70B), using these names tricks Claude Code into enabling its full suite of tools (Write, Edit, Task, etc.) which are normally disabled for smaller models.

## Supported Endpoints

- `GET /api/tags`: Lists available free models from OpenRouter and NVIDIA (formatted as Ollama tags).
- `GET /api/version`: Returns a dummy Ollama version for compatibility.
- `POST /v1/chat/completions`: Standard OpenAI chat endpoint.
- `POST /v1/responses`: Specialized OpenCode "Beads" protocol endpoint (with full SSE translation).
- `POST /v1/messages` & `POST /api/v1/messages`: Anthropic Messages endpoint (with full SSE streaming translation).
- `GET /v1/models`: Returns the current ranked list of all available free models.

## Auto-Recovery & Cooldown

The proxy features transparent, proxy-level auto-recovery across multiple providers. 

If Gastown (or any CLI) makes a request to a free model and either OpenRouter or NVIDIA returns a rate limit (429) or server error (5xx), the proxy automatically intercepts the failure, places the failing model in cooldown using exponential backoff (1 min, 5 min, 25 min, up to 1 hour), and retries the exact same request using the next highest-ranked free model in the combined cache. 

This happens completely transparently without dropping the connection, ensuring agents never stall due to upstream free-tier limits!

**State Persistence**: The proxy saves all active cooldowns and error counts to a local `cooldowns.json` file. If the proxy is restarted, it will automatically reload this file so it remembers which models are still in the penalty box.

## Advanced Sanitization & Ranking

Freeride does more than just proxy; it actively "fixes" requests to ensure they work on free models:

- **Schema Translation**: Automatically converts complex tool schemas (like those from the Gastown Responses API) into standard OpenAI function calls.
- **Payload Cleaning**: Strips Anthropic-specific metadata, flattens content blocks, and converts `system` parameters into system messages.
- **Safety Caps**: Automatically caps `max_tokens` at 4096 and strips unsupported parameters to prevent 400 errors from upstream providers.

## License
This project is licensed under the MIT License - see the [LICENSE](LICENSE) file for details.

- **Intelligent Ranking**: Models are scored based on context length, tool support, and recency. Highly reliable models like **Gemini 2.0 Flash** receive a massive boost to ensure they are used first.
