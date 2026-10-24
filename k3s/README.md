# Freeride Proxy on k3s (`freeride` namespace)

Deploys **only** the standalone Freeride proxy into the `freeride` namespace on k3s (no Gas Town components). Other Pods across the cluster can call it as a zero-cost OpenAI-compatible LLM backend using only free OpenRouter models.

## Layout & Endpoints

| Scope | Endpoint | Purpose |
|---|---|---|
| **In-Cluster (from any Pod / namespace)** | `http://freeride.freeride.svc.cluster.local:11434/v1` | OpenAI-compatible base URL (`OPENAI_BASE_URL`) |
| **In-Cluster Chat Completions** | `http://freeride.freeride.svc.cluster.local:11434/v1/chat/completions` | Direct chat completions endpoint |
| **NodePort (LAN / host testing)** | `http://<node-ip>:31434/v1/chat/completions` | External test access (e.g. `192.168.1.74:31434`) |

## Files in `k3s/`

- `k3s/models.yaml` — Simplified OpenRouter-only model configuration (separate from the root `models.yaml`). Configured with verified free models capable of explaining and generating Agent Gateway API protection configs (`Protecting-Your-API.md`).
- `k3s/setup-secret.sh` — Reads **only** `OPENROUTER_API_KEY` from the repo's `.env` file (never stored raw in git) and creates/updates the `freeride-secrets` Secret and `freeride-models` ConfigMap in the `freeride` namespace.
- `k3s/freeride.yaml` — Kubernetes `Namespace`, `Deployment`, and `Service` (`ClusterIP` + `NodePort` `31434`).
- `k3s/test-curl.sh` — Quick `curl` smoke test script asking Freeride how to protect an API with Agent Gateway.

## Build & Deploy

1. **Build and push the multi-arch container image** (from the repo root):
   ```bash
   make push
   ```

2. **Populate the Kubernetes Secret & ConfigMap and apply the manifest**:
   ```bash
   ./k3s/setup-secret.sh
   kubectl apply -f k3s/freeride.yaml
   kubectl -n freeride get pods -w
   ```
   *(Or run `make k3s-deploy` which runs both `./k3s/setup-secret.sh` and `kubectl apply -f k3s/freeride.yaml`.)*

## Updating `k3s/models.yaml` without rebuilding the image

Because `k3s/models.yaml` is mounted via the `freeride-models` ConfigMap, you can update model routing at any time:

```bash
./k3s/setup-secret.sh
kubectl -n freeride rollout restart deploy/freeride
```

## Testing with `curl`

### 1. From your host / LAN (via NodePort `31434`)

```bash
curl -sS http://192.168.1.74:31434/v1/chat/completions \
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
```

### 2. From another Pod inside k3s

Any Pod in any namespace (e.g., `agentgw`, `default`, `tenant-*`) can reach Freeride at `http://freeride.freeride.svc.cluster.local:11434/v1/chat/completions`:

```bash
kubectl run -it --rm curl-test --image=curlimages/curl --restart=Never -- \
  curl -sS http://freeride.freeride.svc.cluster.local:11434/v1/chat/completions \
  -H "Content-Type: application/json" \
  -d '{
    "model": "gpt-4o",
    "messages": [
      {"role": "user", "content": "Explain in 3 bullet points how to protect an API endpoint with human approval above a spend limit."}
    ],
    "max_tokens": 300
  }'
```
