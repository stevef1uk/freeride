#!/usr/bin/env bash
# wait-for-gt-stack.sh — poll Freeride, Dolt, NATS (gt-nats), and optionally orchestrator.
# :11434 is Freeride's Ollama-compatible API port, not the Ollama desktop app.
#
# Usage:
#   ./scripts/wait-for-gt-stack.sh                 # Freeride + Dolt + NATS healthz
#   ./scripts/wait-for-gt-stack.sh --freeride-only # before gastown build / gt up
#   ./scripts/wait-for-gt-stack.sh --with-orchestrator
#       # after gt up: also require exactly one orchestrator and gt mayor workflow list
#   WAIT_TIMEOUT_SEC=180 ./scripts/wait-for-gt-stack.sh
#
set -euo pipefail

FREERIDE_URL="${FREERIDE_URL:-http://127.0.0.1:11434/v1/models}"
DOLT_HOST="${DOLT_HOST:-127.0.0.1}"
DOLT_PORT="${DOLT_PORT:-${GT_DOLT_PORT:-3307}}"
NATS_HOST="${NATS_HOST:-127.0.0.1}"
NATS_PORT="${NATS_PORT:-4222}"
NATS_MONITOR_PORT="${NATS_MONITOR_PORT:-8222}"
GT_ROOT="${GT_ROOT:-${GT_DIR:-$HOME/gt}}"
TIMEOUT_SEC="${WAIT_TIMEOUT_SEC:-120}"

WAIT_FREERIDE=1
WAIT_DOLT=1
WAIT_NATS=1
WAIT_ORCHESTRATOR=0

for arg in "$@"; do
  case "$arg" in
    --freeride-only)
      WAIT_DOLT=0
      WAIT_NATS=0
      WAIT_ORCHESTRATOR=0
      ;;
    --no-freeride)
      WAIT_FREERIDE=0
      ;;
    --with-orchestrator)
      WAIT_ORCHESTRATOR=1
      ;;
    -h|--help)
      sed -n '2,14p' "$0"
      exit 0
      ;;
    *)
      echo "Unknown option: $arg" >&2
      exit 2
      ;;
  esac
done

tcp_open() {
  local host="$1" port="$2"
  if command -v nc >/dev/null 2>&1; then
    nc -z "$host" "$port" 2>/dev/null
    return $?
  fi
  (echo >/dev/tcp/"$host"/"$port") 2>/dev/null
}

wait_for() {
  local label="$1"
  local timeout="$2"
  shift 2
  local start=$SECONDS
  while (( SECONDS - start < timeout )); do
    if "$@"; then
      echo "OK $label ready ($((SECONDS - start))s)"
      return 0
    fi
    sleep 1
  done
  echo "FAIL $label not ready after ${timeout}s" >&2
  return 1
}

freeride_ready() {
  curl -sf --max-time 3 "$FREERIDE_URL" >/dev/null 2>&1
}

dolt_ready() {
  tcp_open "$DOLT_HOST" "$DOLT_PORT"
}

nats_ready() {
  tcp_open "$NATS_HOST" "$NATS_PORT" || return 1
  # gt-nats publishes the monitoring port; TCP-only checks miss a half-started container
  curl -sf --max-time 3 "http://${NATS_HOST}:${NATS_MONITOR_PORT}/healthz" 2>/dev/null \
    | grep -q '"status":"ok"'
}

orchestrator_ready() {
  local count=0
  if command -v pgrep >/dev/null 2>&1; then
    count=$(pgrep -fc 'gt orchestrator run' 2>/dev/null || echo 0)
  fi
  if [[ "$count" -ne 1 ]]; then
    return 1
  fi
  if [[ ! -d "$GT_ROOT" ]] || ! command -v gt >/dev/null 2>&1; then
    return 0
  fi
  (cd "$GT_ROOT" && gt mayor workflow list >/dev/null 2>&1)
}

failed=0

if [[ "$WAIT_FREERIDE" -eq 1 ]]; then
  wait_for "Freeride ($FREERIDE_URL)" "$TIMEOUT_SEC" freeride_ready || failed=1
fi
if [[ "$WAIT_DOLT" -eq 1 ]]; then
  wait_for "Dolt ($DOLT_HOST:$DOLT_PORT)" "$TIMEOUT_SEC" dolt_ready || failed=1
fi
if [[ "$WAIT_NATS" -eq 1 ]]; then
  wait_for "NATS ($NATS_HOST:$NATS_PORT + :$NATS_MONITOR_PORT healthz)" "$TIMEOUT_SEC" nats_ready || failed=1
fi
if [[ "$WAIT_ORCHESTRATOR" -eq 1 ]]; then
  wait_for "Orchestrator (1 process + gt mayor workflow list)" "$TIMEOUT_SEC" orchestrator_ready || failed=1
fi

exit "$failed"
