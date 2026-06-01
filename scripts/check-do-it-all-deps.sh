#!/usr/bin/env bash
# check-do-it-all-deps.sh — fail fast before make do_it_all / e2e with install hints.
#
# Usage:
#   ./scripts/check-do-it-all-deps.sh
#   GT_CODEINDEX=0 ./scripts/check-do-it-all-deps.sh   # skip codeindex requirement
#
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
FREERIDE_ROOT="${FREERIDE_ROOT:-$(cd "$SCRIPT_DIR/.." && pwd)}"
FAIL=0

note_fail() {
  echo "" >&2
  echo "ERROR: $1" >&2
  echo "       $2" >&2
  FAIL=1
}

require_cmd() {
  local label="$1" cmd="$2" hint="$3"
  if ! command -v "$cmd" >/dev/null 2>&1; then
    note_fail "$label is not installed (missing \`$cmd\` on PATH)." "$hint"
    return 1
  fi
  return 0
}

echo "Checking host dependencies for make do_it_all..."

# --- Repo layout ---
if [[ ! -f "$FREERIDE_ROOT/gastown/go.mod" ]]; then
  note_fail "Gas Town submodule is empty." \
    "From the freeride repo: git submodule update --init --recursive
       Docs: https://github.com/stevef1uk/freeride#cloning-freeride-gas-town-submodule"
fi

if [[ ! -f "$FREERIDE_ROOT/.env" ]]; then
  note_fail "Missing .env with API keys." \
    "cp .env.template .env and set GEMINI_API_KEY, OPENROUTER_API_KEY, etc.
       See README.md → Building and Running → One-line Setup"
fi

# --- Required CLI tools ---
require_cmd "Git" git \
  "Install Git 2.25+: https://git-scm.com/downloads"

require_cmd "Go" go \
  "Install Go 1.18+: https://go.dev/dl/ (needed to build freeride and gastown)"

require_cmd "curl" curl \
  "Install curl (usually preinstalled on macOS/Linux)."

require_cmd "Python 3" python3 \
  "Install Python 3 for the ping_rig e2e (pytest): https://www.python.org/downloads/
       macOS: brew install python@3.12"

require_cmd "Dolt" dolt \
  "Install Dolt 1.82+ for beads storage: https://github.com/dolthub/dolt#installation
       macOS: brew install dolt"

# --- Docker (NATS via gt up) ---
if ! command -v docker >/dev/null 2>&1; then
  note_fail "Docker is not installed." \
    "Install Docker Desktop and ensure the daemon runs before make do_it_all.
       https://docs.docker.com/get-docker/"
elif ! docker info >/dev/null 2>&1; then
  note_fail "Docker is installed but the daemon is not running." \
    "Start Docker Desktop (macOS/Windows) or the docker service (Linux), then retry."
fi

# --- codeindex (rig-flow implement pre_run; disable with GT_CODEINDEX=0) ---
if [[ "${GT_CODEINDEX:-}" != "0" && "${CODEINDEX:-}" != "0" ]]; then
  if ! command -v codeindex >/dev/null 2>&1; then
    note_fail "codeindex CLI is not on PATH." \
      "pip install codeindex   # or: pipx install codeindex
       Verify: codeindex --help
       Project: https://github.com/scheidydude/codeindex
       README: $FREERIDE_ROOT/README.md → Polecat host tools → Codeindex
       To skip this check (not recommended for implement): export GT_CODEINDEX=0"
  elif ! codeindex --help >/dev/null 2>&1; then
    note_fail "codeindex is on PATH but \`codeindex --help\` failed." \
      "Reinstall: pip install --upgrade codeindex
       https://github.com/scheidydude/codeindex"
  fi
else
  echo "SKIP codeindex (GT_CODEINDEX=0 or CODEINDEX=0)"
fi

# --- bd: installed with gastown; warn only before make install ---
if ! command -v bd >/dev/null 2>&1; then
  echo "NOTE: beads CLI (bd) not on PATH yet — gastown \`make install\` will add gt/bd under ~/.local/bin."
  echo "      Ensure ~/.local/bin is on your PATH (see README → Cloning freeride)."
fi

if ! command -v gt >/dev/null 2>&1; then
  echo "NOTE: gt not on PATH yet — \`make do_it_all\` runs \`cd gastown && make install\` next."
fi

if (( FAIL != 0 )); then
  echo "" >&2
  echo "Fix the items above, then run:" >&2
  echo "  make do_it_all" >&2
  echo "" >&2
  echo "Full dependency table: README.md → Gas Town Integration → Dependencies" >&2
  exit 1
fi

echo "OK All checked host dependencies present."
exit 0
