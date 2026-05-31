#!/usr/bin/env bash
# ensure-gt-orchestrator-singleton.sh — stop duplicate "gt orchestrator run" processes.
# Multiple subscribers on gt.orchestrator.mcp cause empty fetch_task / workflow list races.
#
set -euo pipefail

count=0
if command -v pgrep >/dev/null 2>&1; then
  # pgrep -fc counts matching lines; use pattern that avoids matching pgrep itself
  count=$(pgrep -fc 'gt orchestrator run' 2>/dev/null || echo 0)
fi

if [[ "$count" -eq 0 ]]; then
  exit 0
fi

echo "WARN stopping ${count} stray gt orchestrator run process(es) before gt up" >&2
pkill -f 'gt orchestrator run' 2>/dev/null || true

deadline=$((SECONDS + 15))
while (( SECONDS < deadline )); do
  count=$(pgrep -fc 'gt orchestrator run' 2>/dev/null || echo 0)
  if [[ "$count" -eq 0 ]]; then
    echo "OK no stray orchestrator processes"
    exit 0
  fi
  sleep 1
done

echo "FAIL still have ${count} gt orchestrator run process(es) after pkill" >&2
exit 1
