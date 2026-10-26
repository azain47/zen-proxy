package proxy

import (
	"bufio"
	"bytes"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"
)

// The Bun upstream transport runs the real Bun runtime (the engine the
// OpenCode client itself uses) as a local sidecar and lets it perform the
// upstream request. Unlike the uTLS dialer, which reimplements Bun's
// ClientHello in Go, this presents Bun's genuine TLS and HTTP stack, so no
// fingerprint detail can drift.

//go:embed bun_sidecar.ts
var bunSidecarScript string

const sidecarReadyPrefix = "zen-sidecar listening"

var (
	bunMu       sync.Mutex
	bunEndpoint string
	bunStartErr error
	bunFallback sync.Once
	// bunStdin keeps the sidecar's stdin pipe open. When this process exits
	// the pipe closes and the sidecar exits with it.
	bunStdin io.WriteCloser

	bunClientMu sync.Mutex
	bunClient   *http.Client
)

// upstreamHTTPClient returns the HTTP client used for the upstream hop. For
// Zen upstreams it prefers the Bun sidecar (genuine client fingerprint) and
// falls back to the uTLS dialer when Bun is unavailable or the transport is
// forced to "utls".
func upstreamHTTPClient(cfg Config) *http.Client {
	if cfg.Provider != providerZen {
		return proxyHTTPClient
	}
	switch strings.ToLower(strings.TrimSpace(cfg.UpstreamTransport)) {
	case "", "utls", "go":
		return proxyHTTPClient
	case "auto", "bun":
		endpoint, err := bunSidecarEndpoint(cfg.BunPath)
		if err != nil {
			bunFallback.Do(func() {
				fmt.Fprintf(os.Stderr, "zen-proxy: bun upstream transport unavailable (%v); using uTLS fingerprint\n", err)
			})
			return proxyHTTPClient
		}
		bunClientMu.Lock()
		defer bunClientMu.Unlock()
		if bunClient == nil {
			bunClient = &http.Client{Transport: &bunSidecarTransport{endpoint: endpoint, bunPath: cfg.BunPath, local: sidecarLocalClient()}}
		}
		return bunClient
	default:
		return proxyHTTPClient
	}
}

func sidecarLocalClient() *http.Client {
	return &http.Client{
		Transport: &http.Transport{
			Proxy:               nil,
			ForceAttemptHTTP2:   false,
			MaxIdleConnsPerHost: 8,
		},
	}
}

func bunSidecarEndpoint(bunPath string) (string, error) {
	bunMu.Lock()
	defer bunMu.Unlock()
	if bunEndpoint != "" {
		return bunEndpoint, nil
	}
	if bunStartErr != nil {
		return "", bunStartErr
	}
	endpoint, err := startBunSidecar(bunPath)
	if err != nil {
		bunStartErr = err
		return "", err
	}
	bunEndpoint = endpoint
	return endpoint, nil
}

func resetBunSidecarForTest() {
	invalidateBunSidecar()
	bunFallback = sync.Once{}
	bunClientMu.Lock()
	bunClient = nil
	bunClientMu.Unlock()
}

func invalidateBunSidecar() {
	bunMu.Lock()
	defer bunMu.Unlock()
	if bunStdin != nil {
		bunStdin.Close()
		bunStdin = nil
	}
	bunEndpoint, bunStartErr = "", nil
}

func resolveBunBinary(explicit string) (string, error) {
	if explicit == "" {
		found, err := exec.LookPath("bun")
		if err != nil {
			return "", errors.New("no bun binary found (install bun or set ZEN_BUN_PATH)")
		}
		return found, nil
	}
	info, err := os.Stat(explicit)
	if err != nil {
		return "", fmt.Errorf("bun path: %w", err)
	}
	if info.IsDir() {
		candidate := filepath.Join(explicit, "bin", "bun")
		if _, err := os.Stat(candidate); err != nil {
			return "", fmt.Errorf("bun path %s is a directory without bin/bun", explicit)
		}
		return candidate, nil
	}
	return explicit, nil
}

type stderrBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *stderrBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.buf.Len() < 64*1024 {
		b.buf.Write(p)
	}
	return len(p), nil
}

func (b *stderrBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return strings.TrimSpace(b.buf.String())
}

