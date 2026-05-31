#!/usr/bin/env bash
# ensure-gt-orchestrator-singleton.sh — stop duplicate "gt orchestrator run" processes.
# Multiple subscribers on gt.orchestrator.mcp cause empty fetch_task / workflow list races.
# A single running orchestrator is left untouched (do not call this immediately after gt up).
#
set -euo pipefail

GT_ROOT="${GT_ROOT:-${GT_DIR:-$HOME/gt}}"

orchestrator_process_count() {
  local n=0
  if command -v pgrep >/dev/null 2>&1; then
    while IFS= read -r _; do
      n=$((n + 1))
    done < <(pgrep -f 'gt orchestrator run' 2>/dev/null || true)
  fi
  if [[ "$n" -eq 0 && -f "$GT_ROOT/daemon/orchestrator.pid" ]]; then
    local pid
    pid=$(tr -d '[:space:]' < "$GT_ROOT/daemon/orchestrator.pid" 2>/dev/null || echo "")
    if [[ -n "$pid" ]] && kill -0 "$pid" 2>/dev/null; then
      n=1
    fi
  fi
  echo "$n"
}

count=$(orchestrator_process_count)

if [[ "$count" -le 1 ]]; then
  exit 0
fi

echo "WARN stopping ${count} duplicate gt orchestrator run processes" >&2
pkill -f 'gt orchestrator run' 2>/dev/null || true

deadline=$((SECONDS + 15))
while (( SECONDS < deadline )); do
  count=$(orchestrator_process_count)
  if [[ "$count" -le 1 ]]; then
    echo "OK orchestrator singleton ($count process)"
    exit 0
  fi
  sleep 1
done

echo "FAIL still have ${count} gt orchestrator run processes after pkill" >&2
exit 1
