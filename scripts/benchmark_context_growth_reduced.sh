#!/usr/bin/env bash
set -euo pipefail

# Reduced matrix helper for recurring context-growth benchmark runs.
# Preserves existing benchmark semantics; only narrows scenario/size matrix.

SCENARIOS="${SCENARIOS:-branched tool_heavy noisy}"
SIZES="${SIZES:-16 32 64 128}"
RUNS="${RUNS:-1}"
OUTPUT_FILE="${OUTPUT_FILE:-/tmp/context_growth_reduced.csv}"

export SCENARIOS
export SIZES
export RUNS
export OUTPUT_FILE

exec ./scripts/benchmark_context_growth.sh
