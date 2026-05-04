#!/bin/bash
# Wrapper for opencode in Gas Town sessions.

# Force opencode to route through the Freeride proxy
export OPENAI_BASE_URL="http://localhost:11434/v1"
export OPENAI_API_BASE="http://localhost:11434/v1"
export ANTHROPIC_BASE_URL="http://localhost:11434/v1"
export OPENAI_API_KEY="dummy"

# Resolve opencode binary path even when HOME is unset
if [ -z "${HOME:-}" ]; then
    HOME="$(getent passwd "$(id -u)" | cut -d: -f6)"
    export HOME
fi
OPENCODE_BIN="${HOME}/.opencode/bin/opencode"

# Parse arguments
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

# Extract prompt from positional args if not --prompt
if [ -z "$prompt" ] && [ ${#args[@]} -gt 0 ]; then
    last_idx=$((${#args[@]} - 1))
    last_arg="${args[$last_idx]}"
    if [[ "$last_arg" == *"[GAS TOWN]"* ]] || [[ "$last_arg" == *$'\n'* ]]; then
        prompt="$last_arg"
        unset 'args[$last_idx]'
    fi
fi

# Find available port
if [ -z "$port" ]; then
    for _ in $(seq 1 50); do
        candidate=$((40000 + RANDOM % 10000))
        if [ "$candidate" -ge 3306 ] && [ "$candidate" -le 3308 ]; then
            continue
        fi
        if ! ss -tln 2>/dev/null | grep -q ":$candidate "; then
            port=$candidate
            break
        fi
    done
    [ -z "$port" ] && port=$((40000 + RANDOM % 10000))
fi

# Build command WITH --prompt so opencode loads it at start
opencmd=("$OPENCODE_BIN" --port "$port" --model "openai/gpt-4o")
if [ -n "$prompt" ]; then
    opencmd+=(--prompt "$prompt")
fi
opencmd+=("${args[@]}")

# Try to auto-submit after startup (optional - doesn't need to work perfectly)
if [ -n "$prompt" ]; then
    (
        for _ in $(seq 1 30); do
            if curl -s "http://localhost:$port/config" > /dev/null 2>&1; then
                sleep 2  # Give opencode time to fully render
                if [ -n "$TMUX_PANE" ]; then
                    tmux send-keys -t "$TMUX_PANE" Enter
                else
                    curl -s -X POST "http://localhost:$port/tui/submit-prompt" \
                        -H "Content-Type: application/json" -d '{}' > /dev/null 2>&1
                fi
                break
            fi
            sleep 0.5
        done
    ) &
fi

exec "${opencmd[@]}"