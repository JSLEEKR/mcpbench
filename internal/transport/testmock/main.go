// Command testmock is a tiny JSON-RPC stdio server used by transport tests.
// Supported methods:
//
//	ping         → returns {"ok":true}
//	echo         → returns params unchanged
//	slow         → waits params.ms before responding
//	errorMethod  → returns a JSON-RPC error
//	big          → returns a large string of params.size repeats of 'a'
//	crash        → exits with code 7
//	noisy_stderr → prints a line to stderr then replies
//	bad_json     → writes a malformed JSON line (no id)
//
// The server reads newline-delimited JSON-RPC requests from stdin and writes
// responses to stdout, one per line.
package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"
)

type req struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      int64           `json:"id"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params"`
}

type resp struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      int64           `json:"id"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *rpcErr         `json:"error,omitempty"`
}

type rpcErr struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func main() {
	reader := bufio.NewReader(os.Stdin)
	writer := bufio.NewWriter(os.Stdout)
	defer writer.Flush()

	for {
		line, err := reader.ReadBytes('\n')
		if len(line) == 0 && err != nil {
			return
		}
		line = []byte(strings.TrimRight(string(line), "\n"))
		if len(line) == 0 {
			if err != nil {
				return
			}
			continue
		}
		var r req
		if jerr := json.Unmarshal(line, &r); jerr != nil {
			// Ignore unparsable input.
			if err != nil {
				return
			}
			continue
		}
		handle(r, writer)
		if err != nil {
			return
		}
	}
}

func handle(r req, w *bufio.Writer) {
	// Unwrap MCP-style tools/call wrapper: the orchestrator dispatches with
	// method="tools/call" and params={name:<tool>,arguments:<args>}. The mock
	// routes on the tool name instead.
	method := r.Method
	params := r.Params
	if method == "tools/call" {
		var wrap struct {
			Name      string          `json:"name"`
			Arguments json.RawMessage `json:"arguments"`
		}
		if err := json.Unmarshal(r.Params, &wrap); err == nil && wrap.Name != "" {
			method = wrap.Name
			params = wrap.Arguments
		}
	}
	r.Method = method
	r.Params = params
	switch r.Method {
	case "ping":
		send(w, resp{JSONRPC: "2.0", ID: r.ID, Result: json.RawMessage(`{"ok":true}`)})
	case "echo":
		payload := r.Params
		if len(payload) == 0 {
			payload = json.RawMessage(`null`)
		}
		send(w, resp{JSONRPC: "2.0", ID: r.ID, Result: payload})
	case "slow":
		var p struct {
			MS int `json:"ms"`
		}
		_ = json.Unmarshal(r.Params, &p)
		time.Sleep(time.Duration(p.MS) * time.Millisecond)
		send(w, resp{JSONRPC: "2.0", ID: r.ID, Result: json.RawMessage(`{"ok":true}`)})
	case "errorMethod":
		send(w, resp{JSONRPC: "2.0", ID: r.ID, Error: &rpcErr{Code: -32601, Message: "method not found"}})
	case "big":
		var p struct {
			Size int `json:"size"`
		}
		_ = json.Unmarshal(r.Params, &p)
		if p.Size <= 0 {
			p.Size = 1024
		}
		buf := strings.Repeat("a", p.Size)
		payload, _ := json.Marshal(map[string]string{"data": buf})
		send(w, resp{JSONRPC: "2.0", ID: r.ID, Result: payload})
	case "crash":
		os.Exit(7)
	case "noisy_stderr":
		fmt.Fprintln(os.Stderr, "noisy stderr line")
		send(w, resp{JSONRPC: "2.0", ID: r.ID, Result: json.RawMessage(`{"ok":true}`)})
	case "bad_json":
		_, _ = w.WriteString("this is not json\n")
		_ = w.Flush()
	default:
		send(w, resp{JSONRPC: "2.0", ID: r.ID, Error: &rpcErr{Code: -32601, Message: "unknown: " + r.Method}})
	}
}

func send(w *bufio.Writer, r resp) {
	b, _ := json.Marshal(r)
	_, _ = w.Write(b)
	_, _ = w.WriteString("\n")
	_ = w.Flush()
}
