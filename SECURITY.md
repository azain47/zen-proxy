# Security

zen-proxy is intended to run as a local developer tool.

By default it listens on `127.0.0.1:8788`, so other machines on your network
cannot connect to it. If you set `ZEN_HOST=0.0.0.0` or another non-local
address, protect the process with your own network controls. The proxy does
not authenticate incoming local requests.

Provider API keys are forwarded to the configured upstream provider using the
standard authorization header. Normal mode does not intentionally store request
bodies, responses, or API keys. `--verbose`, `--debug`, and `--tui` retain the
latest 100 request traces in process memory, with each payload preview capped at
128 KiB. Authorization headers and JSON fields that look like keys, tokens, or
secrets are redacted. Prompt text and tool output remain visible and may contain
secrets of their own, so enable inspection modes only in a trusted terminal.

The inspector never writes traces to disk. Redirecting verbose output to a file
is an operator action and should be treated as storing potentially sensitive
prompt and tool data. Your upstream provider may also log or retain data
according to its own policies.

Do not expose zen-proxy directly to the public internet.

## Reporting vulnerabilities

Please report security issues privately to the maintainers before public
disclosure. Include enough detail to reproduce the issue and the affected
version or commit.
