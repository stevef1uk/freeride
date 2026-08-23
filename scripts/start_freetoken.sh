#!/bin/bash

set -euo pipefail

pkill -f 'ft serve.*1919' || true

exec ft serve \
  --model-path nvidia/Qwen3.6-35B-A3B-NVFP4 \
  --max-seq-len-override 16384 \
  --max-output-tokens 4096 \
  --max-running-requests 2 \
  --moe-backend hybrid \
  --moe-cache-auto \
  --kv-reserve-tokens 4096 \
  --memory-ratio 0.85 \
  --cuda-graph-max-bs 2 \
  --sampling-defaults model \
  --reasoning-parser qwen3 \
  --port 1919