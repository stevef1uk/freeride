#!/bin/bash
# Wrapper for opencode in Gas Town sessions.
# Auto-submits the --prompt via the TUI server API so agents start working
# immediately without manual interaction.

# Force opencode to route through the Freeride proxy
export OPENAI_BASE_URL="http://localhost:11434/v1"
export OPENAI_API_BASE="http://localhost:11434/v1"
export ANTHROPIC_BASE_URL="http://localhost:11434/v1"
export OPENAI_API_KEY="dummy"

# Resolve opencode binary path even when HOME is unset (tmux/gt env quirk)
if [ -z "${HOME:-}" ]; then
    HOME="$(getent passwd "$(id -u)" | cut -d: -f6)"
    export HOME
fi
OPENCODE_BIN="${HOME}/.opencode/bin/opencode"

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

# If no port specified, find an available ephemeral port
# Avoid ports 3306-3308 (Dolt/MySQL) and check availability
if [ -z "$port" ]; then
    for _ in $(seq 1 50); do
        candidate=$((40000 + RANDOM % 10000))
        # Skip known Dolt ports and check if port is free
        if [ "$candidate" -ge 3306 ] && [ "$candidate" -le 3308 ]; then
            continue
        fi
        if ! ss -tln 2>/dev/null | grep -q ":$candidate "; then
            port=$candidate
            break
        fi
    done
    # Fallback if all checked ports were busy
    if [ -z "$port" ]; then
        port=$((40000 + RANDOM % 10000))
    fi
fi

# Build the opencode command with the selected port
opencmd=("$OPENCODE_BIN" --port "$port" "${args[@]}")

if [ -n "$prompt" ]; then
    # Include the prompt flag so opencode pre-fills it
    opencmd+=(--prompt "$prompt")
fi

# If we have a prompt, auto-submit it once the TUI server is ready.
# We double-fork so the helper is orphaned and re-parented to init,
# preventing zombies when this shell execs opencode.
if [ -n "$prompt" ]; then
    (
        (
            # Wait up to 15 seconds for the server to come up
            for _ in $(seq 1 30); do
                if curl -s "http://localhost:$port/config" > /dev/null 2>&1; then
                    # 1. Try the API first (cleanest)
                    curl -s -X POST "http://localhost:$port/tui/submit-prompt" \
                        -H "Content-Type: application/json" -d '{}' > /dev/null 2>&1

                    # 2. Force a UI "nudge" via tmux if we are in a session
                    # We send Escape to clear any startup "New Model" modals, then Enter to submit
                    if [ -n "$TMUX_PANE" ]; then
                        sleep 1 # Give the UI a moment to render after the API check
                        tmux send-keys -t "$TMUX_PANE" Escape Escape Enter > /dev/null 2>&1
                    fi
                    break
                fi
                sleep 0.5
            done
        ) &
        exit 0
    ) > /dev/null 2>&1 &
fi

# Use exec to replace the shell with opencode so tmux liveness checks work
exec "${opencmd[@]}"
