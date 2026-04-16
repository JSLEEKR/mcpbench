package scenario

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const minimalYAML = `
name: t
transport:
  type: stdio
  cmd: /bin/echo
workload:
  rate: 10
  duration: 1s
tools:
  - name: ping
    weight: 1
`

func TestParseMinimal(t *testing.T) {
	s, err := Parse([]byte(minimalYAML))
	if err != nil {
		t.Fatal(err)
	}
	if s.Name != "t" {
		t.Fatalf("name = %s", s.Name)
	}
	if s.Transport.Type != "stdio" {
		t.Fatal("transport")
	}
	if s.Workload.Duration != time.Second {
		t.Fatal("duration")
	}
	if len(s.Tools) != 1 {
		t.Fatal("tools")
	}
}

func TestParseRejectsEmpty(t *testing.T) {
	_, err := Parse([]byte(""))
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestParseRejectsInvalidYAML(t *testing.T) {
	_, err := Parse([]byte("not : valid : yaml:"))
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestParseRejectsUnknownFields(t *testing.T) {
	_, err := Parse([]byte(`
name: t
transport:
  type: stdio
  cmd: /bin/echo
workload:
  rate: 10
  duration: 1s
tools:
  - name: ping
unknown_top_level: true
`))
	if err == nil {
		t.Fatal("expected strict field error")
	}
}

func TestLoadFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "s.yml")
	if err := os.WriteFile(path, []byte(minimalYAML), 0o600); err != nil {
		t.Fatal(err)
	}
	s, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if s.Name != "t" {
		t.Fatal(s.Name)
	}
}

