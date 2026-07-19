package proxy

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const (
	debugHistoryLimit = 100
	debugBodyLimit    = 128 * 1024
)

type traceContextKey struct{}

// RequestTrace is a redacted snapshot of one client request and its upstream call.
// It stays in memory only and is exposed through --verbose and --tui.
type RequestTrace struct {
	ID               uint64
	StartedAt        time.Time
	Duration         time.Duration
	Protocol         string
	Model            string
	Inbound          TracePayload
	Upstream         TracePayload
	UpstreamResponse TracePayload
	Response         TracePayload
	Error            string
}

type TracePayload struct {
	Method    string
	URL       string
	Status    int
	Headers   map[string]string
	Body      string
	Truncated bool
}

type Debugger struct {
	verbose bool
	logger  *log.Logger

	nextID atomic.Uint64

	mu          sync.RWMutex
	traces      []RequestTrace
	subscribers map[chan RequestTrace]struct{}
}

func NewDebugger(verbose bool) *Debugger {
	return &Debugger{
		verbose:     verbose,
		logger:      log.Default(),
		subscribers: map[chan RequestTrace]struct{}{},
	}
}

func (d *Debugger) Begin(r *http.Request) (context.Context, uint64) {
	id := d.nextID.Add(1)
	trace := RequestTrace{
		ID:        id,
		StartedAt: time.Now(),
		Inbound: TracePayload{
			Method:  r.Method,
			URL:     sanitizeURL(r.URL.String()),
			Headers: sanitizeHeaders(r.Header),
		},
	}

	d.mu.Lock()
	d.traces = append(d.traces, trace)
	if len(d.traces) > debugHistoryLimit {
		d.traces = d.traces[len(d.traces)-debugHistoryLimit:]
	}
	d.mu.Unlock()
	d.publish(trace)

	return context.WithValue(r.Context(), traceContextKey{}, id), id
}

func (d *Debugger) SetRequest(ctx context.Context, protocol, model string) {
	d.update(traceID(ctx), func(trace *RequestTrace) {
		trace.Protocol = protocol
		trace.Model = model
	})
}

func (d *Debugger) SetInboundBody(id uint64, body []byte, truncated bool) {
	payload, wasTruncated := sanitizeBody(body, truncated)
	d.update(id, func(trace *RequestTrace) {
		trace.Inbound.Body = payload
		trace.Inbound.Truncated = wasTruncated
	})
}

func (d *Debugger) RecordUpstreamRequest(ctx context.Context, request *http.Request, body []byte) {
	payload, truncated := sanitizeBody(body, false)
	d.update(traceID(ctx), func(trace *RequestTrace) {
		trace.Upstream = TracePayload{
			Method:    request.Method,
			URL:       sanitizeURL(request.URL.String()),
			Headers:   sanitizeHeaders(request.Header),
			Body:      payload,
			Truncated: truncated,
		}
	})
}

func (d *Debugger) RecordUpstreamResponse(ctx context.Context, response *http.Response) {
	d.update(traceID(ctx), func(trace *RequestTrace) {
		trace.UpstreamResponse.Status = response.StatusCode
		trace.UpstreamResponse.Headers = sanitizeHeaders(response.Header)
	})
}

func (d *Debugger) RecordUpstreamBody(ctx context.Context, body []byte, truncated bool) {
	payload, wasTruncated := sanitizeBody(body, truncated)
	d.update(traceID(ctx), func(trace *RequestTrace) {
		trace.UpstreamResponse.Body = payload
		trace.UpstreamResponse.Truncated = wasTruncated
	})
}

func (d *Debugger) RecordUpstreamError(ctx context.Context, err error) {
	d.update(traceID(ctx), func(trace *RequestTrace) {
		trace.Error = err.Error()
	})
}

func (d *Debugger) Finish(id uint64, response TracePayload) {
	payload, truncated := sanitizeBody([]byte(response.Body), response.Truncated)
	response.Body = payload
	response.Truncated = truncated
	response.Headers = sanitizeHeadersMap(response.Headers)

	d.update(id, func(trace *RequestTrace) {
		trace.Response = response
		trace.Duration = time.Since(trace.StartedAt)
	})

	if d.verbose {
		trace, ok := d.Trace(id)
		if ok {
			d.logTrace(trace)
		}
	}
}

func (d *Debugger) Trace(id uint64) (RequestTrace, bool) {
	d.mu.RLock()
	defer d.mu.RUnlock()
	for i := range d.traces {
		if d.traces[i].ID == id {
			return cloneTrace(d.traces[i]), true
		}
	}
	return RequestTrace{}, false
}

