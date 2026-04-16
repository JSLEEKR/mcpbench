// Package scenario parses YAML scenario files and exposes weighted tool-call
// selection and argument templating for the orchestrator.
package scenario

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"math/rand"
	"os"
	"sort"
	"strings"
	texttemplate "text/template"
	"time"

	mcperrors "github.com/JSLEEKR/mcpbench/internal/errors"
	"gopkg.in/yaml.v3"
)

// Scenario is the top-level parsed YAML.
type Scenario struct {
	Name      string          `yaml:"name"`
	Transport TransportConfig `yaml:"transport"`
	Workload  Workload        `yaml:"workload"`
	Tools     []ToolCall      `yaml:"tools"`
}

// TransportConfig selects and configures the transport.
type TransportConfig struct {
	Type    string            `yaml:"type"`
	Cmd     string            `yaml:"cmd"`
	Args    []string          `yaml:"args"`
	Env     map[string]string `yaml:"env"`
	URL     string            `yaml:"url"`
	Headers map[string]string `yaml:"headers"`
}

// Workload configures the load profile.
type Workload struct {
	Rate        float64       `yaml:"rate"`
	Concurrency int           `yaml:"concurrency"`
	Duration    time.Duration `yaml:"duration"`
	Requests    int           `yaml:"requests"`
	Warmup      time.Duration `yaml:"warmup"`
	Cooldown    time.Duration `yaml:"cooldown"`
	ThinkTime   time.Duration `yaml:"think_time"`
	Timeout     time.Duration `yaml:"timeout"`
}

// ToolCall is a single weighted tool invocation template.
type ToolCall struct {
	Name    string         `yaml:"name"`
	Weight  int            `yaml:"weight"`
	Args    map[string]any `yaml:"args"`
	Timeout time.Duration  `yaml:"timeout"`
}

// DefaultTimeout is the per-call timeout used when neither the tool nor the
// workload specifies one.
const DefaultTimeout = 30 * time.Second

// Load reads and parses a YAML scenario from disk.
func Load(path string) (*Scenario, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read scenario: %w", err)
	}
	return Parse(data)
}

// Parse parses a YAML scenario from bytes.
func Parse(data []byte) (*Scenario, error) {
	return ParseReader(bytes.NewReader(data))
}

// ParseReader parses a scenario from an io.Reader.
func ParseReader(r io.Reader) (*Scenario, error) {
	dec := yaml.NewDecoder(r)
	dec.KnownFields(true)
	var s Scenario
	if err := dec.Decode(&s); err != nil {
		if errors.Is(err, io.EOF) {
			return nil, fmt.Errorf("scenario: empty document")
		}
		return nil, fmt.Errorf("scenario: yaml parse: %w", err)
	}
	if err := s.Validate(); err != nil {
		return nil, err
	}
	return &s, nil
}

// Validate checks that the scenario is internally consistent. Returns the
// first error encountered.
func (s *Scenario) Validate() error {
	if strings.TrimSpace(s.Name) == "" {
		return fmt.Errorf("scenario: name required")
	}
	if err := s.Transport.validate(); err != nil {
		return err
	}
	if err := s.Workload.validate(); err != nil {
		return err
	}
	if len(s.Tools) == 0 {
		return fmt.Errorf("scenario: at least one tool required")
	}
	var totalWeight int64
	for i := range s.Tools {
		t := &s.Tools[i]
		if strings.TrimSpace(t.Name) == "" {
			return fmt.Errorf("scenario: tool[%d] name required", i)
		}
		if t.Weight < 0 {
			return fmt.Errorf("scenario: tool[%d] weight must be >= 0", i)
		}
		if t.Weight == 0 {
			// Default weight = 1.
			t.Weight = 1
		}
		totalWeight += int64(t.Weight)
	}
	if totalWeight <= 0 {
		return fmt.Errorf("scenario: total tool weight must be > 0")
	}
	return nil
}

func (t *TransportConfig) validate() error {
	switch strings.ToLower(t.Type) {
	case "stdio":
		if strings.TrimSpace(t.Cmd) == "" {
			return fmt.Errorf("scenario: transport.cmd required for stdio")
		}
	case "http", "sse":
		if strings.TrimSpace(t.URL) == "" {
			return fmt.Errorf("scenario: transport.url required for http")
		}
		if !strings.HasPrefix(t.URL, "http://") && !strings.HasPrefix(t.URL, "https://") {
			return fmt.Errorf("scenario: transport.url must start with http:// or https://")
		}
	case "":
		return fmt.Errorf("scenario: transport.type required (stdio|http)")
	default:
		return fmt.Errorf("scenario: unknown transport.type %q (want stdio|http)", t.Type)
	}
	return nil
}

