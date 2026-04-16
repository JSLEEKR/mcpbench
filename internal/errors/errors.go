// Package errors classifies mcpbench error categories so the aggregator can
// keep a per-category count without scattering type switches throughout.
package errors

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
)

// Category identifies the kind of failure observed for a single request.
type Category string

// Stable set of error categories.
const (
	CategoryOK        Category = "ok"
	CategoryTimeout   Category = "timeout"
	CategoryJSONRPC   Category = "jsonrpc"
	CategoryTransport Category = "transport"
	CategoryTemplate  Category = "template"
	CategoryOther     Category = "other"
)

// AllCategories returns all error-bearing categories (excluding ok).
func AllCategories() []Category {
	return []Category{CategoryTimeout, CategoryJSONRPC, CategoryTransport, CategoryTemplate, CategoryOther}
}

// JSONRPCError is the minimal interface the classifier needs from a JSON-RPC
// error value. Defined here to avoid an import cycle with internal/jsonrpc.
type JSONRPCError interface {
	error
	// Code returns the JSON-RPC error code.
	RPCCode() int
}

// rpcCoder is a helper implementation used by transports that do not want to
// implement the interface directly.
type rpcCoder struct {
	code int
	msg  string
}

func (e *rpcCoder) Error() string { return e.msg }
func (e *rpcCoder) RPCCode() int  { return e.code }

// NewJSONRPCError wraps a JSON-RPC code+message as a JSONRPCError.
func NewJSONRPCError(code int, msg string) error {
	return &rpcCoder{code: code, msg: msg}
}

// TimeoutError indicates the request was cancelled due to timeout.
type TimeoutError struct{ Inner error }

func (e *TimeoutError) Error() string {
	if e.Inner == nil {
		return "timeout"
	}
	return fmt.Sprintf("timeout: %v", e.Inner)
}

// Unwrap allows errors.Is / errors.As.
func (e *TimeoutError) Unwrap() error { return e.Inner }

// TransportError indicates a failure at the transport layer (subprocess died,
// connection refused, malformed JSON frame, etc.).
type TransportError struct{ Inner error }

func (e *TransportError) Error() string {
	if e.Inner == nil {
		return "transport error"
	}
	return fmt.Sprintf("transport error: %v", e.Inner)
}

// Unwrap allows errors.Is / errors.As.
func (e *TransportError) Unwrap() error { return e.Inner }

// TemplateError indicates rendering the tool-args template failed.
type TemplateError struct{ Inner error }

func (e *TemplateError) Error() string {
	if e.Inner == nil {
		return "template error"
	}
	return fmt.Sprintf("template error: %v", e.Inner)
}

// Unwrap allows errors.Is / errors.As.
func (e *TemplateError) Unwrap() error { return e.Inner }

// Classify returns the Category an error belongs to. nil returns CategoryOK.
func Classify(err error) Category {
	if err == nil {
		return CategoryOK
	}
	var te *TimeoutError
	if errors.As(err, &te) {
		return CategoryTimeout
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return CategoryTimeout
	}
	var rpcErr JSONRPCError
	if errors.As(err, &rpcErr) {
		return CategoryJSONRPC
	}
	var tre *TransportError
	if errors.As(err, &tre) {
		return CategoryTransport
	}
	if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
		return CategoryTransport
	}
	var tme *TemplateError
	if errors.As(err, &tme) {
		return CategoryTemplate
	}
	// Heuristic fallbacks for unwrapped strings from net/http etc.
	msg := strings.ToLower(err.Error())
	if strings.Contains(msg, "timeout") || strings.Contains(msg, "deadline exceeded") {
		return CategoryTimeout
	}
	if strings.Contains(msg, "connection refused") ||
		strings.Contains(msg, "broken pipe") ||
		strings.Contains(msg, "reset by peer") ||
		strings.Contains(msg, "no such host") {
		return CategoryTransport
	}
	return CategoryOther
}
