# zen-proxy

Single-binary proxy that lets **Claude Code**, **Codex**, and any OpenAI-compatible tool use [OpenCode Zen](https://opencode.ai/zen) or [OpenRouter](https://openrouter.ai/) models.

```
Claude Code ──→ /v1/messages (Anthropic)  ──┐
Codex       ──→ /v1/responses (OpenAI)    ──┼──→ Chat Completions ──→ Zen or OpenRouter
Cursor etc  ──→ /v1/chat/completions      ──┘
```

Single Go binary with no runtime dependencies. Zen free models work without an API key; OpenRouter requires your OpenRouter API key.

## Install

Recommended:

```bash
curl -fsSL https://raw.githubusercontent.com/azain47/zen-proxy/main/install.sh | sh
```

The installer downloads a prebuilt release asset when available, installs the
`zen-proxy` binary into `/usr/local/bin` or `~/.local/bin`, and falls back to
building from source when run inside a checkout. Published SHA-256 checksums
are verified when `sha256sum` or `shasum` is available.

```bash
go install github.com/azain47/zen-proxy/cmd/zen-proxy@latest
```

Or build from source:

```bash
git clone https://github.com/azain47/zen-proxy
cd zen-proxy
make build
```

From a source checkout you can also run:

```bash
./install.sh
```

Installer environment variables:

| Env var | Description |
|---------|-------------|
| `ZEN_PROXY_VERSION` | Release tag to install (default: `latest`) |
| `ZEN_PROXY_BINDIR` | Install directory override |
| `ZEN_PROXY_REPO` | GitHub repo override, e.g. `owner/zen-proxy` |
| `ZEN_PROXY_FROM_SOURCE=1` | Force local source build and install |

## Release Builds

Build release artifacts for macOS, Linux, and Windows on both amd64 and arm64:

```bash
make release VERSION=v0.2.0
```

Artifacts are written to `dist/`:

| Platform | Artifact |
|----------|----------|
| macOS Intel | `zen-proxy_darwin_amd64.tar.gz` |
| macOS Apple Silicon | `zen-proxy_darwin_arm64.tar.gz` |
| Linux amd64 | `zen-proxy_linux_amd64.tar.gz` |
| Linux arm64 | `zen-proxy_linux_arm64.tar.gz` |
| Windows amd64 | `zen-proxy_windows_amd64.zip` |
| Windows arm64 | `zen-proxy_windows_arm64.zip` |

Each archive includes the project and third-party licenses. The release target
also writes `dist/checksums.txt`. The installer looks for the macOS/Linux
`.tar.gz` names above when installing from GitHub Releases.

To publish a GitHub Release, push a version tag:

```bash
git tag v0.2.0
git push origin v0.2.0
```

The release workflow builds the same artifacts and uploads them to the tagged release.

## Project Layout

```text
cmd/zen-proxy/      CLI entrypoint and version wiring
internal/proxy/     protocol translation, upstream handling, config, tests
.github/workflows/  CI and release automation
install.sh          standalone installer
```

## Usage

```bash
./zen-proxy
```

For a live request inspector, start the proxy with:

```bash
zen-proxy --tui
```

The dashboard keeps the latest 100 requests in memory and provides separate
views for the inbound client request, translated upstream request, raw upstream
response, and response returned to the client. Use `j`/`k` to select requests,
arrow keys or the mouse wheel for line scrolling, Page Up/Page Down or
`Ctrl+U`/`Ctrl+D` for page scrolling, Home/End to jump, `1`-`4` to select a
view, and `q` to stop the dashboard and proxy. For headless debugging,
`zen-proxy --verbose` (or `--debug`) prints the same sanitized traces to stderr.

By default, the proxy uses OpenCode Zen. On startup, it fetches and displays the
available models. The model names and counts are controlled by the upstream and
change over time; startup output has this form:

```
zen-proxy → https://opencode.ai/zen/v1/chat/completions (provider: zen, default model: deepseek-v4-flash-free)
fetched <count> models from upstream

  Free models (<count>):
    • <model-id>
    ...

  Other models (<count>):
    • <model-id>
    ...

listening on 127.0.0.1:8788
```

### With OpenRouter

Create an API key at OpenRouter, then run:

```bash
ZEN_PROVIDER=openrouter OPENROUTER_API_KEY=sk-or-v1-your-key zen-proxy
```

The OpenRouter preset uses:

| Setting | Value |
|---------|-------|
| Upstream | `https://openrouter.ai/api/v1/chat/completions` |
| Models | `https://openrouter.ai/api/v1/models` |
| Fallback model | `openrouter/free` |

You can use any OpenRouter model name directly, including `:free` models such as `qwen/qwen3-coder:free`, or the free-model router `openrouter/free`.

For OpenRouter, startup output lists only free text-output models to keep the terminal readable. The `/v1/models` endpoint still returns the full upstream model list.

Optional OpenRouter metadata headers:

```bash
OPENROUTER_HTTP_REFERER=https://your-app.example \
OPENROUTER_APP_TITLE="your app name" \
ZEN_PROVIDER=openrouter \
OPENROUTER_API_KEY=sk-or-v1-your-key \
zen-proxy
```

### With Claude Code

```bash
export ANTHROPIC_BASE_URL=http://localhost:8788
export ANTHROPIC_API_KEY=anything
export ANTHROPIC_MODEL=deepseek-v4-flash-free
claude
```

You can switch models in-session with `/model` or set any model from the list above.

### With Codex

Start the proxy, then install a Codex profile from another terminal:

```bash
zen-proxy
```

```bash
./scripts/install-codex-profile.sh
```

This writes `~/.codex/zen-proxy.config.toml`. Codex 0.134.0 and later load that
file when `--profile zen-proxy` is selected. Codex fetches per-model metadata
(context window, reasoning levels, and agent instructions) from the proxy's
`/v1/models` endpoint, so the generated profile does not override model-specific
context limits. Set `ZEN_PROXY_CODEX_CONTEXT_WINDOW` or
`ZEN_PROXY_CODEX_COMPACT_LIMIT` only when you intentionally want fixed values.

Manual equivalent:

```toml
model = "deepseek-v4-flash-free"
model_provider = "zen-proxy"

[model_providers.zen-proxy]
name = "zen-proxy"
base_url = "http://127.0.0.1:8788/v1"
wire_api = "responses"

[model_providers.zen-proxy.auth]
command = "/usr/bin/printf"
args = ["zen-proxy"]
timeout_ms = 1000
refresh_interval_ms = 0
```

The static command-auth token is not a secret and Zen Proxy does not validate
it. It is present because current Codex versions refresh a custom provider's
`/models` endpoint only when that provider has command-backed authentication.
This enables live model metadata without using ChatGPT authentication or a
separate helper script.

#### Custom Codex agent instructions

By default the proxy serves a bundled copy of Codex's official agent prompt as
the `base_instructions` of every advertised model. It comes from
`codex-rs/models-manager/prompt.md` in
[openai/codex](https://github.com/openai/codex) under Apache-2.0. To replace it
with your own prompt, point at a file:

```bash
ZEN_PROXY_CODEX_INSTRUCTIONS_FILE=/path/to/my-prompt.md zen-proxy
```

The file contents are served as-is to every Codex model entry, fully replacing
the built-in prompt. If the path is unset or unreadable, the proxy logs a
warning and falls back to the bundled prompt. Restart the proxy after editing
the file; it is read once at startup.

Start Codex with that profile:

```bash
codex --profile zen-proxy
```

To change models, keep the provider fixed and change only the model slug:

```bash
codex --profile zen-proxy -m mimo-v2.5-free
codex --profile zen-proxy -m 'qwen/qwen3-coder:free'
```

Inside an active Codex session, use `/model` and enter any model supported by
your selected upstream. zen-proxy passes the selected model through unchanged;
if a provider-side model is temporarily unavailable, pick another model from
the startup list.

Do not use only `OPENAI_BASE_URL` for Codex. That leaves Codex on the built-in
OpenAI provider path, where ChatGPT-account auth can reject non-OpenAI model
names before the request reaches zen-proxy.

### With Cursor / Continue / any OpenAI-compatible tool

- Base URL: `http://localhost:8788/v1`
- API Key: anything (not validated)
- Model: any model from the list shown at startup

## Configuration

| Env var | Default | Description |
|---------|---------|-------------|
| `ZEN_HOST` | `127.0.0.1` | Listen host. Use `0.0.0.0` only if you intentionally want network access |
| `ZEN_PORT` | `8788` | Listen port |
| `ZEN_PROVIDER` | `zen` | Provider preset: `zen` or `openrouter` |
| `ZEN_UPSTREAM` | `https://opencode.ai/zen/v1/chat/completions` | Upstream endpoint |
| `ZEN_MODELS_URL` | derived from upstream | Upstream models endpoint |
| `ZEN_MODEL_METADATA_URL` | unset | Optional LiteLLM-compatible capability catalog URL |
| `ZEN_CORS_ORIGINS` | unset | Comma-separated exact browser origins allowed to call the proxy. Browser requests with an `Origin` header are denied by default; CLI clients do not require CORS |
| `ZEN_VERBOSE` / `ZEN_DEBUG` | unset | Print sanitized inbound, translated upstream, upstream response, and client response payloads |
| `ZEN_TUI` | unset | Start the live in-memory request inspector; equivalent to `--tui` |
| `ZEN_API_KEY` | `public` | Upstream API key (use `public` for free tier) |
| `ZEN_MODEL` | `deepseek-v4-flash-free` | Fallback model when request has none |
| `OPENROUTER_API_KEY` | unset | API key used when `ZEN_PROVIDER=openrouter` and `ZEN_API_KEY` is unset |
| `OPENROUTER_HTTP_REFERER` | unset | Optional OpenRouter attribution header |
| `OPENROUTER_APP_TITLE` | `zen-proxy` | Optional OpenRouter attribution header |
| `ZEN_HTTP_REFERER` | unset | Alias for `OPENROUTER_HTTP_REFERER` |
| `ZEN_APP_TITLE` | unset | Alias for `OPENROUTER_APP_TITLE` |
| `ZEN_PROXY_CODEX_INSTRUCTIONS_FILE` | unset | Path to a file whose contents replace Codex's built-in agent prompt in every `/v1/models` entry. See [Custom Codex agent instructions](#custom-codex-agent-instructions). |

The proxy passes through whatever model name the client sends. `ZEN_MODEL` is
only used as a fallback.
When `ZEN_MODEL_METADATA_URL` is configured, provider model data is enriched
from that LiteLLM-compatible catalog. Provider-reported context windows take
precedence; the catalog fills missing context windows and known reasoning,
parallel-tool, and vision capabilities. If the catalog is unavailable or has
no matching entry,
the proxy exposes conservative `low`, `medium`, and `high` reasoning controls
but otherwise avoids guessing model capabilities. Providers may ignore a
reasoning effort for models that do not implement one. A
mutable hosted catalog is intentionally not enabled by default.

For example, to opt into LiteLLM's staging catalog explicitly:

```bash
ZEN_MODEL_METADATA_URL=https://raw.githubusercontent.com/BerriAI/litellm/litellm_internal_staging/model_prices_and_context_window.json zen-proxy
```

## Endpoints

| Method | Path | Protocol |
|--------|------|----------|
| POST | `/v1/messages` | Anthropic Messages API |
| POST | `/v1/messages/count_tokens` | Anthropic token-count estimate |
| POST | `/v1/responses` | OpenAI Responses API |
| POST | `/v1/chat/completions` | OpenAI Chat Completions |
| GET | `/v1/models` | Model listing snapshot fetched from the selected upstream at startup |
| GET | `/health` | Health check |

## How it works

1. On startup, fetches available models from the upstream models endpoint
2. Accepts requests in three API formats (Anthropic, Responses, Chat Completions)
3. Translates all requests into OpenAI Chat Completions format
4. Forwards to the selected upstream with the configured authorization
5. Translates responses back to the original protocol format

Streaming, tool calls, and thinking/reasoning blocks are all supported.

## Security

zen-proxy is designed as a local developer tool. It listens on `127.0.0.1` by default and does not authenticate incoming requests. Do not expose it directly to the public internet; if you set `ZEN_HOST=0.0.0.0`, protect it with your own network controls.

Provider API keys are forwarded to the selected upstream. Normal mode does not
store request bodies, responses, or keys. Debug and TUI modes retain a bounded,
sanitized in-memory history for inspection. See [SECURITY.md](SECURITY.md) for details.

## Using paid models

Set `ZEN_API_KEY` to your OpenCode API key to access paid models:

```bash
ZEN_API_KEY=sk-your-key zen-proxy
```

Then use any model from the paid list (e.g., `claude-opus-4-8`, `gpt-5.5`, `deepseek-v4-pro`).

## License

Zen Proxy's original code is MIT licensed. The bundled OpenAI Codex agent
prompt is Apache-2.0 licensed. See [THIRD_PARTY_NOTICES.md](THIRD_PARTY_NOTICES.md)
and [LICENSES/Apache-2.0.txt](LICENSES/Apache-2.0.txt).
