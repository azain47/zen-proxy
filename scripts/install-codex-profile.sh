#!/bin/sh

set -eu

PROFILE_NAME="${ZEN_PROXY_CODEX_PROFILE:-zen-proxy}"
CODEX_HOME_DIR="${CODEX_HOME:-${HOME}/.codex}"
MODEL="${ZEN_PROXY_CODEX_MODEL:-deepseek-v4-flash-free}"
BASE_URL="${ZEN_PROXY_CODEX_BASE_URL:-http://127.0.0.1:8788/v1}"
CONTEXT_WINDOW="${ZEN_PROXY_CODEX_CONTEXT_WINDOW:-128000}"
COMPACT_LIMIT="${ZEN_PROXY_CODEX_COMPACT_LIMIT:-96000}"
PROFILE_PATH="${CODEX_HOME_DIR}/${PROFILE_NAME}.config.toml"

mkdir -p "$CODEX_HOME_DIR"

cat >"$PROFILE_PATH" <<EOF
model = "${MODEL}"
model_provider = "zen-proxy"
model_context_window = ${CONTEXT_WINDOW}
model_auto_compact_token_limit = ${COMPACT_LIMIT}

[model_providers.zen-proxy]
name = "zen-proxy"
base_url = "${BASE_URL}"
wire_api = "responses"
EOF

printf 'Installed Codex profile: %s\n' "$PROFILE_PATH"
printf 'Run zen-proxy, then start Codex with:\n'
printf '  codex --profile %s\n' "$PROFILE_NAME"
