// Package jsonrpc defines minimal JSON-RPC 2.0 envelopes used by mcpbench and
// a lock-free monotonically increasing correlation-id allocator.
package jsonrpc

import (
	"encoding/json"
	"errors"
	"sync/atomic"
)

// Version is the JSON-RPC protocol version string.
const Version = "2.0"

// Request represents a single JSON-RPC 2.0 request envelope.
type Request struct {
	JSONRPC string `json:"jsonrpc"`
	ID      int64  `json:"id"`
	Method  string `json:"method"`
	Params  any    `json:"params,omitempty"`
}

// Response represents a single JSON-RPC 2.0 response envelope.
type Response struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      int64           `json:"id"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *Error          `json:"error,omitempty"`
}

// Error is the error field inside a JSON-RPC response.
type Error struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    any    `json:"data,omitempty"`
}

// Error implements the error interface.
func (e *Error) Error() string {
	if e == nil {
		return "<nil jsonrpc.Error>"
	}
	return e.Message
}

// ErrUnknownID is returned when demuxing a response whose id is not registered.
var ErrUnknownID = errors.New("jsonrpc: unknown response id")

// IDPool allocates monotonically increasing correlation IDs starting at 1.
type IDPool struct {
	n atomic.Int64
}

// NewIDPool returns a fresh IDPool.
func NewIDPool() *IDPool {
	return &IDPool{}
}

// Next returns the next unique id.
func (p *IDPool) Next() int64 {
	return p.n.Add(1)
}

// NewRequest constructs a valid JSON-RPC request envelope.
func NewRequest(id int64, method string, params any) *Request {
	return &Request{
		JSONRPC: Version,
		ID:      id,
		Method:  method,
		Params:  params,
	}
}

// Marshal serializes the request to a single line of JSON (no trailing newline).
func (r *Request) Marshal() ([]byte, error) {
	return json.Marshal(r)
}

// ParseResponse parses a single JSON-RPC response frame.
func ParseResponse(data []byte) (*Response, error) {
	var resp Response
	if err := json.Unmarshal(data, &resp); err != nil {
		return nil, err
	}
	if resp.JSONRPC != Version {
		return nil, errors.New("jsonrpc: invalid version")
	}
	return &resp, nil
}

// ToolCallParams is the MCP tools/call params shape.
type ToolCallParams struct {
	Name      string         `json:"name"`
	Arguments map[string]any `json:"arguments,omitempty"`
}
