#!/usr/bin/env bash
set -euo pipefail

STORE_DIR="${STORE_DIR:-./store}"

mkdir -p "$STORE_DIR"

if ! pgrep -f "xs serve $STORE_DIR" >/dev/null 2>&1; then
	echo "starting xs serve on $STORE_DIR"
	xs serve "$STORE_DIR" &
	XS_PID=$!
	trap 'kill "$XS_PID" >/dev/null 2>&1 || true' EXIT
	sleep 1
fi

if [ -f .env.local ]; then
	set -a
	# shellcheck disable=SC1091
	. ./.env.local
	set +a
fi

export HARNESS_BACKEND_MODE=xs

PROMPT="${*:-hello persistent harness}"

exec go run ./cmd/harness "$PROMPT"
