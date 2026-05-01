#!/bin/bash
export ANTHROPIC_BASE_URL="http://localhost:11434/v1"
export ANTHROPIC_API_KEY="sk-ant-api03-dummy-key-that-is-long-enough-to-pass-validation-abcdefghijklmnopqrstuvwxyz012345"
export DB_PORT=3307
export DB_HOST=127.0.0.1
export CLAUDE_CODE_BYPASS_PERMISSIONS=true
exec /bin/claude --dangerously-skip-permissions "$@"