func (d *Debugger) Snapshot() []RequestTrace {
	d.mu.RLock()
	defer d.mu.RUnlock()
	result := make([]RequestTrace, len(d.traces))
	for i := range d.traces {
		result[i] = cloneTrace(d.traces[i])
	}
	return result
}

func (d *Debugger) Subscribe() (<-chan RequestTrace, func()) {
	ch := make(chan RequestTrace, 64)
	d.mu.Lock()
	d.subscribers[ch] = struct{}{}
	d.mu.Unlock()

	return ch, func() {
		d.mu.Lock()
		if _, ok := d.subscribers[ch]; ok {
			delete(d.subscribers, ch)
			close(ch)
		}
		d.mu.Unlock()
	}
}

func (d *Debugger) update(id uint64, change func(*RequestTrace)) {
	if id == 0 {
		return
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	for i := range d.traces {
		if d.traces[i].ID == id {
			change(&d.traces[i])
			d.publishLocked(d.traces[i])
			return
		}
	}
}

func (d *Debugger) publish(trace RequestTrace) {
	d.mu.RLock()
	defer d.mu.RUnlock()
	d.publishLocked(trace)
}

func (d *Debugger) publishLocked(trace RequestTrace) {
	for ch := range d.subscribers {
		select {
		case ch <- cloneTrace(trace):
		default:
		}
	}
}

func (d *Debugger) logTrace(trace RequestTrace) {
	d.logger.Printf(
		"trace #%d %s %s protocol=%s model=%s upstream_status=%d status=%d duration=%s",
		trace.ID,
		trace.Inbound.Method,
		trace.Inbound.URL,
		trace.Protocol,
		trace.Model,
		trace.UpstreamResponse.Status,
		trace.Response.Status,
		trace.Duration.Round(time.Millisecond),
	)
	d.logPayload(trace.ID, "inbound", trace.Inbound)
	d.logPayload(trace.ID, "upstream request", trace.Upstream)
	d.logPayload(trace.ID, "upstream response", trace.UpstreamResponse)
	d.logPayload(trace.ID, "response", trace.Response)
	if trace.Error != "" {
		d.logger.Printf("trace #%d error: %s", trace.ID, trace.Error)
	}
}

func (d *Debugger) logPayload(id uint64, label string, payload TracePayload) {
	if payload.Body == "" {
		return
	}
	suffix := ""
	if payload.Truncated {
		suffix = " [truncated]"
	}
	d.logger.Printf("trace #%d %s body%s:\n%s", id, label, suffix, payload.Body)
}

func traceID(ctx context.Context) uint64 {
	id, _ := ctx.Value(traceContextKey{}).(uint64)
	return id
}

func cloneTrace(trace RequestTrace) RequestTrace {
	trace.Inbound.Headers = cloneHeaders(trace.Inbound.Headers)
	trace.Upstream.Headers = cloneHeaders(trace.Upstream.Headers)
	trace.UpstreamResponse.Headers = cloneHeaders(trace.UpstreamResponse.Headers)
	trace.Response.Headers = cloneHeaders(trace.Response.Headers)
	return trace
}

func cloneHeaders(headers map[string]string) map[string]string {
	if len(headers) == 0 {
		return nil
	}
	result := make(map[string]string, len(headers))
	for key, value := range headers {
		result[key] = value
	}
	return result
}

func sanitizeHeaders(headers http.Header) map[string]string {
	result := make(map[string]string, len(headers))
	for key, values := range headers {
		result[key] = strings.Join(values, ", ")
	}
	return sanitizeHeadersMap(result)
}

func sanitizeHeadersMap(headers map[string]string) map[string]string {
	if len(headers) == 0 {
		return nil
	}
	result := make(map[string]string, len(headers))
	for key, value := range headers {
		if isSecretKey(key) {
			result[key] = "[redacted]"
			continue
		}
		result[key] = value
	}
	return result
}

func sanitizeBody(body []byte, alreadyTruncated bool) (string, bool) {
	truncated := alreadyTruncated
	if len(body) > debugBodyLimit {
		body = body[:debugBodyLimit]
		truncated = true
	}
	if len(body) == 0 {
		return "", truncated
	}

	var value any
	if err := json.Unmarshal(body, &value); err == nil {
		redactJSON(value)
		if pretty, err := json.MarshalIndent(value, "", "  "); err == nil {
			return string(pretty), truncated
		}
	}
	return string(body), truncated
}

func redactJSON(value any) {
	switch v := value.(type) {
	case map[string]any:
		for key, item := range v {
			if isSecretKey(key) {
				v[key] = "[redacted]"
				continue
			}
			redactJSON(item)
		}
	case []any:
		for _, item := range v {
			redactJSON(item)
		}
	}
}

func isSecretKey(key string) bool {
	key = strings.ToLower(strings.ReplaceAll(key, "-", "_"))
	return key == "authorization" || key == "proxy_authorization" || key == "x_api_key" || key == "api_key" ||
		strings.Contains(key, "access_token") || strings.Contains(key, "refresh_token") || strings.Contains(key, "secret") ||
		strings.HasSuffix(key, "_key") || key == "key"
}

func sanitizeURL(raw string) string {
	parsed, err := url.Parse(raw)
	if err != nil {
		return raw
	}
	parsed.User = nil
	query := parsed.Query()
	for key := range query {
		if isSecretKey(key) {
			query.Set(key, "[redacted]")
		}
	}
	parsed.RawQuery = query.Encode()
	return parsed.String()
}

type captureReadCloser struct {
	reader    io.ReadCloser
	limit     int
	buffer    bytes.Buffer
	truncated bool
	onClose   func([]byte, bool)
	once      sync.Once
}

func newCaptureReadCloser(reader io.ReadCloser, limit int, onClose func([]byte, bool)) *captureReadCloser {
	return &captureReadCloser{reader: reader, limit: limit, onClose: onClose}
}

func (r *captureReadCloser) Read(p []byte) (int, error) {
	n, err := r.reader.Read(p)
	if n > 0 {
		r.capture(p[:n])
	}
	return n, err
}

func (r *captureReadCloser) Close() error {
	r.once.Do(func() {
		if r.onClose != nil {
			r.onClose(r.buffer.Bytes(), r.truncated)
		}
	})
	return r.reader.Close()
}

func (r *captureReadCloser) capture(data []byte) {
	remaining := r.limit - r.buffer.Len()
	if remaining <= 0 {
		r.truncated = true
		return
	}
	if len(data) > remaining {
		r.buffer.Write(data[:remaining])
		r.truncated = true
		return
	}
	r.buffer.Write(data)
}

type traceResponseWriter struct {
	http.ResponseWriter
	capture *captureReadCloser
	status  int
}

func newTraceResponseWriter(writer http.ResponseWriter) *traceResponseWriter {
	return &traceResponseWriter{
		ResponseWriter: writer,
		capture:        newCaptureReadCloser(io.NopCloser(strings.NewReader("")), debugBodyLimit, nil),
	}
}

func (w *traceResponseWriter) WriteHeader(status int) {
	if w.status == 0 {
		w.status = status
	}
	w.ResponseWriter.WriteHeader(status)
}

func (w *traceResponseWriter) Write(data []byte) (int, error) {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	w.capture.capture(data)
	return w.ResponseWriter.Write(data)
}

func (w *traceResponseWriter) Flush() {
	if flusher, ok := w.ResponseWriter.(http.Flusher); ok {
		flusher.Flush()
	}
}

func (w *traceResponseWriter) Unwrap() http.ResponseWriter {
	return w.ResponseWriter
}

func (w *traceResponseWriter) payload() TracePayload {
	status := w.status
	if status == 0 {
		status = http.StatusOK
	}
	return TracePayload{
		Status:    status,
		Headers:   sanitizeHeaders(w.Header()),
		Body:      w.capture.buffer.String(),
		Truncated: w.capture.truncated,
	}
}

func withDebug(next http.Handler, debugger *Debugger) http.Handler {
	if debugger == nil {
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx, id := debugger.Begin(r)
		capture := newCaptureReadCloser(r.Body, debugBodyLimit, nil)
		r = r.WithContext(ctx)
		r.Body = capture
		response := newTraceResponseWriter(w)

		next.ServeHTTP(response, r)
		debugger.SetInboundBody(id, capture.buffer.Bytes(), capture.truncated)
		debugger.Finish(id, response.payload())
	})
}

func traceHeaders(headers map[string]string) string {
	if len(headers) == 0 {
		return ""
	}
	keys := make([]string, 0, len(headers))
	for key := range headers {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	var parts []string
	for _, key := range keys {
		parts = append(parts, fmt.Sprintf("%s: %s", key, headers[key]))
	}
	return strings.Join(parts, "\n")
}
