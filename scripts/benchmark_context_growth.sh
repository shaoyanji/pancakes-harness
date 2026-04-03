#!/usr/bin/env bash
set -euo pipefail

# Context-growth benchmark:
# compares naive direct full-context calls vs harness-bounded egress paths.
#
# Prereqs:
# - harness serve is already running
# - Ollama is running locally

OLLAMA_ENDPOINT="${OLLAMA_ENDPOINT:-http://127.0.0.1:11434}"
OLLAMA_MODEL="${OLLAMA_MODEL:-qwen3:0.6b}"
HARNESS_URL="${HARNESS_URL:-http://127.0.0.1:8080}"
SIZES="${SIZES:-4 8 16}"
RUNS="${RUNS:-1}"
OUTPUT_FILE="${OUTPUT_FILE:-context_growth_results.csv}"
SCENARIOS="${SCENARIOS:-linear noisy tool_heavy branched}"

require_cmd() {
  command -v "$1" >/dev/null 2>&1 || {
    echo "missing required command: $1" >&2
    exit 1
  }
}

require_cmd curl
require_cmd perl
require_cmd date

json_get() {
  local json="$1"
  local expr="$2"
  perl -MJSON::PP -e '
    use strict;
    use warnings;
    my ($raw, $expr) = @ARGV;
    my $d = eval { JSON::PP->new->decode($raw) };
    if (!$d) { exit 0; }
    my @parts = split(/\./, $expr);
    my $v = $d;
    for my $p (@parts) {
      if (ref($v) eq "HASH" && exists $v->{$p}) {
        $v = $v->{$p};
      } else {
        exit 0;
      }
    }
    if (!defined $v) { exit 0; }
    if (ref($v)) {
      print JSON::PP->new->encode($v);
    } else {
      print $v;
    }
  ' "$json" "$expr"
}

csv_escape() {
  local s="$1"
  s="${s//$'\n'/ }"
  s="${s//$'\r'/ }"
  s="${s//\"/\"\"}"
  printf '"%s"' "$s"
}

now_ms() { date +%s%3N; }

bench_latency() {
  local start end
  start="$(now_ms)"
  "$@" >/tmp/cg_last_resp.json
  end="$(now_ms)"
  echo "$((end - start))"
}

metrics_snapshot() {
  curl -sS "${HARNESS_URL}/metrics"
}

compaction_stage_delta() {
  local before="$1"
  local after="$2"
  perl -MJSON::PP -e '
    use strict;
    use warnings;
    my ($braw, $araw) = @ARGV;
    my $b = eval { JSON::PP->new->decode($braw) } || {};
    my $a = eval { JSON::PP->new->decode($araw) } || {};
    my $bm = $b->{compaction_stage_counts} || {};
    my $am = $a->{compaction_stage_counts} || {};
    my @inc;
    for my $k (sort keys %$am) {
      my $d = ($am->{$k} || 0) - ($bm->{$k} || 0);
      push @inc, $k if $d > 0;
    }
    print(@inc ? join("+", @inc) : "n/a");
  ' "$before" "$after"
}

reason_delta() {
  local before="$1"
  local after="$2"
  local field="$3"
  perl -MJSON::PP -e '
    use strict;
    use warnings;
    my ($braw, $araw, $field) = @ARGV;
    my $b = eval { JSON::PP->new->decode($braw) } || {};
    my $a = eval { JSON::PP->new->decode($araw) } || {};
    my $bm = $b->{$field} || {};
    my $am = $a->{$field} || {};
    my ($best_key, $best_delta) = ("n/a", 0);
    for my $k (sort keys %$am) {
      my $d = ($am->{$k} || 0) - ($bm->{$k} || 0);
      next if $d <= 0;
      if ($d > $best_delta || ($d == $best_delta && $k lt $best_key)) {
        $best_key = $k;
        $best_delta = $d;
      }
    }
    print $best_key;
  ' "$before" "$after" "$field"
}

budget_pressure_delta() {
  local before="$1"
  local after="$2"
  perl -MJSON::PP -e '
    use strict;
    use warnings;
    my ($braw, $araw) = @ARGV;
    my $b = eval { JSON::PP->new->decode($braw) } || {};
    my $a = eval { JSON::PP->new->decode($araw) } || {};
    my $delta = ($a->{selector_budget_pressure_total} || 0) - ($b->{selector_budget_pressure_total} || 0);
    print($delta > 0 ? $delta : 0);
  ' "$before" "$after"
}

scenario_message() {
  local scenario="$1"
  local idx="$2"
  case "$scenario" in
    linear)
      echo "linear turn ${idx}: user says hello and asks for short acknowledgement."
      ;;
    noisy)
      echo "noise turn ${idx}: lorem ipsum telemetry=$(printf '%040d' "$idx") random-tags=a,b,c,ignore."
      ;;
    tool_heavy)
      echo "tool-heavy turn ${idx}: tool.result call_id=t${idx} payload=xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx summary=ok artifact=blob://tool/${idx}"
      ;;
    branched)
      echo "branched main turn ${idx}: maintain concise project summary for main branch only."
      ;;
    *)
      echo "generic turn ${idx}"
      ;;
  esac
}