func startBunSidecar(bunPath string) (string, error) {
	binary, err := resolveBunBinary(bunPath)
	if err != nil {
		return "", err
	}

	scriptPath := filepath.Join(os.TempDir(), fmt.Sprintf("zen-proxy-bun-sidecar-%d.ts", os.Getpid()))
	if err := os.WriteFile(scriptPath, []byte(bunSidecarScript), 0o600); err != nil {
		return "", fmt.Errorf("writing sidecar script: %w", err)
	}

	cmd := exec.Command(binary, scriptPath)
	cmd.Env = os.Environ()
	stderr := &stderrBuffer{}
	cmd.Stderr = stderr
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return "", err
	}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return "", err
	}
	if err := cmd.Start(); err != nil {
		return "", fmt.Errorf("starting bun sidecar: %w", err)
	}

	portCh := make(chan string, 1)
	go func() {
		scanner := bufio.NewScanner(stdout)
		for scanner.Scan() {
			line := strings.TrimSpace(scanner.Text())
			if strings.HasPrefix(line, sidecarReadyPrefix) {
				port := strings.TrimSpace(strings.TrimPrefix(line, sidecarReadyPrefix))
				if port != "" {
					portCh <- port
				}
			}
		}
		portCh <- ""
	}()

	select {
	case port := <-portCh:
		if port == "" {
			_ = stdin.Close()
			_ = cmd.Wait()
			return "", fmt.Errorf("bun sidecar exited before ready: %s", stderr.String())
		}
		endpoint := "http://127.0.0.1:" + port
		if err := waitForSidecar(endpoint, 10*time.Second); err != nil {
			_ = stdin.Close()
			_ = cmd.Wait()
			return "", err
		}
		_ = os.Remove(scriptPath)
		// Keep stdin open for the lifetime of the process; when this
		// process exits the pipe closes and the sidecar exits with it.
		bunStdin = stdin
		return endpoint, nil
	case <-time.After(15 * time.Second):
		_ = stdin.Close()
		_ = cmd.Process.Kill()
		return "", fmt.Errorf("bun sidecar did not become ready: %s", stderr.String())
	}
}

func waitForSidecar(endpoint string, timeout time.Duration) error {
	client := sidecarLocalClient()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		resp, err := client.Get(endpoint + "/healthz")
		if err == nil {
			_, _ = io.Copy(io.Discard, resp.Body)
			resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return nil
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	return errors.New("bun sidecar health check timed out")
}

type bunSidecarEnvelope struct {
	URL     string      `json:"url"`
	Method  string      `json:"method"`
	Headers [][2]string `json:"headers"`
	Body    string      `json:"body"`
}

type bunSidecarTransport struct {
	mu       sync.Mutex
	endpoint string
	bunPath  string
	local    *http.Client
}

func (t *bunSidecarTransport) getEndpoint() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.endpoint
}

func (t *bunSidecarTransport) setEndpoint(endpoint string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.endpoint = endpoint
}

// zenAppHeaderOrder is the order the genuine OpenCode client writes its
// application headers to the wire (captured from opencode 1.19.4); Bun
// appends its transport headers after these.
var zenAppHeaderOrder = []string{
	"Authorization",
	"Content-Type",
	"User-Agent",
	"x-opencode-client",
	"x-opencode-project",
	"x-opencode-request",
	"x-opencode-session",
	"x-opencode-session-id",
}

// Headers the local HTTP hop re-adds; Bun's fetch sets its own.
var zenTransportManagedHeaders = map[string]bool{
	"Host":              true,
	"Content-Length":    true,
	"Connection":        true,
	"Accept":            true,
	"Accept-Encoding":   true,
	"Transfer-Encoding": true,
	"Keep-Alive":        true,
}

func orderedSidecarHeaders(header http.Header) [][2]string {
	out := make([][2]string, 0, len(header))
	seen := make(map[string]bool, len(header))
	for _, name := range zenAppHeaderOrder {
		values, ok := header[http.CanonicalHeaderKey(name)]
		if !ok {
			continue
		}
		seen[http.CanonicalHeaderKey(name)] = true
		for _, value := range values {
			out = append(out, [2]string{name, value})
		}
	}
	rest := make([]string, 0, len(header))
	for name := range header {
		if seen[name] || zenTransportManagedHeaders[name] {
			continue
		}
		rest = append(rest, name)
	}
	sort.Strings(rest)
	for _, name := range rest {
		for _, value := range header[name] {
			out = append(out, [2]string{name, value})
		}
	}
	return out
}

func (t *bunSidecarTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	var body string
	if req.Body != nil {
		data, err := io.ReadAll(req.Body)
		req.Body.Close()
		if err != nil {
			return nil, err
		}
		body = string(data)
	}

	envelope := bunSidecarEnvelope{
		URL:     req.URL.String(),
		Method:  req.Method,
		Headers: orderedSidecarHeaders(req.Header),
		Body:    body,
	}
	payload, err := json.Marshal(envelope)
	if err != nil {
		return nil, err
	}

	resp, err := t.forward(req, payload)
	if err == nil || !errors.Is(err, syscall.ECONNREFUSED) {
		return resp, err
	}
	// The sidecar died (crash, kill, or pipe close); restart it once and
	// retry the request against the fresh endpoint.
	invalidateBunSidecar()
	endpoint, restartErr := bunSidecarEndpoint(t.bunPath)
	if restartErr != nil {
		return nil, err
	}
	t.setEndpoint(endpoint)
	return t.forward(req, payload)
}

func (t *bunSidecarTransport) forward(req *http.Request, payload []byte) (*http.Response, error) {
	localReq, err := http.NewRequestWithContext(req.Context(), http.MethodPost, t.getEndpoint()+"/forward", bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	localReq.Header.Set("Content-Type", "application/json")

	resp, err := t.local.Do(localReq)
	if err != nil {
		return nil, err
	}
	if resp.Header.Get("x-zen-sidecar-error") == "1" {
		defer resp.Body.Close()
		data, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return nil, fmt.Errorf("bun sidecar upstream error: %s", strings.TrimSpace(string(data)))
	}
	resp.Header.Del("x-zen-sidecar-error")
	resp.Request = req
	return resp, nil
}
