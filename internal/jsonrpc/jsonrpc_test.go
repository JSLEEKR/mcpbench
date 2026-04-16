package jsonrpc

import (
	"encoding/json"
	"sync"
	"testing"
)

func TestNewRequest(t *testing.T) {
	r := NewRequest(42, "tools/call", map[string]any{"name": "x"})
	if r.JSONRPC != "2.0" {
		t.Fatalf("want 2.0 got %s", r.JSONRPC)
	}
	if r.ID != 42 {
		t.Fatalf("id = %d", r.ID)
	}
	if r.Method != "tools/call" {
		t.Fatalf("method = %s", r.Method)
	}
}

func TestRequestMarshal(t *testing.T) {
	r := NewRequest(1, "ping", nil)
	b, err := r.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	var back map[string]any
	if err := json.Unmarshal(b, &back); err != nil {
		t.Fatal(err)
	}
	if back["jsonrpc"] != "2.0" {
		t.Fatalf("jsonrpc field wrong: %v", back)
	}
	if _, ok := back["params"]; ok {
		t.Fatalf("params should be omitted when nil: %s", b)
	}
}

func TestRequestMarshalWithParams(t *testing.T) {
	r := NewRequest(1, "tools/call", ToolCallParams{Name: "read", Arguments: map[string]any{"path": "/a"}})
	b, err := r.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	if len(b) == 0 {
		t.Fatalf("empty bytes")
	}
	var back map[string]any
	if err := json.Unmarshal(b, &back); err != nil {
		t.Fatal(err)
	}
	params, ok := back["params"].(map[string]any)
	if !ok {
		t.Fatalf("params missing/wrong type")
	}
	if params["name"] != "read" {
		t.Fatalf("tool name wrong")
	}
}

func TestParseResponseOK(t *testing.T) {
	raw := []byte(`{"jsonrpc":"2.0","id":7,"result":{"ok":true}}`)
	resp, err := ParseResponse(raw)
	if err != nil {
		t.Fatal(err)
	}
	if resp.ID != 7 {
		t.Fatalf("id = %d", resp.ID)
	}
	if resp.Error != nil {
		t.Fatalf("unexpected error")
	}
	if string(resp.Result) == "" {
		t.Fatalf("empty result")
	}
}

func TestParseResponseError(t *testing.T) {
	raw := []byte(`{"jsonrpc":"2.0","id":9,"error":{"code":-32601,"message":"method not found"}}`)
	resp, err := ParseResponse(raw)
	if err != nil {
		t.Fatal(err)
	}
	if resp.Error == nil {
		t.Fatalf("expected error")
	}
	if resp.Error.Code != -32601 {
		t.Fatalf("code = %d", resp.Error.Code)
	}
	if resp.Error.Message != "method not found" {
		t.Fatalf("msg = %s", resp.Error.Message)
	}
}

func TestParseResponseRejectsBadJSON(t *testing.T) {
	_, err := ParseResponse([]byte(`not-json`))
	if err == nil {
		t.Fatalf("expected parse error")
	}
}

func TestParseResponseRejectsWrongVersion(t *testing.T) {
	raw := []byte(`{"jsonrpc":"1.0","id":1}`)
	_, err := ParseResponse(raw)
	if err == nil {
		t.Fatalf("expected version error")
	}
}

func TestErrorString(t *testing.T) {
	var e *Error
	if got := e.Error(); got != "<nil jsonrpc.Error>" {
		t.Fatalf("nil case: %s", got)
	}
	e = &Error{Code: -32000, Message: "boom"}
	if e.Error() != "boom" {
		t.Fatalf("err string: %s", e.Error())
	}
}

func TestIDPoolSequential(t *testing.T) {
	p := NewIDPool()
	for i := int64(1); i <= 100; i++ {
		if got := p.Next(); got != i {
			t.Fatalf("i=%d got=%d", i, got)
		}
	}
}

func TestIDPoolConcurrent(t *testing.T) {
	p := NewIDPool()
	const workers = 32
	const per = 1000
	var wg sync.WaitGroup
	seen := sync.Map{}
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < per; i++ {
				id := p.Next()
				if _, loaded := seen.LoadOrStore(id, true); loaded {
					t.Errorf("duplicate id %d", id)
					return
				}
			}
		}()
	}
	wg.Wait()
}
