#!/bin/bash
# Wrapper for opencode in Gas Town sessions.
# Translates --prompt into `opencode run` for auto-execution,
# then starts the persistent TUI so the session stays alive for nudges.

export OPENAI_BASE_URL="http://localhost:11434/v1"
export OPENAI_API_BASE="http://localhost:11434/v1"
export ANTHROPIC_BASE_URL="http://localhost:11434/v1"
export OPENAI_API_KEY="dummy"

args=()
prompt=""
i=1
while [ $i -le $# ]; do
    arg="${!i}"
    if [ "$arg" = "--prompt" ]; then
        i=$((i+1))
        prompt="${!i}"
    else
        args+=("$arg")
    fi
    i=$((i+1))
done

if [ -n "$prompt" ]; then
    # Auto-execute the prompt non-interactively first
    ~/.opencode/bin/opencode run "${args[@]}" "$prompt"
fi

# Start persistent TUI for nudges and interactive work
exec ~/.opencode/bin/opencode "${args[@]}"
