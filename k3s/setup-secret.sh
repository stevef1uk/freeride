#!/usr/bin/env bash
# Populate the freeride-secrets Kubernetes Secret (OPENROUTER_API_KEY only)
# and the freeride-models ConfigMap (from k3s/models.yaml) in namespace: freeride.
#
# Never stores raw API keys in any committed YAML or config file.
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "${SCRIPT_DIR}/.." && pwd)"
ENV_FILE="${ENV_FILE:-${REPO_ROOT}/.env}"
MODELS_FILE="${MODELS_FILE:-${SCRIPT_DIR}/models.yaml}"
NAMESPACE="${NAMESPACE:-freeride}"

if [[ ! -f "${ENV_FILE}" ]]; then
  echo "ERROR: .env file not found at ${ENV_FILE}" >&2
  exit 1
fi

if [[ ! -f "${MODELS_FILE}" ]]; then
  echo "ERROR: Simplified models.yaml not found at ${MODELS_FILE}" >&2
  exit 1
fi

# Extract ONLY OPENROUTER_API_KEY from .env (strip optional quotes/whitespace)
OPENROUTER_API_KEY="$(grep -E '^[[:space:]]*OPENROUTER_API_KEY[[:space:]]*=' "${ENV_FILE}" | tail -n 1 | cut -d '=' -f 2- | sed -e 's/^[[:space:]]*//' -e 's/[[:space:]]*$//' -e 's/^"//' -e 's/"$//' -e "s/^'//" -e "s/'$//")"

if [[ -z "${OPENROUTER_API_KEY}" ]]; then
  echo "ERROR: OPENROUTER_API_KEY is empty or missing in ${ENV_FILE}" >&2
  exit 1
fi

echo "==> Ensuring namespace '${NAMESPACE}' exists..."
kubectl create namespace "${NAMESPACE}" --dry-run=client -o yaml | kubectl apply -f -

echo "==> Creating/updating Secret 'freeride-secrets' (OPENROUTER_API_KEY only) in namespace '${NAMESPACE}'..."
kubectl -n "${NAMESPACE}" create secret generic freeride-secrets \
  --from-literal=OPENROUTER_API_KEY="${OPENROUTER_API_KEY}" \
  --dry-run=client -o yaml | kubectl apply -f -

echo "==> Creating/updating ConfigMap 'freeride-models' from ${MODELS_FILE} in namespace '${NAMESPACE}'..."
kubectl -n "${NAMESPACE}" create configmap freeride-models \
  --from-file=models.yaml="${MODELS_FILE}" \
  --dry-run=client -o yaml | kubectl apply -f -

echo "==> Done! Secret 'freeride-secrets' and ConfigMap 'freeride-models' are ready in namespace '${NAMESPACE}'."
