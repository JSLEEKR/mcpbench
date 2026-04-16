package transport

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	mcperrors "github.com/JSLEEKR/mcpbench/internal/errors"
)

func TestHTTPOKResponse(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		resp := map[string]any{
			"jsonrpc": "2.0",
			"id":      body["id"],
			"result":  map[string]bool{"ok": true},
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer srv.Close()
	tr, err := NewHTTP(HTTPConfig{URL: srv.URL})
	if err != nil {
		t.Fatal(err)
	}
	defer tr.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	raw, err := tr.Call(ctx, "ping", nil)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"ok":true`) {
		t.Fatalf("got %s", raw)
	}
}

func TestHTTPSendsJSONRPCEnvelope(t *testing.T) {
	var body []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"jsonrpc":"2.0","id":1}`)
	}))
	defer srv.Close()
	tr, _ := NewHTTP(HTTPConfig{URL: srv.URL})
	defer tr.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_, err := tr.Call(ctx, "tools/call", map[string]any{"name": "x"})
	if err != nil {
		t.Fatal(err)
	}
	var req map[string]any
	if err := json.Unmarshal(body, &req); err != nil {
		t.Fatal(err)
	}
	if req["jsonrpc"] != "2.0" {
		t.Fatal("missing jsonrpc version")
	}
	if req["method"] != "tools/call" {
		t.Fatal("method wrong")
	}
	if req["id"] == nil {
		t.Fatal("missing id")
	}
}

func TestHTTPCustomHeaders(t *testing.T) {
	var authz string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		authz = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"jsonrpc":"2.0","id":1}`)
	}))
	defer srv.Close()
	tr, _ := NewHTTP(HTTPConfig{URL: srv.URL, Headers: map[string]string{"Authorization": "Bearer abc"}})
	defer tr.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_, err := tr.Call(ctx, "ping", nil)
	if err != nil {
		t.Fatal(err)
	}
	if authz != "Bearer abc" {
		t.Fatalf("authz = %s", authz)
	}
}

func TestHTTP4xxBecomesTransportError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "forbidden", http.StatusForbidden)
	}))
	defer srv.Close()
	tr, _ := NewHTTP(HTTPConfig{URL: srv.URL})
	defer tr.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_, err := tr.Call(ctx, "ping", nil)
	if err == nil {
		t.Fatal("expected error")
	}
	if mcperrors.Classify(err) != mcperrors.CategoryTransport {
		t.Fatalf("category = %s", mcperrors.Classify(err))
	}
}

func TestHTTP5xxBecomesTransportError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	defer srv.Close()
	tr, _ := NewHTTP(HTTPConfig{URL: srv.URL})
	defer tr.Close()
	_, err := tr.Call(context.Background(), "ping", nil)
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestHTTPConnectionRefused(t *testing.T) {
	// Port 1 is typically not listening.
	tr, _ := NewHTTP(HTTPConfig{URL: "http://127.0.0.1:1/"})
	defer tr.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Second)
	defer cancel()
	_, err := tr.Call(ctx, "ping", nil)
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestHTTPTimeoutBecomesTimeoutError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(500 * time.Millisecond)
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"jsonrpc":"2.0","id":1}`)
	}))
	defer srv.Close()
	tr, _ := NewHTTP(HTTPConfig{URL: srv.URL})
	defer tr.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	_, err := tr.Call(ctx, "ping", nil)
	if err == nil {
		t.Fatal("expected timeout")
	}
	if mcperrors.Classify(err) != mcperrors.CategoryTimeout {
		t.Fatalf("category = %s", mcperrors.Classify(err))
	}
}

func TestHTTPConcurrentCalls(t *testing.T) {
	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"jsonrpc":"2.0","id":%v}`, body["id"])
	}))
	defer srv.Close()
	tr, _ := NewHTTP(HTTPConfig{URL: srv.URL})
	defer tr.Close()
	const n = 50
	errCh := make(chan error, n)
	for i := 0; i < n; i++ {
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			_, err := tr.Call(ctx, "ping", nil)
			errCh <- err
		}()
	}
	for i := 0; i < n; i++ {
		if err := <-errCh; err != nil {
			t.Fatal(err)
		}
	}
	if atomic.LoadInt32(&hits) != n {
		t.Fatalf("hits = %d", hits)
	}
}

func TestHTTPRejectsInvalidURL(t *testing.T) {
	_, err := NewHTTP(HTTPConfig{URL: ""})
	if err == nil {
		t.Fatal("expected empty url error")
	}
	_, err = NewHTTP(HTTPConfig{URL: "ftp://x"})
	if err == nil {
		t.Fatal("expected scheme error")
	}
}

func TestHTTPClose(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"jsonrpc":"2.0","id":1}`)
	}))
	defer srv.Close()
	tr, _ := NewHTTP(HTTPConfig{URL: srv.URL})
	if err := tr.Close(); err != nil {
		t.Fatal(err)
	}
	if err := tr.Close(); err != nil {
		t.Fatal(err)
	}
	_, err := tr.Call(context.Background(), "ping", nil)
	if err == nil {
		t.Fatal("expected closed error")
	}
	var te *mcperrors.TransportError
	if !errors.As(err, &te) {
		t.Fatalf("type = %T", err)
	}
}

