#!/usr/bin/env bash
# Simple curl test against the Freeride k3s deployment (or local proxy).
# Usage:
#   ./k3s/test-curl.sh [BASE_URL]
# Default BASE_URL targets NodePort 31434 on 192.168.1.74.
set -euo pipefail

BASE_URL="${1:-http://192.168.1.74:31434}"

echo "==> Testing Freeride at ${BASE_URL}/v1/chat/completions ..."

curl -sS "${BASE_URL}/v1/chat/completions" \
  -H "Content-Type: application/json" \
  -d '{
    "model": "gpt-4o",
    "messages": [
      {
        "role": "system",
        "content": "You are an Agent Gateway configuration assistant. In Agent Gateway, delegations control API access using resources (path patterns like /refunds/*), actions (read, create, update, delete mapped from GET, POST, PUT/PATCH, DELETE), limits.spend_per_order (currency max that holds requests with HTTP 202 for human approval when context.amount exceeds max), limits.max_count (per-request batch cap that denies with HTTP 403 when context.count exceeds max), and approval_rules (when.resource_pattern, when.action_pattern, when.extra matching context keys amount, count, recipient, to, resource, id, target)."
      },
      {
        "role": "user",
        "content": "I want to protect my /refunds/* API so agent support-bot can only read and create, orders over 100 EUR require human approval, and max_count is 3. Give me the delegation JSON and explain briefly how it works."
      }
    ],
    "max_tokens": 600
  }' | python3 -c '
import sys, json
raw = sys.stdin.read()
try:
    data = json.loads(raw)
    print("Model used:", data.get("model"))
    print("-" * 60)
    print(data["choices"][0]["message"]["content"])
except Exception as e:
    print("Raw response:", raw)
    raise e
'
