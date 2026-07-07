#!/bin/sh

set -eu

PROFILE_NAME="${ZEN_PROXY_CODEX_PROFILE:-zen-proxy}"
CODEX_HOME_DIR="${CODEX_HOME:-${HOME}/.codex}"
MODEL="${ZEN_PROXY_CODEX_MODEL:-deepseek-v4-flash-free}"
BASE_URL="${ZEN_PROXY_CODEX_BASE_URL:-http://127.0.0.1:8788/v1}"
CATALOG_URL="${ZEN_PROXY_CODEX_CATALOG_URL:-${BASE_URL%/}/models}"
CONTEXT_WINDOW="${ZEN_PROXY_CODEX_CONTEXT_WINDOW:-128000}"
COMPACT_LIMIT="${ZEN_PROXY_CODEX_COMPACT_LIMIT:-96000}"
PROFILE_PATH="${CODEX_HOME_DIR}/${PROFILE_NAME}.config.toml"
CATALOG_DIR="${CODEX_HOME_DIR}/model-catalogs"
CATALOG_PATH="${CATALOG_DIR}/zen-proxy.json"

mkdir -p "$CODEX_HOME_DIR" "$CATALOG_DIR"

CATALOG_LINE=""
if command -v curl >/dev/null 2>&1; then
	if curl -fsSL "$CATALOG_URL" -o "${CATALOG_PATH}.tmp" 2>/dev/null; then
		mv "${CATALOG_PATH}.tmp" "$CATALOG_PATH"
		CATALOG_LINE="model_catalog_json = \"${CATALOG_PATH}\""
	else
		rm -f "${CATALOG_PATH}.tmp"
	fi
fi

cat >"$PROFILE_PATH" <<EOF
model = "${MODEL}"
model_provider = "zen-proxy"
model_context_window = ${CONTEXT_WINDOW}
model_auto_compact_token_limit = ${COMPACT_LIMIT}
${CATALOG_LINE}

[model_providers.zen-proxy]
name = "zen-proxy"
base_url = "${BASE_URL}"
wire_api = "responses"
EOF

printf 'Installed Codex profile: %s\n' "$PROFILE_PATH"
if [ -n "$CATALOG_LINE" ]; then
	printf 'Installed Codex model catalog: %s\n' "$CATALOG_PATH"
else
	printf 'Skipped model catalog: start zen-proxy and rerun this script to suppress Codex metadata warnings.\n'
fi
printf 'Run zen-proxy, then start Codex with:\n'
printf '  codex --profile %s\n' "$PROFILE_NAME"
