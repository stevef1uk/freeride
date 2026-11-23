#!/usr/bin/env bash
#
# Post-reset HQ healer (written by clean-gastown.sh / Fix #101).
#
# Run AFTER `gt up` has brought the Dolt server back online. Seeds the
# `hq.config.issue_prefix` row so HQ-bound mail sends (mayor inbox,
# planner inbox, etc.) no longer fail with "database not initialized".
#
# Safe to run multiple times: the INSERT IGNORE is a no-op if the row
# is already present.
set -euo pipefail

DOLT_HOST="${DOLT_HOST:-127.0.0.1}"
DOLT_PORT="${DOLT_PORT:-3307}"
HQ_PREFIX="${HQ_PREFIX:-hq}"

if ! command -v dolt >/dev/null 2>&1; then
    echo "[FATAL] dolt CLI not found in PATH" >&2
    exit 1
fi

if ! DOLT_CLI_PASSWORD="" dolt --host "$DOLT_HOST" --port "$DOLT_PORT" \
        --user root --no-tls sql -q "SELECT 1;" >/dev/null 2>&1; then
    echo "[FATAL] Dolt server not reachable on ${DOLT_HOST}:${DOLT_PORT}." >&2
    echo "         Run 'gt up' (or 'gt dolt start') first, then re-run me." >&2
    exit 1
fi

DOLT_CLI_PASSWORD="" dolt --host "$DOLT_HOST" --port "$DOLT_PORT" \
    --user root --no-tls sql -q "
USE hq;
INSERT IGNORE INTO config (\`key\`, value) VALUES ('issue_prefix', '${HQ_PREFIX}');
SELECT 'issue_prefix' AS \`key\`, value FROM config WHERE \`key\`='issue_prefix';
" 2>&1

echo "✓ HQ issue_prefix seeded (${HQ_PREFIX}) — gt mail send mayor/ should now work."
