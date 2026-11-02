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

echo "== build: base rung, then the adapter merged over it"
"$NEEDLE" build "$BASE" --out "$DATA/rituals-base.cact"
"$NEEDLE" build "$BASE" --lora "$DATA/rituals-lora.safetensors" --out "$DATA/rituals-tuned.cact"

# Score both archives through the .cact engine, which is the deployed runtime and
# the only usable benchmark: it keeps a KV cache, where the pure-JAX decoder in
# needle.model.run re-runs the whole prefix per token (16-26 s per row here).
for pair in base:rituals-base tuned:rituals-tuned; do
  label=${pair%%:*}
  archive=${pair##*:}
  echo "== benchmark: $label"
  "$PY" scripts/needle_bench.py --weights "$DATA/$archive.cact" \
    --eval "$DATA/eval.jsonl" --label "$label-rung$DEPTH" \
    --out "$DATA/predictions-$label.jsonl" --report "$DATA/report-$label.json"
done

echo "== done"
ls -la "$DATA"
