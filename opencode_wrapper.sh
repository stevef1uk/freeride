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

# Start opencode in the background
"${opencmd[@]}" &
OPID=$!

# If we have a prompt, auto-submit it once the TUI server is ready
if [ -n "$prompt" ]; then
    # Wait up to 15 seconds for the server to come up
    for _ in $(seq 1 30); do
        if curl -s "http://localhost:$port/config" > /dev/null 2>&1; then
            curl -s -X POST "http://localhost:$port/tui/submit-prompt" \
                -H "Content-Type: application/json" -d '{}' > /dev/null 2>&1
            break
        fi
        sleep 0.5
    done
fi

# Wait for opencode to finish (keeps the tmux session alive)
wait $OPID