func TestHTTPSSESingleFrame(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		w.Header().Set("Content-Type", "text/event-stream")
		flusher, _ := w.(http.Flusher)
		fmt.Fprintf(w, "data: {\"jsonrpc\":\"2.0\",\"id\":%v,\"result\":{\"ok\":true}}\n\n", body["id"])
		if flusher != nil {
			flusher.Flush()
		}
		fmt.Fprintf(w, "event: done\n\n")
		if flusher != nil {
			flusher.Flush()
		}
	}))
	defer srv.Close()
	tr, _ := NewHTTP(HTTPConfig{URL: srv.URL, AllowSSE: true})
	defer tr.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	raw, err := tr.Call(ctx, "ping", nil)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"ok":true`) {
		t.Fatalf("got %s", raw)
	}
}

func TestHTTPSSEMultiEventLastWins(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		w.Header().Set("Content-Type", "text/event-stream")
		flusher := w.(http.Flusher)
		// Send an intermediate progress frame with a DIFFERENT id (notification)
		fmt.Fprint(w, "data: {\"jsonrpc\":\"2.0\",\"id\":-1,\"result\":{\"progress\":0.5}}\n\n")
		flusher.Flush()
		// Send the matching final frame
		fmt.Fprintf(w, "data: {\"jsonrpc\":\"2.0\",\"id\":%v,\"result\":{\"done\":true}}\n\n", body["id"])
		flusher.Flush()
		fmt.Fprint(w, "event: done\n\n")
		flusher.Flush()
	}))
	defer srv.Close()
	tr, _ := NewHTTP(HTTPConfig{URL: srv.URL, AllowSSE: true})
	defer tr.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	raw, err := tr.Call(ctx, "ping", nil)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"done":true`) {
		t.Fatalf("got %s", raw)
	}
}

func TestHTTPSSENoMatchingFrame(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		flusher := w.(http.Flusher)
		fmt.Fprint(w, "data: {\"jsonrpc\":\"2.0\",\"id\":999}\n\n")
		flusher.Flush()
		fmt.Fprint(w, "event: done\n\n")
		flusher.Flush()
	}))
	defer srv.Close()
	tr, _ := NewHTTP(HTTPConfig{URL: srv.URL, AllowSSE: true})
	defer tr.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_, err := tr.Call(ctx, "ping", nil)
	if err == nil {
		t.Fatal("expected no-matching-frame error")
	}
}

func TestHTTPSSECommentsIgnored(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		w.Header().Set("Content-Type", "text/event-stream")
		flusher := w.(http.Flusher)
		fmt.Fprint(w, ": keepalive\n\n")
		flusher.Flush()
		fmt.Fprintf(w, "data: {\"jsonrpc\":\"2.0\",\"id\":%v,\"result\":{\"ok\":true}}\n\n", body["id"])
		flusher.Flush()
	}))
	defer srv.Close()
	tr, _ := NewHTTP(HTTPConfig{URL: srv.URL, AllowSSE: true})
	defer tr.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	raw, err := tr.Call(ctx, "ping", nil)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"ok":true`) {
		t.Fatal("comment mis-handled")
	}
}

func TestHTTPSSEMultiLineData(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		w.Header().Set("Content-Type", "text/event-stream")
		flusher := w.(http.Flusher)
		fmt.Fprintf(w, "data: {\"jsonrpc\":\"2.0\",\n")
		fmt.Fprintf(w, "data: \"id\":%v,\n", body["id"])
		fmt.Fprintf(w, "data: \"result\":{\"ok\":true}}\n\n")
		flusher.Flush()
	}))
	defer srv.Close()
	tr, _ := NewHTTP(HTTPConfig{URL: srv.URL, AllowSSE: true})
	defer tr.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	raw, err := tr.Call(ctx, "ping", nil)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"ok":true`) {
		t.Fatalf("got %s", raw)
	}
}

func TestHTTPSSENotEnabledFallsBackToBody(t *testing.T) {
	// When AllowSSE is false, we should NOT parse event-stream responses
	// specially; we return the raw body as-is.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"jsonrpc":"2.0","id":1}`)
	}))
	defer srv.Close()
	tr, _ := NewHTTP(HTTPConfig{URL: srv.URL, AllowSSE: false})
	defer tr.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	raw, err := tr.Call(ctx, "ping", nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(raw) == 0 {
		t.Fatal("empty")
	}
}

func TestHTTPAcceptsCustomClient(t *testing.T) {
	client := &http.Client{Timeout: 1 * time.Second}
	tr, err := NewHTTP(HTTPConfig{URL: "http://127.0.0.1:1/", Client: client})
	if err != nil {
		t.Fatal(err)
	}
	if tr.client != client {
		t.Fatal("client not set")
	}
}
