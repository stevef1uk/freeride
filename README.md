# Freeride Proxy

This is a stand-alone, Ollama-compatible proxy that dynamically fetches and serves free models from OpenRouter using the `freeride` capability logic.

It runs locally on port `:11434` (Ollama's default port), intercepting requests to the OpenAI-compatible endpoint (`/v1/chat/completions`) and the Ollama native model listing endpoint (`/api/tags`). 

## Prerequisites

- Go 1.15+ (The proxy has been adapted to build cleanly with older Go versions, though a modern version is recommended).
- An OpenRouter API key.

## Building and Running

1. Build the proxy:
   ```bash
   go build
   ```

2. Run the proxy with your OpenRouter API key:
   ```bash
   export OPENROUTER_API_KEY="sk-or-v1-..."
   ./freeride
   ```

## Integration with Common AI Tools

Because Freeride automatically translates requests and strips out hardcoded model requirements, you can trick most popular AI coding assistants into using it as their backend for 100% free, auto-recovering inference.

### 1. Claude Code
**Status: Experimental (No Stream Support Yet)**
Claude Code strictly uses the Anthropic SDK. While Freeride intercepts the `/v1/messages` endpoint, **streaming is currently broken** because Claude Code expects Anthropic-formatted SSE events, while the proxy's fallback models return OpenAI format. 
```bash
export ANTHROPIC_BASE_URL="http://localhost:11434"
export ANTHROPIC_API_KEY="dummy_key"
claude
```

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

### 5. Gastown
**Status: Fully Supported**
Gastown agents can use this proxy as a cost-free backend for all model inference.
```bash
export ANTHROPIC_BASE_URL="http://localhost:11434"
export ANTHROPIC_API_KEY="dummy_key"
gt start
```

## Supported Endpoints

- `GET /api/tags`: Lists available free models from OpenRouter (formatted as Ollama tags).
- `GET /api/version`: Returns a dummy Ollama version for compatibility.
- `POST /v1/chat/completions`: Standard OpenAI chat endpoint.
- `POST /v1/responses`: Specialized OpenCode "Beads" protocol endpoint (with full SSE translation).
- `POST /v1/messages`: Anthropic Messages endpoint (currently non-streaming only).
- `GET /v1/models`: Returns the current ranked list of free models.

## Auto-Recovery & Cooldown

The proxy features transparent, proxy-level auto-recovery. 

If Gastown (or any CLI) makes a request to a free model and OpenRouter returns a rate limit (429) or server error (5xx), the proxy automatically intercepts the failure, places the failing model in cooldown using exponential backoff (1 min, 5 min, 25 min, up to 1 hour), and retries the exact same request using the next highest-ranked free model in the cache. 

This happens completely transparently without dropping the connection, ensuring agents never stall due to upstream free-tier limits!

**State Persistence**: The proxy saves all active cooldowns and error counts to a local `cooldowns.json` file. If the proxy is restarted, it will automatically reload this file so it remembers which models are still in the penalty box.
