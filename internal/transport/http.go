package transport

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	mcperrors "github.com/JSLEEKR/mcpbench/internal/errors"
	"github.com/JSLEEKR/mcpbench/internal/jsonrpc"
)

// HTTPConfig configures the HTTP/SSE transport.
type HTTPConfig struct {
	URL     string
	Headers map[string]string
	// Client overrides the default http.Client. Optional.
	Client *http.Client
	// AllowSSE enables parsing of text/event-stream responses. When the
	// server sends SSE, the transport collates all `data:` events into a
	// single JSON-RPC response (the last well-formed frame wins).
	AllowSSE bool
}

// HTTP is an HTTP/SSE-based JSON-RPC transport.
type HTTP struct {
	cfg    HTTPConfig
	client *http.Client
	ids    *jsonrpc.IDPool

	closeMu sync.Mutex
	closed  bool
}

// NewHTTP constructs a new HTTP transport.
func NewHTTP(cfg HTTPConfig) (*HTTP, error) {
	if strings.TrimSpace(cfg.URL) == "" {
		return nil, fmt.Errorf("http: url required")
	}
	if !strings.HasPrefix(cfg.URL, "http://") && !strings.HasPrefix(cfg.URL, "https://") {
		return nil, fmt.Errorf("http: url must start with http:// or https://")
	}
	client := cfg.Client
	if client == nil {
		client = &http.Client{
			Timeout: 0, // governed by ctx per-call
			Transport: &http.Transport{
				MaxIdleConns:        100,
				MaxIdleConnsPerHost: 100,
				IdleConnTimeout:     90 * time.Second,
			},
		}
	}
	return &HTTP{cfg: cfg, client: client, ids: jsonrpc.NewIDPool()}, nil
}

// Call issues a JSON-RPC call over HTTP. When the server returns
// text/event-stream and AllowSSE is set, frames are collated.
func (h *HTTP) Call(ctx context.Context, method string, params any) ([]byte, error) {
	h.closeMu.Lock()
	if h.closed {
		h.closeMu.Unlock()
		return nil, &mcperrors.TransportError{Inner: fmt.Errorf("http: transport closed")}
	}
	h.closeMu.Unlock()

	id := h.ids.Next()
	req := jsonrpc.NewRequest(id, method, params)
	body, err := req.Marshal()
	if err != nil {
		return nil, &mcperrors.TransportError{Inner: fmt.Errorf("http: marshal: %w", err)}
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, h.cfg.URL, bytes.NewReader(body))
	if err != nil {
		return nil, &mcperrors.TransportError{Inner: err}
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept", "application/json, text/event-stream")
	for k, v := range h.cfg.Headers {
		httpReq.Header.Set(k, v)
	}

	resp, err := h.client.Do(httpReq)
	if err != nil {
		if ctx.Err() != nil {
			return nil, &mcperrors.TimeoutError{Inner: err}
		}
		return nil, &mcperrors.TransportError{Inner: err}
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		// Drain to free the connection for keep-alive.
		_, _ = io.Copy(io.Discard, resp.Body)
		return nil, &mcperrors.TransportError{Inner: fmt.Errorf("http: status %d", resp.StatusCode)}
	}

	ct := resp.Header.Get("Content-Type")
	if h.cfg.AllowSSE && strings.HasPrefix(ct, "text/event-stream") {
		return parseSSE(resp.Body, id)
	}

	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		if ctx.Err() != nil {
			return nil, &mcperrors.TimeoutError{Inner: err}
		}
		return nil, &mcperrors.TransportError{Inner: err}
	}
	return raw, nil
}

// Close releases underlying HTTP connections.
func (h *HTTP) Close() error {
	h.closeMu.Lock()
	defer h.closeMu.Unlock()
	if h.closed {
		return nil
	}
	h.closed = true
	if tr, ok := h.client.Transport.(*http.Transport); ok {
		tr.CloseIdleConnections()
	}
	return nil
}

// parseSSE reads text/event-stream events and returns the last well-formed
// JSON-RPC frame whose id matches the expected id. A frame with
// "event: done" signals the stream's end.
func parseSSE(r io.Reader, expectedID int64) ([]byte, error) {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)
	var current []byte
	var matched []byte
	var event string

	flush := func() {
		defer func() {
			event = ""
			current = nil
		}()
		if len(current) == 0 {
			return
		}
		// Attempt to parse this frame's data.
		var peek struct {
			ID int64 `json:"id"`
		}
		if err := json.Unmarshal(current, &peek); err != nil {
			return
		}
		if peek.ID == expectedID {
			cp := make([]byte, len(current))
			copy(cp, current)
			matched = cp
		}
	}

	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			flush()
			if event == "done" {
				break
			}
			continue
		}
		if strings.HasPrefix(line, ":") {
			continue // comment
		}
		if strings.HasPrefix(line, "event:") {
			event = strings.TrimSpace(strings.TrimPrefix(line, "event:"))
			continue
		}
		if strings.HasPrefix(line, "data:") {
			data := strings.TrimPrefix(line, "data:")
			data = strings.TrimPrefix(data, " ")
			if len(current) > 0 {
				current = append(current, '\n')
			}
			current = append(current, []byte(data)...)
		}
	}
	flush()
	if err := scanner.Err(); err != nil {
		return nil, &mcperrors.TransportError{Inner: err}
	}
	if matched == nil {
		return nil, &mcperrors.TransportError{Inner: fmt.Errorf("sse: no matching frame for id %d", expectedID)}
	}
	return matched, nil
}
