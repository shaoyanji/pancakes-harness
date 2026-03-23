#!/usr/bin/env bash
set -euo pipefail

N="${N:-3}"
OLLAMA_ENDPOINT="${OLLAMA_ENDPOINT:-http://127.0.0.1:11434}"
OLLAMA_MODEL="${OLLAMA_MODEL:-qwen3:0.6b}"
HARNESS_URL="${HARNESS_URL:-http://127.0.0.1:8080}"

direct_payload=$(cat <<JSON
{"model":"$OLLAMA_MODEL","stream":false,"messages":[{"role":"user","content":"hello harness"}]}
JSON
)

turn_payload='{"session_id":"bench-turn","branch_id":"main","text":"hello harness"}'
agent_payload='{"session_id":"bench-agent","branch_id":"main","task":"Say hello in one short sentence.","refs":["branch:head"],"constraints":{"reply_style":"brief","max_sentences":1},"allow_tools":false}'

run_case() {
  local name="$1"
  shift
  local total=0
  local max=0
  for _ in $(seq 1 "$N"); do
    local start end ms
    start=$(date +%s%3N)
    "$@" >/dev/null
    end=$(date +%s%3N)
    ms=$((end - start))
    total=$((total + ms))
    if (( ms > max )); then
      max=$ms
    fi
  done
  local avg=$((total / N))
  printf "%-28s avg=%4dms max=%4dms n=%d\n" "$name" "$avg" "$max" "$N"
}

echo "Benchmark comparison (N=$N)"
run_case "direct_ollama_api_chat" curl -sS -X POST "$OLLAMA_ENDPOINT/api/chat" -H "Content-Type: application/json" -d "$direct_payload"
run_case "harness_v1_turn" curl -sS -X POST "$HARNESS_URL/v1/turn" -H "Content-Type: application/json" -d "$turn_payload"
run_case "harness_v1_agent_call" curl -sS -X POST "$HARNESS_URL/v1/agent-call" -H "Content-Type: application/json" -d "$agent_payload"
