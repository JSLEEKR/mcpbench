// Package transport defines the Transport interface used by the orchestrator
// and provides stdio and HTTP/SSE implementations.
package transport

import (
	"context"
	"fmt"
)

// Transport is the wire-level abstraction for an MCP server connection.
// Implementations must be safe for concurrent Call invocation.
type Transport interface {
	// Call issues a single JSON-RPC request and returns the raw response
	// bytes. Timeouts and cancellation are honored via ctx.
	Call(ctx context.Context, method string, params any) (json []byte, err error)

	// Close releases resources (kills subprocess, closes HTTP connections).
	Close() error
}

// Kind enumerates the transport varieties mcpbench can open.
type Kind string

// Known transport kinds.
const (
	KindStdio Kind = "stdio"
	KindHTTP  Kind = "http"
	KindSSE   Kind = "sse"
)

// ParseKind normalizes a string into a Kind, returning an error on unknowns.
func ParseKind(s string) (Kind, error) {
	switch s {
	case "stdio":
		return KindStdio, nil
	case "http", "https":
		return KindHTTP, nil
	case "sse":
		return KindSSE, nil
	case "":
		return "", fmt.Errorf("transport: kind required")
	default:
		return "", fmt.Errorf("transport: unknown kind %q", s)
	}
}
