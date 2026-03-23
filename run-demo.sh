#!/usr/bin/env bash
set -euo pipefail

if [ -f .env.local ]; then
	set -a
	# shellcheck disable=SC1091
	. ./.env.local
	set +a
fi

PROMPT="${*:-hello harness}"

exec go run ./cmd/harness "$PROMPT"