func TestLoadFileMissing(t *testing.T) {
	_, err := Load("/tmp/definitely-does-not-exist-mcpbench")
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestValidateMissingName(t *testing.T) {
	s := &Scenario{Transport: TransportConfig{Type: "stdio", Cmd: "x"}, Workload: Workload{Rate: 1, Duration: time.Second}, Tools: []ToolCall{{Name: "a"}}}
	if err := s.Validate(); err == nil {
		t.Fatal("expected name error")
	}
}

func TestValidateMissingTransportType(t *testing.T) {
	s := &Scenario{Name: "t", Workload: Workload{Rate: 1, Duration: time.Second}, Tools: []ToolCall{{Name: "a"}}}
	err := s.Validate()
	if err == nil || !strings.Contains(err.Error(), "transport.type") {
		t.Fatalf("err = %v", err)
	}
}

func TestValidateBadTransportType(t *testing.T) {
	s := &Scenario{Name: "t", Transport: TransportConfig{Type: "bogus"}, Workload: Workload{Rate: 1, Duration: time.Second}, Tools: []ToolCall{{Name: "a"}}}
	if err := s.Validate(); err == nil {
		t.Fatal("expected type error")
	}
}

func TestValidateStdioRequiresCmd(t *testing.T) {
	s := &Scenario{Name: "t", Transport: TransportConfig{Type: "stdio"}, Workload: Workload{Rate: 1, Duration: time.Second}, Tools: []ToolCall{{Name: "a"}}}
	if err := s.Validate(); err == nil {
		t.Fatal("expected cmd error")
	}
}

func TestValidateHTTPRequiresURL(t *testing.T) {
	s := &Scenario{Name: "t", Transport: TransportConfig{Type: "http"}, Workload: Workload{Rate: 1, Duration: time.Second}, Tools: []ToolCall{{Name: "a"}}}
	if err := s.Validate(); err == nil {
		t.Fatal("expected url error")
	}
}

func TestValidateHTTPBadURL(t *testing.T) {
	s := &Scenario{Name: "t", Transport: TransportConfig{Type: "http", URL: "example.com"}, Workload: Workload{Rate: 1, Duration: time.Second}, Tools: []ToolCall{{Name: "a"}}}
	if err := s.Validate(); err == nil {
		t.Fatal("expected url scheme error")
	}
}

func TestValidateHTTPAcceptsHTTPS(t *testing.T) {
	s := &Scenario{Name: "t", Transport: TransportConfig{Type: "http", URL: "https://example.com/"}, Workload: Workload{Rate: 1, Duration: time.Second}, Tools: []ToolCall{{Name: "a"}}}
	if err := s.Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestValidateSSEIsHTTP(t *testing.T) {
	s := &Scenario{Name: "t", Transport: TransportConfig{Type: "sse", URL: "https://x/"}, Workload: Workload{Rate: 1, Duration: time.Second}, Tools: []ToolCall{{Name: "a"}}}
	if err := s.Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestValidateWorkloadRequiresDurationOrRequests(t *testing.T) {
	s := &Scenario{Name: "t", Transport: TransportConfig{Type: "stdio", Cmd: "x"}, Workload: Workload{Rate: 1}, Tools: []ToolCall{{Name: "a"}}}
	err := s.Validate()
	if err == nil || !strings.Contains(err.Error(), "duration or requests") {
		t.Fatalf("err = %v", err)
	}
}

func TestValidateWorkloadRequiresRateOrConcurrency(t *testing.T) {
	s := &Scenario{Name: "t", Transport: TransportConfig{Type: "stdio", Cmd: "x"}, Workload: Workload{Duration: time.Second}, Tools: []ToolCall{{Name: "a"}}}
	if err := s.Validate(); err == nil {
		t.Fatal("expected rate/concurrency error")
	}
}

func TestValidateWorkloadNegative(t *testing.T) {
	s := &Scenario{Name: "t", Transport: TransportConfig{Type: "stdio", Cmd: "x"}, Workload: Workload{Rate: -1, Duration: time.Second}, Tools: []ToolCall{{Name: "a"}}}
	if err := s.Validate(); err == nil {
		t.Fatal("expected negative error")
	}
}

func TestValidateWorkloadNegativeTime(t *testing.T) {
	s := &Scenario{Name: "t", Transport: TransportConfig{Type: "stdio", Cmd: "x"}, Workload: Workload{Rate: 1, Duration: time.Second, Warmup: -1}, Tools: []ToolCall{{Name: "a"}}}
	if err := s.Validate(); err == nil {
		t.Fatal("expected negative warmup error")
	}
}

func TestValidateNoTools(t *testing.T) {
	s := &Scenario{Name: "t", Transport: TransportConfig{Type: "stdio", Cmd: "x"}, Workload: Workload{Rate: 1, Duration: time.Second}}
	if err := s.Validate(); err == nil {
		t.Fatal("expected no tools error")
	}
}

func TestValidateToolNoName(t *testing.T) {
	s := &Scenario{Name: "t", Transport: TransportConfig{Type: "stdio", Cmd: "x"}, Workload: Workload{Rate: 1, Duration: time.Second}, Tools: []ToolCall{{Name: ""}}}
	if err := s.Validate(); err == nil {
		t.Fatal("expected tool name error")
	}
}

func TestValidateToolNegativeWeight(t *testing.T) {
	s := &Scenario{Name: "t", Transport: TransportConfig{Type: "stdio", Cmd: "x"}, Workload: Workload{Rate: 1, Duration: time.Second}, Tools: []ToolCall{{Name: "a", Weight: -1}}}
	if err := s.Validate(); err == nil {
		t.Fatal("expected weight error")
	}
}

func TestValidateZeroWeightDefaultsToOne(t *testing.T) {
	s := &Scenario{Name: "t", Transport: TransportConfig{Type: "stdio", Cmd: "x"}, Workload: Workload{Rate: 1, Duration: time.Second}, Tools: []ToolCall{{Name: "a", Weight: 0}}}
	if err := s.Validate(); err != nil {
		t.Fatal(err)
	}
	if s.Tools[0].Weight != 1 {
		t.Fatal("weight not defaulted")
	}
}

func TestCallTimeout(t *testing.T) {
	s := &Scenario{Workload: Workload{Timeout: 5 * time.Second}}
	// tool override
	tc := &ToolCall{Timeout: 100 * time.Millisecond}
	if got := s.CallTimeout(tc); got != 100*time.Millisecond {
		t.Fatalf("got %v", got)
	}
	// workload default
	tc = &ToolCall{}
	if got := s.CallTimeout(tc); got != 5*time.Second {
		t.Fatalf("got %v", got)
	}
	// package default
	s = &Scenario{}
	if got := s.CallTimeout(&ToolCall{}); got != DefaultTimeout {
		t.Fatalf("got %v", got)
	}
}

func TestPickerSingleTool(t *testing.T) {
	s := &Scenario{Tools: []ToolCall{{Name: "only", Weight: 5}}}
	p := NewPicker(s, 1)
	for i := 0; i < 20; i++ {
		if p.Next().Name != "only" {
			t.Fatal("picked wrong")
		}
	}
}

func TestPickerDeterministicWithSeed(t *testing.T) {
	s := &Scenario{Tools: []ToolCall{{Name: "a", Weight: 1}, {Name: "b", Weight: 1}}}
	p1 := NewPicker(s, 42)
	p2 := NewPicker(s, 42)
	for i := 0; i < 50; i++ {
		a := p1.Next().Name
		b := p2.Next().Name
		if a != b {
			t.Fatalf("diverged at %d: %s vs %s", i, a, b)
		}
	}
}

func TestPickerWeightedDistribution(t *testing.T) {
	s := &Scenario{Tools: []ToolCall{{Name: "a", Weight: 1}, {Name: "b", Weight: 9}}}
	p := NewPicker(s, 1)
	counts := map[string]int{}
	const iters = 10_000
	for i := 0; i < iters; i++ {
		counts[p.Next().Name]++
	}
	// Expect roughly 10/90 split. Allow ±3%.
	ratioB := float64(counts["b"]) / float64(iters)
	if ratioB < 0.87 || ratioB > 0.93 {
		t.Fatalf("b ratio = %f", ratioB)
	}
}

func TestPickerZeroTotal(t *testing.T) {
	// After Validate, zero weight becomes 1; but construct Picker directly.
	s := &Scenario{Tools: []ToolCall{{Name: "a", Weight: 0}}}
	p := NewPicker(s, 1)
	if p.Next().Name != "a" {
		t.Fatal("picked wrong")
	}
}

func TestRenderArgsNoTemplate(t *testing.T) {
	out, err := RenderArgs(map[string]any{"a": "plain", "b": 42}, TemplateContext{Iter: 1})
	if err != nil {
		t.Fatal(err)
	}
	if out["a"] != "plain" {
		t.Fatal("string mangled")
	}
	if out["b"] != 42 {
		t.Fatal("int mangled")
	}
}

func TestRenderArgsIterTemplate(t *testing.T) {
	out, err := RenderArgs(map[string]any{"q": "page-{{.Iter}}"}, TemplateContext{Iter: 7})
	if err != nil {
		t.Fatal(err)
	}
	if out["q"] != "page-7" {
		t.Fatalf("got %v", out["q"])
	}
}

func TestRenderArgsEnvTemplate(t *testing.T) {
	out, err := RenderArgs(map[string]any{"k": "{{.Env.FOO}}"}, TemplateContext{Env: map[string]string{"FOO": "bar"}})
	if err != nil {
		t.Fatal(err)
	}
	if out["k"] != "bar" {
		t.Fatalf("got %v", out["k"])
	}
}

func TestRenderArgsMissingVarFails(t *testing.T) {
	_, err := RenderArgs(map[string]any{"k": "{{.Env.NOPE}}"}, TemplateContext{Env: map[string]string{}})
	if err == nil {
		t.Fatal("expected missing-key error")
	}
}

func TestRenderArgsBadTemplate(t *testing.T) {
	_, err := RenderArgs(map[string]any{"k": "{{ broken"}, TemplateContext{})
	if err == nil {
		t.Fatal("expected parse error")
	}
}

func TestRenderArgsNestedMap(t *testing.T) {
	args := map[string]any{
		"outer": map[string]any{
			"q": "val-{{.Iter}}",
		},
	}
	out, err := RenderArgs(args, TemplateContext{Iter: 3})
	if err != nil {
		t.Fatal(err)
	}
	nested, ok := out["outer"].(map[string]any)
	if !ok {
		t.Fatalf("nested type wrong: %T", out["outer"])
	}
	if nested["q"] != "val-3" {
		t.Fatalf("got %v", nested["q"])
	}
}

func TestRenderArgsSlice(t *testing.T) {
	args := map[string]any{
		"items": []any{"a", "{{.Iter}}", "b"},
	}
	out, err := RenderArgs(args, TemplateContext{Iter: 9})
	if err != nil {
		t.Fatal(err)
	}
	items := out["items"].([]any)
	if items[1] != "9" {
		t.Fatalf("got %v", items)
	}
}

func TestRenderArgsEmpty(t *testing.T) {
	out, err := RenderArgs(nil, TemplateContext{})
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 0 {
		t.Fatal("non-empty")
	}
}

func TestRenderArgsNonStringValue(t *testing.T) {
	out, err := RenderArgs(map[string]any{"x": 3.14, "y": true}, TemplateContext{})
	if err != nil {
		t.Fatal(err)
	}
	if out["x"] != 3.14 || out["y"] != true {
		t.Fatalf("got %v", out)
	}
}

func TestExampleIsValid(t *testing.T) {
	s, err := Parse([]byte(Example()))
	if err != nil {
		t.Fatal(err)
	}
	if s.Name == "" {
		t.Fatal("name empty")
	}
}

func TestParseFullYAML(t *testing.T) {
	yml := `
name: full
transport:
  type: http
  url: https://example.com/rpc
  headers:
    Authorization: Bearer xyz
workload:
  rate: 50
  concurrency: 8
  duration: 10s
  warmup: 1s
  cooldown: 1s
  think_time: 100ms
  timeout: 5s
tools:
  - name: a
    weight: 2
    args:
      x: 1
    timeout: 1s
  - name: b
    weight: 3
`
	s, err := Parse([]byte(yml))
	if err != nil {
		t.Fatal(err)
	}
	if s.Transport.Headers["Authorization"] != "Bearer xyz" {
		t.Fatal("headers lost")
	}
	if s.Workload.ThinkTime != 100*time.Millisecond {
		t.Fatal("think_time lost")
	}
	if s.Tools[0].Timeout != time.Second {
		t.Fatal("tool timeout lost")
	}
}

func TestParseRejectsNegativeDuration(t *testing.T) {
	yml := `
name: t
transport: {type: stdio, cmd: x}
workload:
  rate: 1
  duration: 1s
  warmup: -1s
tools: [{name: a}]
`
	_, err := Parse([]byte(yml))
	if err == nil {
		t.Fatal("expected negative error")
	}
}

func TestTransportEnvMap(t *testing.T) {
	yml := `
name: t
transport:
  type: stdio
  cmd: x
  env:
    A: "1"
    B: "2"
workload:
  rate: 1
  duration: 1s
tools: [{name: t1}]
`
	s, err := Parse([]byte(yml))
	if err != nil {
		t.Fatal(err)
	}
	if s.Transport.Env["A"] != "1" || s.Transport.Env["B"] != "2" {
		t.Fatal("env missing")
	}
}

func TestRequestsOnlyValid(t *testing.T) {
	yml := `
name: t
transport: {type: stdio, cmd: x}
workload:
  concurrency: 4
  requests: 100
tools: [{name: t1}]
`
	s, err := Parse([]byte(yml))
	if err != nil {
		t.Fatal(err)
	}
	if s.Workload.Requests != 100 {
		t.Fatal("requests")
	}
}