warm_model() {
  echo "Warming model..."
  local direct='{"model":"'"${OLLAMA_MODEL}"'","stream":false,"think":false,"keep_alive":-1,"messages":[{"role":"user","content":"warmup ping"}]}'
  curl -sS -X POST "${OLLAMA_ENDPOINT}/api/chat" -H "Content-Type: application/json" -d "$direct" >/dev/null || true
  local h='{"session_id":"warmup-turn","branch_id":"main","text":"warmup ping"}'
  curl -sS -X POST "${HARNESS_URL}/v1/turn" -H "Content-Type: application/json" -d "$h" >/dev/null || true
  local a='{"session_id":"warmup-agent","branch_id":"main","task":"Reply with warmup.","allow_tools":false}'
  curl -sS -X POST "${HARNESS_URL}/v1/agent-call" -H "Content-Type: application/json" -d "$a" >/dev/null || true
}

prepare_history() {
  local scenario="$1"
  local size="$2"
  local session_turn="$3"
  local session_agent="$4"

  for i in $(seq 1 "$size"); do
    local msg
    msg="$(scenario_message "$scenario" "$i")"
    local t='{"session_id":"'"${session_turn}"'","branch_id":"main","text":"'"${msg//\"/\\\"}"'"}'
    curl -sS -X POST "${HARNESS_URL}/v1/turn" -H "Content-Type: application/json" -d "$t" >/dev/null

    local a='{"session_id":"'"${session_agent}"'","branch_id":"main","task":"'"${msg//\"/\\\"}"'","allow_tools":false}'
    curl -sS -X POST "${HARNESS_URL}/v1/agent-call" -H "Content-Type: application/json" -d "$a" >/dev/null
  done

  if [[ "$scenario" == "branched" ]]; then
    local fork='{"session_id":"'"${session_turn}"'","parent_branch_id":"main","child_branch_id":"alt-1"}'
    curl -sS -X POST "${HARNESS_URL}/v1/branch/fork" -H "Content-Type: application/json" -d "$fork" >/dev/null || true
    for i in $(seq 1 "$size"); do
      local alt="branched alt turn ${i}: unrelated branch chatter and noisy artifacts $(printf '%050d' "$i")"
      local t='{"session_id":"'"${session_turn}"'","branch_id":"alt-1","text":"'"${alt//\"/\\\"}"'"}'
      curl -sS -X POST "${HARNESS_URL}/v1/turn" -H "Content-Type: application/json" -d "$t" >/dev/null || true
    done
  fi
}

direct_call_full_context() {
  local scenario="$1"
  local size="$2"
  local token="$3"
  local tmpfile
  tmpfile="$(mktemp)"

  for i in $(seq 1 "$size"); do
    printf 'user|%s\n' "$(scenario_message "$scenario" "$i")" >>"$tmpfile"
    printf 'assistant|ack %d\n' "$i" >>"$tmpfile"
  done
  if [[ "$scenario" == "branched" ]]; then
    for i in $(seq 1 "$size"); do
      printf 'user|alt branch unrelated context %s\n' "$(printf '%030d' "$i")" >>"$tmpfile"
      printf 'assistant|alt ack %d\n' "$i" >>"$tmpfile"
    done
  fi
  printf 'user|Reply with exactly %s\n' "$token" >>"$tmpfile"

  local payload
  payload="$(perl -MJSON::PP -e '
    use strict; use warnings;
    my ($model, $file) = @ARGV;
    open my $fh, "<", $file or die $!;
    my @messages;
    while (my $line = <$fh>) {
      chomp $line;
      my ($role, $content) = split(/\|/, $line, 2);
      push @messages, { role => $role, content => $content };
    }
    print JSON::PP->new->encode({
      model => $model,
      stream => JSON::PP::false,
      think => JSON::PP::false,
      keep_alive => -1,
      messages => \@messages
    });
  ' "$OLLAMA_MODEL" "$tmpfile")"
  rm -f "$tmpfile"

  printf '%s' "$payload" | wc -c | tr -d ' ' >/tmp/cg_last_direct_request_bytes
  curl -sS -X POST "${OLLAMA_ENDPOINT}/api/chat" -H "Content-Type: application/json" -d "$payload"
}

append_result() {
  local scenario="$1"
  local size="$2"
  local path="$3"
  local latency_ms="$4"
  local envelope_bytes="$5"
  local request_body_bytes="$6"
  local output_text="$7"
  local correctness="$8"
  local compaction_stage="$9"
  local selector_inclusion="$10"
  local selector_exclusion="${11}"
  local selector_budget_pressure="${12}"
  {
    printf '%s,%s,%s,%s,%s,%s,%s,%s,%s,%s,%s,%s\n' \
      "$scenario" \
      "$size" \
      "$path" \
      "$latency_ms" \
      "$envelope_bytes" \
      "$request_body_bytes" \
      "$correctness" \
      "$compaction_stage" \
      "$selector_inclusion" \
      "$selector_exclusion" \
      "$selector_budget_pressure" \
      "$(csv_escape "$output_text")"
  } >>"$OUTPUT_FILE"
}

