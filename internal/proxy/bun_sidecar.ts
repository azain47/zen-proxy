// zen-proxy upstream sidecar. Runs inside the real Bun runtime so upstream
// requests carry Bun's genuine TLS/HTTP fingerprint (the same engine the
// OpenCode client uses) instead of a reimplementation. The Go proxy posts
// {url, method, headers, body} envelopes to /forward and streams the upstream
// response back verbatim.

interface Envelope {
  url: string;
  method?: string;
  headers?: [string, string][];
  body?: string;
}

function errorResponse(message: string, status = 502): Response {
  return new Response(JSON.stringify({ error: message }), {
    status,
    headers: { "content-type": "application/json", "x-zen-sidecar-error": "1" },
  });
}

const server = Bun.serve({
  hostname: "127.0.0.1",
  port: 0,
  // Never time out long SSE streams between chunks.
  idleTimeout: 0,
  maxRequestBodySize: 512 * 1024 * 1024,
  async fetch(req) {
    const { pathname } = new URL(req.url);
    if (pathname === "/healthz") {
      return new Response("ok");
    }
    if (pathname !== "/forward" || req.method !== "POST") {
      return new Response("not found", { status: 404 });
    }

    let envelope: Envelope;
    try {
      envelope = (await req.json()) as Envelope;
    } catch {
      return errorResponse("invalid envelope", 400);
    }
    if (!envelope.url) {
      return errorResponse("missing url", 400);
    }

    const headers = new Headers();
    for (const [name, value] of envelope.headers ?? []) {
      headers.append(name, value);
    }

    const controller = new AbortController();
    req.signal.addEventListener("abort", () => controller.abort());

    let upstream: Response;
    try {
      upstream = await fetch(envelope.url, {
        method: envelope.method || "POST",
        headers,
        body: envelope.body && envelope.body.length > 0 ? envelope.body : undefined,
        signal: controller.signal,
      });
    } catch (err) {
      return errorResponse(String(err));
    }

    // Bun's fetch decompresses the body itself; drop encoding/length headers
    // so the Go side does not double-decode or mis-frame the stream.
    const outHeaders = new Headers();
    upstream.headers.forEach((value, name) => {
      switch (name.toLowerCase()) {
        case "content-encoding":
        case "content-length":
        case "transfer-encoding":
        case "connection":
          return;
        default:
          outHeaders.append(name, value);
      }
    });

    return new Response(upstream.body, { status: upstream.status, headers: outHeaders });
  },
});

console.log(`zen-sidecar listening ${server.port}`);

// Exit when the parent Go process goes away (its end of our stdin closes).
process.stdin.resume();
process.stdin.on("end", () => process.exit(0));
process.stdin.on("close", () => process.exit(0));
