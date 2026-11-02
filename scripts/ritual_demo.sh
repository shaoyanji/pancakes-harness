#!/usr/bin/env bash
# Ritual: one ingress turn through the local fast sidecar, no API key.
#
# Serves the harness with -preprocess needle (needle3 extraction + laya gate),
# posts one turn, and prints what the sidecar recorded. The sidecar is async by
# design, so its result lands on the event spine after the turn returns; the
# bridge's own stderr line is the observable proof that both local models ran.
#
#   scripts/ritual_demo.sh                 # default sample text
#   TEXT="why is the build red?" scripts/ritual_demo.sh
#   PORT=8099 NEEDLE_PYTHON=~/needle-venv/bin/python scripts/ritual_demo.sh
set -euo pipefail

ROOT=$(cd "$(dirname "$0")/.." && pwd)
PORT=${PORT:-8099}
TEXT=${TEXT:-"run the go tests for internal/preprocess and then read flake.nix, this is urgent"}

export NEEDLE_PYTHON=${NEEDLE_PYTHON:-python3}
export NEEDLE_BRIDGE=${NEEDLE_BRIDGE:-$ROOT/scripts/needle_bridge.py}
export HF_HUB_OFFLINE=${HF_HUB_OFFLINE:-1}
export NEEDLE_TELEMETRY=${NEEDLE_TELEMETRY:-0}

if [[ -n ${HARNESS_BIN:-} ]]; then
  HARNESS=$HARNESS_BIN
elif command -v harness >/dev/null 2>&1; then
  HARNESS=$(command -v harness)
else
  HARNESS=$(nix build "$ROOT#harness" --no-link --print-out-paths)/bin/harness
fi

LOG=$(mktemp -t ritual-serve.XXXXXX.log)
"$HARNESS" serve -model-mode mock -preprocess needle -port "$PORT" >"$LOG" 2>&1 &
SERVER=$!
trap 'kill "$SERVER" 2>/dev/null || true; wait "$SERVER" 2>/dev/null || true' EXIT

for _ in $(seq 1 50); do
  curl -sf "http://127.0.0.1:$PORT/healthz" >/dev/null && break
  sleep 0.2
done
curl -sf "http://127.0.0.1:$PORT/healthz" >/dev/null || {
  echo "serve did not come up on port $PORT" >&2
  sed -n 1,40p "$LOG" >&2
  exit 1
}

BODY=$(TEXT="$TEXT" python3 -c 'import json, os; print(json.dumps({"session_id": "ritual", "branch_id": "main", "text": os.environ["TEXT"]}))')
echo "== turn =="
curl -sf -X POST "http://127.0.0.1:$PORT/v1/turn" -H 'Content-Type: application/json' -d "$BODY"
echo
echo "== waiting for the sidecar =="
for _ in $(seq 1 100); do
  grep -q '"event": "preprocess"' "$LOG" && break
  sleep 0.2
done

echo "== sidecar record =="
grep '"event": "preprocess"' "$LOG" | tail -1 || {
  echo "no preprocess record; serve log:" >&2
  cat "$LOG" >&2
  exit 1
}
