package errors

import (
	"context"
	"errors"
	"fmt"
	"io"
	"testing"
)

func TestClassifyNilIsOK(t *testing.T) {
	if got := Classify(nil); got != CategoryOK {
		t.Fatalf("want ok got %s", got)
	}
}

func TestClassifyTimeoutError(t *testing.T) {
	e := &TimeoutError{Inner: errors.New("x")}
	if got := Classify(e); got != CategoryTimeout {
		t.Fatalf("got %s", got)
	}
	if e.Error() == "" {
		t.Fatal("empty msg")
	}
	if e.Unwrap() == nil {
		t.Fatal("unwrap nil")
	}
}

func TestClassifyTimeoutNilInner(t *testing.T) {
	e := &TimeoutError{}
	if e.Error() != "timeout" {
		t.Fatal(e.Error())
	}
}

func TestClassifyDeadlineExceeded(t *testing.T) {
	if got := Classify(context.DeadlineExceeded); got != CategoryTimeout {
		t.Fatalf("got %s", got)
	}
}

func TestClassifyJSONRPCError(t *testing.T) {
	e := NewJSONRPCError(-32601, "method not found")
	if got := Classify(e); got != CategoryJSONRPC {
		t.Fatalf("got %s", got)
	}
}

func TestClassifyWrappedJSONRPC(t *testing.T) {
	inner := NewJSONRPCError(-32000, "server error")
	wrapped := fmt.Errorf("calling tool: %w", inner)
	if got := Classify(wrapped); got != CategoryJSONRPC {
		t.Fatalf("got %s", got)
	}
}

func TestClassifyTransportError(t *testing.T) {
	e := &TransportError{Inner: errors.New("pipe closed")}
	if got := Classify(e); got != CategoryTransport {
		t.Fatalf("got %s", got)
	}
	if e.Unwrap() == nil {
		t.Fatal("unwrap nil")
	}
}

func TestClassifyTransportNilInner(t *testing.T) {
	e := &TransportError{}
	if e.Error() != "transport error" {
		t.Fatal(e.Error())
	}
}

func TestClassifyEOF(t *testing.T) {
	if got := Classify(io.EOF); got != CategoryTransport {
		t.Fatalf("got %s", got)
	}
	if got := Classify(io.ErrUnexpectedEOF); got != CategoryTransport {
		t.Fatalf("got %s", got)
	}
}

func TestClassifyTemplateError(t *testing.T) {
	e := &TemplateError{Inner: errors.New("bad tmpl")}
	if got := Classify(e); got != CategoryTemplate {
		t.Fatalf("got %s", got)
	}
	if e.Unwrap() == nil {
		t.Fatal("unwrap nil")
	}
}

func TestClassifyTemplateNilInner(t *testing.T) {
	e := &TemplateError{}
	if e.Error() != "template error" {
		t.Fatal(e.Error())
	}
}

func TestClassifyHeuristicTimeout(t *testing.T) {
	if got := Classify(errors.New("context deadline exceeded")); got != CategoryTimeout {
		t.Fatalf("got %s", got)
	}
	if got := Classify(errors.New("read tcp 1.2.3.4: i/o timeout")); got != CategoryTimeout {
		t.Fatalf("got %s", got)
	}
}

func TestClassifyHeuristicTransport(t *testing.T) {
	cases := []string{
		"dial tcp: connection refused",
		"write: broken pipe",
		"read: connection reset by peer",
		"dial tcp: lookup host: no such host",
	}
	for _, c := range cases {
		t.Run(c, func(t *testing.T) {
			if got := Classify(errors.New(c)); got != CategoryTransport {
				t.Fatalf("got %s", got)
			}
		})
	}
}

func TestClassifyOther(t *testing.T) {
	if got := Classify(errors.New("weird thing happened")); got != CategoryOther {
		t.Fatalf("got %s", got)
	}
}

func TestAllCategories(t *testing.T) {
	got := AllCategories()
	if len(got) != 5 {
		t.Fatalf("len=%d", len(got))
	}
	seen := map[Category]bool{}
	for _, c := range got {
		if seen[c] {
			t.Fatalf("dup %s", c)
		}
		seen[c] = true
	}
	if seen[CategoryOK] {
		t.Fatal("AllCategories should exclude ok")
	}
}

func TestJSONRPCErrorCode(t *testing.T) {
	e := NewJSONRPCError(-1, "boom")
	rpc, ok := e.(JSONRPCError)
	if !ok {
		t.Fatal("not JSONRPCError")
	}
	if rpc.RPCCode() != -1 {
		t.Fatalf("code = %d", rpc.RPCCode())
	}
}
