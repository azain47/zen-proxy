#!/bin/sh

set -eu

PROFILE_NAME="${ZEN_PROXY_CODEX_PROFILE:-zen-proxy}"
CODEX_HOME_DIR="${CODEX_HOME:-${HOME}/.codex}"
MODEL="${ZEN_PROXY_CODEX_MODEL:-deepseek-v4-flash-free}"
BASE_URL="${ZEN_PROXY_CODEX_BASE_URL:-http://127.0.0.1:8788/v1}"
CONTEXT_WINDOW="${ZEN_PROXY_CODEX_CONTEXT_WINDOW:-}"
COMPACT_LIMIT="${ZEN_PROXY_CODEX_COMPACT_LIMIT:-}"
PROFILE_PATH="${CODEX_HOME_DIR}/${PROFILE_NAME}.config.toml"
TMP_PROFILE="${PROFILE_PATH}.tmp.$$"

die() {
	printf 'ERR %s\n' "$*" >&2
	exit 1
}

validate_positive_integer() {
	VALUE="$1"
	NAME="$2"
	case "$VALUE" in
	"") ;;
	0 | *[!0-9]*) die "${NAME} must be a positive integer" ;;
	esac
}

escape_toml_string() {
	printf '%s' "$1" | sed 's/\\/\\\\/g; s/"/\\"/g'
}

case "$PROFILE_NAME" in
"" | *[!A-Za-z0-9_-]*) die "profile name may contain only letters, numbers, hyphens, and underscores" ;;
esac
validate_positive_integer "$CONTEXT_WINDOW" "ZEN_PROXY_CODEX_CONTEXT_WINDOW"
validate_positive_integer "$COMPACT_LIMIT" "ZEN_PROXY_CODEX_COMPACT_LIMIT"
MODEL_TOML="$(escape_toml_string "$MODEL")"
BASE_URL_TOML="$(escape_toml_string "$BASE_URL")"

cleanup() {
	rm -f "$TMP_PROFILE"
}
trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM

mkdir -p "$CODEX_HOME_DIR"

{
	printf 'model = "%s"\n' "$MODEL_TOML"
	printf 'model_provider = "zen-proxy"\n'
	if [ -n "$CONTEXT_WINDOW" ]; then
		printf 'model_context_window = %s\n' "$CONTEXT_WINDOW"
	fi
	if [ -n "$COMPACT_LIMIT" ]; then
		printf 'model_auto_compact_token_limit = %s\n' "$COMPACT_LIMIT"
	fi
	printf '\n[model_providers.zen-proxy]\n'
	printf 'name = "zen-proxy"\n'
	printf 'base_url = "%s"\n' "$BASE_URL_TOML"
	printf 'wire_api = "responses"\n'
	printf '\n[model_providers.zen-proxy.auth]\n'
	printf 'command = "/usr/bin/printf"\n'
	printf 'args = ["zen-proxy"]\n'
	printf 'timeout_ms = 1000\n'
	printf 'refresh_interval_ms = 0\n'
} >"$TMP_PROFILE"
chmod 0600 "$TMP_PROFILE"
mv -f "$TMP_PROFILE" "$PROFILE_PATH"

printf 'Installed Codex profile: %s\n' "$PROFILE_PATH"
printf 'Run zen-proxy, then start Codex with:\n'
printf '  codex --profile %s\n' "$PROFILE_NAME"
