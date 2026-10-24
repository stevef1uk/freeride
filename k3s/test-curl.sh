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
        "content": "You are an Agent Gateway configuration assistant. Reference schema for POST /v1/delegations/: { \"user_id\": \"alice\", \"agent_id\": \"support-bot\", \"resources\": [\"/refunds/*\"], \"actions\": [\"read\", \"create\"], \"expires_in_seconds\": 3600, \"limits\": { \"spend_per_order\": { \"type\": \"currency\", \"max\": 100, \"unit\": \"EUR\" }, \"max_count\": { \"type\": \"count\", \"max\": 3 } }, \"approval_rules\": [ { \"when\": { \"resource_pattern\": \"/refunds/*\", \"action_pattern\": \"create\", \"extra\": { \"recipient\": \"treasury@acme.com\" } }, \"require\": true, \"rationale\": \"treasury refunds need finance sign-off\" } ] }. Rules: actions map from HTTP verbs (GET->read, POST->create, PUT/PATCH->update, DELETE->delete). spend_per_order holds requests with HTTP 202 when context.amount > max. max_count denies with HTTP 403 when context.count > max (per-request cap, not cumulative). Only 7 body context keys exist: amount, count, recipient, to, resource, id, target."
      },
      {
        "role": "user",
        "content": "I want to protect my /refunds/* API so agent support-bot can only read and create, orders over 100 EUR require human approval, and max_count is 3. Give me the delegation JSON and explain briefly how it works."
      }
    ],
    "max_tokens": 600
  }' | jq -r '.choices[0].message.content'