warm_model
echo 'scenario,size,path,latency_ms,envelope_bytes,request_body_bytes,correctness,compaction_stage,selector_inclusion_reason,selector_exclusion_reason,selector_budget_pressure,output_text' >"$OUTPUT_FILE"

for scenario in $SCENARIOS; do
  for size in $SIZES; do
    for run_idx in $(seq 1 "$RUNS"); do
      session_turn="cg_turn_${scenario}_${size}_${run_idx}_$RANDOM"
      session_agent="cg_agent_${scenario}_${size}_${run_idx}_$RANDOM"
      token="BENCH_OK_${scenario}_${size}_${run_idx}"

      prepare_history "$scenario" "$size" "$session_turn" "$session_agent"

      # 1) Direct naive full-context baseline.
      direct_latency="$(bench_latency direct_call_full_context "$scenario" "$size" "$token")"
      direct_resp="$(cat /tmp/cg_last_resp.json)"
      direct_request_bytes="$(cat /tmp/cg_last_direct_request_bytes 2>/dev/null || echo n/a)"
      direct_text="$(json_get "$direct_resp" "message.content")"
      direct_ok="fail"
      [[ "$direct_text" == *"$token"* ]] && direct_ok="pass"
      append_result "$scenario" "$size" "direct_full_context" "$direct_latency" "n/a" "${direct_request_bytes:-n/a}" "$direct_text" "$direct_ok" "n/a" "n/a" "n/a" "0"

      # 2) Harness /v1/turn
      before_metrics="$(metrics_snapshot)"
      turn_payload='{"session_id":"'"${session_turn}"'","branch_id":"main","text":"Reply with exactly '"${token}"'"}'
      turn_latency="$(bench_latency curl -sS -X POST "${HARNESS_URL}/v1/turn" -H "Content-Type: application/json" -d "$turn_payload")"
      turn_resp="$(cat /tmp/cg_last_resp.json)"
      after_metrics="$(metrics_snapshot)"
      turn_text="$(json_get "$turn_resp" "answer")"
      turn_env="$(json_get "$turn_resp" "envelope_bytes")"
      turn_stage="$(compaction_stage_delta "$before_metrics" "$after_metrics")"
      turn_selector_in="$(reason_delta "$before_metrics" "$after_metrics" "selector_inclusion_reason_counts")"
      turn_selector_ex="$(reason_delta "$before_metrics" "$after_metrics" "selector_exclusion_reason_counts")"
      turn_budget_pressure="$(budget_pressure_delta "$before_metrics" "$after_metrics")"
      turn_ok="fail"
      [[ "$turn_text" == *"$token"* ]] && turn_ok="pass"
      append_result "$scenario" "$size" "harness_v1_turn" "$turn_latency" "${turn_env:-n/a}" "n/a" "$turn_text" "$turn_ok" "$turn_stage" "$turn_selector_in" "$turn_selector_ex" "$turn_budget_pressure"

      # 3) Harness /v1/agent-call
      before_metrics="$(metrics_snapshot)"
      agent_payload='{"session_id":"'"${session_agent}"'","branch_id":"main","task":"Reply with exactly '"${token}"'","refs":["branch:head","tool:last"],"constraints":{"reply_style":"brief","max_sentences":1},"allow_tools":false}'
      agent_latency="$(bench_latency curl -sS -X POST "${HARNESS_URL}/v1/agent-call" -H "Content-Type: application/json" -d "$agent_payload")"
      agent_resp="$(cat /tmp/cg_last_resp.json)"
      after_metrics="$(metrics_snapshot)"
      agent_text="$(json_get "$agent_resp" "answer")"
      agent_env="$(json_get "$agent_resp" "envelope_bytes")"
      agent_stage="$(compaction_stage_delta "$before_metrics" "$after_metrics")"
      agent_selector_in="$(reason_delta "$before_metrics" "$after_metrics" "selector_inclusion_reason_counts")"
      agent_selector_ex="$(reason_delta "$before_metrics" "$after_metrics" "selector_exclusion_reason_counts")"
      agent_budget_pressure="$(budget_pressure_delta "$before_metrics" "$after_metrics")"
      agent_ok="fail"
      [[ "$agent_text" == *"$token"* ]] && agent_ok="pass"
      append_result "$scenario" "$size" "harness_v1_agent_call" "$agent_latency" "${agent_env:-n/a}" "n/a" "$agent_text" "$agent_ok" "$agent_stage" "$agent_selector_in" "$agent_selector_ex" "$agent_budget_pressure"
    done
  done
done

echo "Wrote benchmark results to ${OUTPUT_FILE}"
cat "$OUTPUT_FILE"
