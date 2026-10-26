package proxy

import (
	"bufio"
	"bytes"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"testing"
	"time"
)

func testBunPath(t *testing.T) string {
	t.Helper()
	if path := os.Getenv("ZEN_BUN_PATH"); path != "" {
		return path
	}
	path, err := exec.LookPath("bun")
	if err != nil {
		t.Skip("bun not available")
	}
	return path
}

func TestOrderedSidecarHeadersSkipsTransportManaged(t *testing.T) {
	header := http.Header{}
	header.Set("Host", "opencode.ai")
	header.Set("Content-Length", "10")
	header.Set("Accept-Encoding", "gzip")
	header.Set("Connection", "keep-alive")
	header.Set("Authorization", "Bearer public")
	header.Set("Content-Type", "application/json")
	header.Set("User-Agent", "opencode/1.19.4 ai-sdk/provider-utils/4.0.23 runtime/bun/1.4.4")
	header.Set("x-opencode-session-id", "ses_x")
	header.Set("x-opencode-session", "ses_x")
	header.Set("x-opencode-project", "global")
	header.Set("x-opencode-request", "msg_y")
	header.Set("x-opencode-client", "cli")
	header.Set("X-Custom", "extra")

	got := orderedSidecarHeaders(header)
	names := make([]string, len(got))
	for i, pair := range got {
		names[i] = pair[0]
	}
	want := []string{
		"Authorization", "Content-Type", "User-Agent",
		"x-opencode-client", "x-opencode-project", "x-opencode-request",
		"x-opencode-session", "x-opencode-session-id",
		"X-Custom",
	}
	if strings.Join(names, ",") != strings.Join(want, ",") {
		t.Fatalf("header order = %v, want %v", names, want)
	}
}

func TestBunSidecarTransportSurfacesErrors(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("x-zen-sidecar-error", "1")
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadGateway)
		_, _ = w.Write([]byte(`{"error":"boom"}`))
	}))
	defer server.Close()

	transport := &bunSidecarTransport{endpoint: server.URL, local: sidecarLocalClient()}
	req, _ := http.NewRequest(http.MethodPost, "http://example.invalid/v1/chat/completions", strings.NewReader("{}"))
	_, err := transport.RoundTrip(req)
	if err == nil || !strings.Contains(err.Error(), "boom") {
		t.Fatalf("err = %v, want sidecar error", err)
	}
}

func TestBunSidecarTransportRestartsDeadSidecar(t *testing.T) {
	bunPath := testBunPath(t)
	resetBunSidecarForTest()
	if _, err := bunSidecarEndpoint(bunPath); err != nil {
		t.Fatalf("starting sidecar: %v", err)
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("ok"))
	}))
	defer server.Close()

	transport := &bunSidecarTransport{endpoint: "http://127.0.0.1:1", bunPath: bunPath, local: sidecarLocalClient()}
	req, _ := http.NewRequest(http.MethodPost, server.URL, strings.NewReader(`{}`))
	req.Header.Set("Authorization", "Bearer public")
	resp, err := transport.RoundTrip(req)
	if err != nil {
		t.Fatalf("self-heal roundtrip: %v", err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil || string(data) != "ok" {
		t.Fatalf("body = %q, err = %v", data, err)
	}
}

func TestBunSidecarTransportPreservesHeaderOrderAndStreams(t *testing.T) {
	bunPath := testBunPath(t)
	resetBunSidecarForTest()
	endpoint, err := bunSidecarEndpoint(bunPath)
	if err != nil {
		t.Fatalf("starting sidecar: %v", err)
	}

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()

	type capture struct {
		raw []byte
		err error
	}
	captured := make(chan capture, 1)
	responseBody := "data: hello world ok\n\n"
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			captured <- capture{err: err}
			return
		}
		defer conn.Close()
		reader := bufio.NewReader(conn)
		var raw bytes.Buffer
		contentLength := 0
		for {
			line, err := reader.ReadString('\n')
			if err != nil {
				captured <- capture{err: err}
				return
			}
			raw.WriteString(line)
			if strings.HasPrefix(strings.ToLower(line), "content-length:") {
				value := strings.TrimSpace(strings.SplitN(line, ":", 2)[1])
				contentLength, _ = strconv.Atoi(value)
			}
			if line == "\r\n" {
				break
			}
		}
		body := make([]byte, contentLength)
		if _, err := io.ReadFull(reader, body); err != nil {
			captured <- capture{err: err}
			return
		}
		raw.Write(body)
		_, _ = conn.Write([]byte("HTTP/1.1 200 OK\r\nContent-Type: text/event-stream\r\nContent-Length: " +
			strconv.Itoa(len(responseBody)) + "\r\n\r\n" + responseBody))
		captured <- capture{raw: raw.Bytes()}
	}()

	transport := &bunSidecarTransport{endpoint: endpoint, local: sidecarLocalClient()}
	body := `{"model":"deepseek-v4.1-flash-free","messages":[{"role":"user","content":"hi"}],"stream":true}`
	req, err := http.NewRequest(http.MethodPost, "http://"+ln.Addr().String()+"/inference/openai/v1/chat/completions", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer public")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "opencode/1.19.4 ai-sdk/provider-utils/4.0.23 runtime/bun/1.4.4")
	req.Header.Set("x-opencode-client", "cli")
	req.Header.Set("x-opencode-project", "global")
	req.Header.Set("x-opencode-request", "msg_16f1daeb2001tVp82lthWz9s6E")
	req.Header.Set("x-opencode-session", "ses_e90e25160ffeb1Mfwfne9S3LbN")
	req.Header.Set("x-opencode-session-id", "ses_e90e25160ffeb1Mfwfne9S3LbN")

	resp, err := transport.RoundTrip(req)
	if err != nil {
		t.Fatalf("roundtrip: %v", err)
	}
	data, err := io.ReadAll(resp.Body)
	resp.Body.Close()
	if err != nil {
		t.Fatalf("reading body: %v", err)
	}
	if resp.StatusCode != http.StatusOK || string(data) != responseBody {
		t.Fatalf("status = %d, body = %q", resp.StatusCode, data)
	}

	select {
	case cap := <-captured:
		if cap.err != nil {
			t.Fatalf("capture: %v", cap.err)
		}
		headerBlock := string(cap.raw)
		headerEnd := strings.Index(headerBlock, "\r\n\r\n")
		lines := strings.Split(headerBlock[:headerEnd], "\r\n")
		var order []string
		for _, line := range lines[1:] {
			name := strings.ToLower(strings.SplitN(line, ":", 2)[0])
			order = append(order, name)
		}
		want := []string{
			"authorization", "content-type", "user-agent",
			"x-opencode-client", "x-opencode-project", "x-opencode-request",
			"x-opencode-session", "x-opencode-session-id",
		}
		if len(order) < len(want) {
			t.Fatalf("too few headers on wire: %v", order)
		}
		for i, name := range want {
			if order[i] != name {
				t.Fatalf("wire header %d = %q, want %q (full order %v)", i, order[i], name, order)
			}
		}
		if !strings.Contains(headerBlock, body) {
			t.Fatal("request body was not forwarded")
		}
	case <-time.After(10 * time.Second):
		t.Fatal("mock upstream never received the request")
	}
}