func (w *Workload) validate() error {
	if w.Duration < 0 || w.Requests < 0 || w.Rate < 0 || w.Concurrency < 0 {
		return fmt.Errorf("scenario: workload numbers must be non-negative")
	}
	if w.Duration == 0 && w.Requests == 0 {
		return fmt.Errorf("scenario: workload must set duration or requests")
	}
	if w.Duration > 0 && w.Requests > 0 {
		// Both set silently picks duration in the orchestrator and discards
		// the requests budget, which is rarely what the operator meant. The
		// CLI rejects --duration+--requests for the same reason; the YAML
		// path must agree.
		return fmt.Errorf("scenario: workload.duration and workload.requests are mutually exclusive")
	}
	if w.Rate == 0 && w.Concurrency == 0 {
		return fmt.Errorf("scenario: workload must set rate or concurrency")
	}
	if w.Warmup < 0 || w.Cooldown < 0 || w.ThinkTime < 0 || w.Timeout < 0 {
		return fmt.Errorf("scenario: workload time fields must be non-negative")
	}
	return nil
}

// CallTimeout returns the effective timeout for a tool: tool override >
// workload timeout > DefaultTimeout.
func (s *Scenario) CallTimeout(t *ToolCall) time.Duration {
	if t.Timeout > 0 {
		return t.Timeout
	}
	if s.Workload.Timeout > 0 {
		return s.Workload.Timeout
	}
	return DefaultTimeout
}

// Picker selects tools according to their weights.
type Picker struct {
	tools []ToolCall
	cum   []int64
	total int64
	rng   *rand.Rand
}

// NewPicker creates a weighted tool picker. seed sets the deterministic random
// source; pass 0 for a random seed.
func NewPicker(s *Scenario, seed int64) *Picker {
	src := rand.NewSource(seed)
	if seed == 0 {
		src = rand.NewSource(time.Now().UnixNano())
	}
	p := &Picker{
		tools: make([]ToolCall, len(s.Tools)),
		rng:   rand.New(src),
	}
	copy(p.tools, s.Tools)
	p.cum = make([]int64, len(p.tools))
	var cum int64
	for i, t := range p.tools {
		cum += int64(t.Weight)
		p.cum[i] = cum
	}
	p.total = cum
	return p
}

// Next returns a pointer to the next selected tool. Safe for a single caller
// goroutine; wrap with sync if sharing across goroutines.
func (p *Picker) Next() *ToolCall {
	if p.total <= 0 {
		return &p.tools[0]
	}
	if len(p.tools) == 1 {
		return &p.tools[0]
	}
	pick := p.rng.Int63n(p.total)
	idx := sort.Search(len(p.cum), func(i int) bool { return p.cum[i] > pick })
	if idx >= len(p.tools) {
		idx = len(p.tools) - 1
	}
	return &p.tools[idx]
}

// TemplateContext is the variable environment exposed to arg templates.
type TemplateContext struct {
	Iter int64
	Env  map[string]string
	Rand float64
}

// RenderArgs returns a freshly-built args map with any string template
// placeholders rendered via text/template.
func RenderArgs(args map[string]any, ctx TemplateContext) (map[string]any, error) {
	if len(args) == 0 {
		return map[string]any{}, nil
	}
	out := make(map[string]any, len(args))
	for k, v := range args {
		rendered, err := renderValue(v, ctx)
		if err != nil {
			return nil, &mcperrors.TemplateError{Inner: fmt.Errorf("arg %q: %w", k, err)}
		}
		out[k] = rendered
	}
	return out, nil
}

func renderValue(v any, ctx TemplateContext) (any, error) {
	switch val := v.(type) {
	case string:
		if !strings.Contains(val, "{{") {
			return val, nil
		}
		tmpl, err := texttemplate.New("arg").Option("missingkey=error").Parse(val)
		if err != nil {
			return nil, err
		}
		var buf strings.Builder
		if err := tmpl.Execute(&buf, ctx); err != nil {
			return nil, err
		}
		return buf.String(), nil
	case map[string]any:
		return RenderArgs(val, ctx)
	case []any:
		out := make([]any, len(val))
		for i, item := range val {
			rendered, err := renderValue(item, ctx)
			if err != nil {
				return nil, err
			}
			out[i] = rendered
		}
		return out, nil
	default:
		return val, nil
	}
}

// Example returns a reference scenario document used in README and tests.
func Example() string {
	return strings.TrimSpace(`
name: awesome-mcp-server-hot-path
transport:
  type: stdio
  cmd: node
  args: ["./dist/server.js"]
  env:
    LOG_LEVEL: warn
workload:
  rate: 100
  duration: 30s
  warmup: 5s
  cooldown: 2s
  timeout: 5s
tools:
  - name: read_file
    weight: 3
    args:
      path: "/tmp/a.txt"
  - name: search
    weight: 1
    args:
      pattern: "iter-{{.Iter}}"
      max_results: 10
`) + "\n"
}
