# zen-proxy

Single-binary proxy that lets **Claude Code**, **Codex**, and any OpenAI-compatible tool use [OpenCode Zen](https://opencode.ai/zen) or [OpenRouter](https://openrouter.ai/) models.

```
Claude Code ──→ /v1/messages (Anthropic)  ──┐
Codex       ──→ /v1/responses (OpenAI)    ──┼──→ Chat Completions ──→ Zen or OpenRouter
Cursor etc  ──→ /v1/chat/completions      ──┘
```

Zero dependencies. Single Go binary. Zen free models work without an API key; OpenRouter requires your OpenRouter API key.

## Install

Recommended:

```bash
curl -fsSL https://raw.githubusercontent.com/azain47/zen-proxy/main/install.sh | sh
```

The installer downloads a prebuilt release asset when available, installs the
`zen-proxy` binary into `/usr/local/bin` or `~/.local/bin`, and falls back to
building from source when run inside a checkout.

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
make release VERSION=v0.1.0
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

The release target also writes `dist/checksums.txt`. The installer looks for the macOS/Linux `.tar.gz` names above when installing from GitHub Releases.

To publish a GitHub Release, push a version tag:

```bash
git tag v0.1.0
git push origin v0.1.0
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

By default, the proxy uses OpenCode Zen. On startup, it fetches and displays all available models:

```
zen-proxy → https://opencode.ai/zen/v1/chat/completions (provider: zen, default model: deepseek-v4-flash-free)
fetched 50 models from upstream

  Free models (5):
    • big-pickle
    • deepseek-v4-flash-free
    • mimo-v2.5-free
    • nemotron-3-ultra-free
    • north-mini-code-free

  Other models (45):
    • claude-opus-4-8
    • deepseek-v4-pro
    • gpt-5.5
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

```bash
export OPENAI_BASE_URL=http://localhost:8788
export OPENAI_API_KEY=anything
codex --model deepseek-v4-flash-free
```

Or in `~/.codex/config.toml`:

```toml
model = "deepseek-v4-flash-free"
```

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
| `ZEN_API_KEY` | `public` | Upstream API key (use `public` for free tier) |
| `ZEN_MODEL` | `deepseek-v4-flash-free` | Fallback model when request has none |
| `OPENROUTER_API_KEY` | unset | API key used when `ZEN_PROVIDER=openrouter` and `ZEN_API_KEY` is unset |
| `OPENROUTER_HTTP_REFERER` | unset | Optional OpenRouter attribution header |
| `OPENROUTER_APP_TITLE` | `zen-proxy` | Optional OpenRouter attribution header |

The proxy passes through whatever model name the client sends. `ZEN_MODEL` is only used as a fallback.

## Endpoints

| Method | Path | Protocol |
|--------|------|----------|
| POST | `/v1/messages` | Anthropic Messages API |
| POST | `/v1/responses` | OpenAI Responses API |
| POST | `/v1/chat/completions` | OpenAI Chat Completions |
| GET | `/v1/models` | Model listing (fetched live from the selected upstream) |
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

Provider API keys are forwarded to the selected upstream. zen-proxy does not intentionally store request bodies, responses, or keys. See [SECURITY.md](SECURITY.md) for details.

## Using paid models

Set `ZEN_API_KEY` to your OpenCode API key to access paid models:

```bash
ZEN_API_KEY=sk-your-key zen-proxy
```

Then use any model from the paid list (e.g., `claude-opus-4-8`, `gpt-5.5`, `deepseek-v4-pro`).

## License

MIT
