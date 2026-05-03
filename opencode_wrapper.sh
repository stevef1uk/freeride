#!/bin/bash
# Wrapper for opencode in Gas Town sessions.
# Auto-submits the --prompt via the TUI server API so agents start working
# immediately without manual interaction.

export OPENAI_BASE_URL="http://localhost:11434/v1"
export OPENAI_API_BASE="http://localhost:11434/v1"
export ANTHROPIC_BASE_URL="http://localhost:11434/v1"
export OPENAI_API_KEY="dummy"

args=()
prompt=""
port=""
i=1
while [ $i -le $# ]; do
    arg="${!i}"
    if [ "$arg" = "--prompt" ]; then
        i=$((i+1))
        prompt="${!i}"
    elif [ "$arg" = "--port" ]; then
        i=$((i+1))
        port="${!i}"
    else
        args+=("$arg")
    fi
    i=$((i+1))
done

# Gas Town passes the prompt as a positional argument (PromptMode: "arg"),
# but opencode requires --prompt flag. Detect if the last positional arg
# looks like a Gas Town prompt and move it to --prompt.
if [ -z "$prompt" ] && [ ${#args[@]} -gt 0 ]; then
    last_idx=$((${#args[@]} - 1))
    last_arg="${args[$last_idx]}"
    # Gas Town prompts contain "[GAS TOWN]" or newlines
    if [[ "$last_arg" == *"[GAS TOWN]"* ]] || [[ "$last_arg" == *$'\n'* ]]; then
        prompt="$last_arg"
        unset 'args[$last_idx]'
    fi
fi

# If no port specified, pick a random ephemeral port
if [ -z "$port" ]; then
    port=$((40000 + RANDOM % 10000))
fi

# Build the opencode command with the selected port
opencmd=(~/.opencode/bin/opencode --port "$port" "${args[@]}")

if [ -n "$prompt" ]; then
    # Include the prompt flag so opencode pre-fills it
    opencmd+=(--prompt "$prompt")
fi

# If we have a prompt, auto-submit it once the TUI server is ready in the background
if [ -n "$prompt" ]; then
    (
        # Wait up to 15 seconds for the server to come up
        for _ in $(seq 1 30); do
            if curl -s "http://localhost:$port/config" > /dev/null 2>&1; then
                curl -s -X POST "http://localhost:$port/tui/submit-prompt" \
                    -H "Content-Type: application/json" -d '{}' > /dev/null 2>&1
                break
            fi
            sleep 0.5
        done
    ) &
fi

# Use exec to replace the shell with opencode so tmux liveness checks work
exec "${opencmd[@]}"
