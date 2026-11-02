#!/usr/bin/env bash
# Fine-tune needle3 on the harness's own tools and score it against the base rung.
#
#   PY=/path/to/train-venv/bin/python NEEDLE=/path/to/train-venv/bin/needle \
#     bash scripts/needle_finetune.sh
#
# The venv needs the training extra: pip install 'cactus-needle[train]==3.2.9'
# (python 3.12; jax CPU is enough, no CUDA). Everything it writes under
# scripts/needle-data/ is regenerable and gitignored.
set -euo pipefail

ROOT=$(cd "$(dirname "$0")/.." && pwd)
cd "$ROOT"

PY=${PY:-.venv-train/bin/python}
NEEDLE=${NEEDLE:-.venv-train/bin/needle}
DEPTH=${DEPTH:-12}          # ladder rung; the 20-layer compile needs ~4.5 GB and is killed here
EPOCHS=${EPOCHS:-2}
RANK=${RANK:-16}
DATA=${DATA:-scripts/needle-data}
BASE=${BASE:-checkpoints/needle3-rung$DEPTH.safetensors}

for tool in "$PY" "$NEEDLE"; do
  [ -x "$tool" ] || { echo "missing $tool - set PY and NEEDLE to the training venv (see HARNESS.md)" >&2; exit 1; }
done

echo "== dataset"
"$PY" scripts/needle_rituals.py gen --out-dir "$DATA"

echo "== rung"
"$PY" scripts/needle_rung.py --depth "$DEPTH" --out "$BASE"

echo "== finetune"
"$NEEDLE" finetune "$DATA/train.jsonl" \
  --checkpoint "$BASE" \
  --out "$DATA/rituals-lora.safetensors" \
  --epochs "$EPOCHS" --lora-rank "$RANK" --batch-size 4 --val-split 0.05

echo "== build"
"$NEEDLE" build "$BASE" --lora "$DATA/rituals-lora.safetensors" --out "$DATA/rituals-tuned.cact"

echo "== benchmark: base rung"
"$PY" scripts/needle_bench.py --checkpoint "$BASE" \
  --eval "$DATA/eval.jsonl" --label "base-rung$DEPTH" \
  --out "$DATA/predictions-base.jsonl" --report "$DATA/report-base.json"

echo "== benchmark: tuned"
"$PY" scripts/needle_bench.py --checkpoint "$BASE" --adapter "$DATA/rituals-lora.safetensors" \
  --eval "$DATA/eval.jsonl" --label "tuned-rung$DEPTH" \
  --out "$DATA/predictions-tuned.jsonl" --report "$DATA/report-tuned.json"

echo "== done"
ls -la "$DATA"
