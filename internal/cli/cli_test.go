package cli

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/JSLEEKR/mcpbench/internal/metrics"
	"github.com/JSLEEKR/mcpbench/internal/report"
	"github.com/JSLEEKR/mcpbench/internal/scenario"
)

func minimalScenarioStruct() *scenario.Scenario {
	return &scenario.Scenario{
		Name:      "t",
		Transport: scenario.TransportConfig{Type: "stdio", Cmd: "x"},
		Workload:  scenario.Workload{Rate: 1, Duration: time.Second},
		Tools:     []scenario.ToolCall{{Name: "a", Weight: 1}},
	}
}

func TestVersionCommand(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if err := Execute([]string{"version"}, &stdout, &stderr); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stdout.String(), "mcpbench") {
		t.Fatalf("out = %s", stdout.String())
	}
}

func TestRootShowsHelp(t *testing.T) {
	var stdout, stderr bytes.Buffer
	_ = Execute([]string{"--help"}, &stdout, &stderr)
	out := stdout.String() + stderr.String()
	if !strings.Contains(out, "mcpbench") {
		t.Fatalf("out = %s", out)
	}
}

func TestRunRequiresScenario(t *testing.T) {
	var stdout, stderr bytes.Buffer
	err := Execute([]string{"run"}, &stdout, &stderr)
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestRunRejectsMutualExclusiveBudgets(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "s.yml")
	_ = os.WriteFile(path, []byte(`
name: t
transport: {type: stdio, cmd: /bin/false}
workload: {requests: 1, concurrency: 1}
tools: [{name: a}]
`), 0o600)
	var stdout, stderr bytes.Buffer
	err := Execute([]string{"run", "--scenario", path, "--duration", "1s", "--requests", "10"}, &stdout, &stderr)
	if err == nil || !strings.Contains(err.Error(), "mutually exclusive") {
		t.Fatalf("err = %v", err)
	}
}

func TestScenarioValidateOK(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "s.yml")
	_ = os.WriteFile(path, []byte(`
name: t
transport: {type: stdio, cmd: x}
workload: {requests: 1, concurrency: 1}
tools: [{name: a}]
`), 0o600)
	var stdout, stderr bytes.Buffer
	if err := Execute([]string{"scenario-validate", path}, &stdout, &stderr); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stdout.String(), "OK") {
		t.Fatal("expected OK")
	}
}

func TestScenarioValidateBad(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "bad.yml")
	_ = os.WriteFile(path, []byte(`name: t`), 0o600)
	var stdout, stderr bytes.Buffer
	if err := Execute([]string{"scenario-validate", path}, &stdout, &stderr); err == nil {
		t.Fatal("expected error")
	}
}

func TestCompareHappyPath(t *testing.T) {
	dir := t.TempDir()
	a := filepath.Join(dir, "a.json")
	b := filepath.Join(dir, "b.json")
	rf := sampleRunJSON()
	_ = os.WriteFile(a, rf, 0o600)
	_ = os.WriteFile(b, rf, 0o600)
	var stdout, stderr bytes.Buffer
	if err := Execute([]string{"compare", a, b}, &stdout, &stderr); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stdout.String(), "OK") {
		t.Fatalf("out: %s", stdout.String())
	}
}

func TestCompareDetectsRegression(t *testing.T) {
	dir := t.TempDir()
	a := filepath.Join(dir, "a.json")
	b := filepath.Join(dir, "b.json")
	_ = os.WriteFile(a, sampleRunJSON(), 0o600)
	_ = os.WriteFile(b, regressedRunJSON(), 0o600)
	var stdout, stderr bytes.Buffer
	err := Execute([]string{"compare", a, b, "--threshold-p95=+10%"}, &stdout, &stderr)
	if !errors.Is(err, ErrRegression) {
		t.Fatalf("err = %v", err)
	}
}

func TestCompareJSONOutput(t *testing.T) {
	dir := t.TempDir()
	a := filepath.Join(dir, "a.json")
	b := filepath.Join(dir, "b.json")
	_ = os.WriteFile(a, sampleRunJSON(), 0o600)
	_ = os.WriteFile(b, sampleRunJSON(), 0o600)
	var stdout, stderr bytes.Buffer
	if err := Execute([]string{"compare", a, b, "--output", "json"}, &stdout, &stderr); err != nil {
		t.Fatal(err)
	}
	var back map[string]any
	if err := json.Unmarshal(stdout.Bytes(), &back); err != nil {
		t.Fatalf("bad json: %v\n%s", err, stdout.String())
	}
}

func TestCompareBadThreshold(t *testing.T) {
	dir := t.TempDir()
	a := filepath.Join(dir, "a.json")
	b := filepath.Join(dir, "b.json")
	_ = os.WriteFile(a, sampleRunJSON(), 0o600)
	_ = os.WriteFile(b, sampleRunJSON(), 0o600)
	var stdout, stderr bytes.Buffer
	err := Execute([]string{"compare", a, b, "--threshold-p95=bad"}, &stdout, &stderr)
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestCompareMissingFile(t *testing.T) {
	var stdout, stderr bytes.Buffer
	err := Execute([]string{"compare", "/nope1.json", "/nope2.json"}, &stdout, &stderr)
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestCompareUnknownOutput(t *testing.T) {
	dir := t.TempDir()
	a := filepath.Join(dir, "a.json")
	b := filepath.Join(dir, "b.json")
	_ = os.WriteFile(a, sampleRunJSON(), 0o600)
	_ = os.WriteFile(b, sampleRunJSON(), 0o600)
	var stdout, stderr bytes.Buffer
	err := Execute([]string{"compare", a, b, "--output", "xml"}, &stdout, &stderr)
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestCompareExactlyTwoArgs(t *testing.T) {
	var stdout, stderr bytes.Buffer
	err := Execute([]string{"compare", "only-one"}, &stdout, &stderr)
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestApplyCLIOverridesSpawnParses(t *testing.T) {
	s := minimalScenarioStruct()
	f := &runFlags{spawn: "node server.js --port 1"}
	applyCLIOverrides(s, f)
	if s.Transport.Cmd != "node" {
		t.Fatalf("cmd %s", s.Transport.Cmd)
	}
	if len(s.Transport.Args) != 3 {
		t.Fatalf("args %v", s.Transport.Args)
	}
}

func TestApplyCLIOverridesDurationSwitchesRequests(t *testing.T) {
	s := minimalScenarioStruct()
	s.Workload.Requests = 10
	f := &runFlags{duration: 5 * time.Second}
	applyCLIOverrides(s, f)
	if s.Workload.Requests != 0 {
		t.Fatal("requests not cleared")
	}
	if s.Workload.Duration != 5*time.Second {
		t.Fatal("duration")
	}
}

func TestApplyCLIOverridesRequestsSwitchesDuration(t *testing.T) {
	s := minimalScenarioStruct()
	s.Workload.Duration = 5 * time.Second
	f := &runFlags{requests: 10}
	applyCLIOverrides(s, f)
	if s.Workload.Duration != 0 {
		t.Fatal("duration not cleared")
	}
}

func sampleRunJSON() []byte {
	rf := &report.RunFile{
		Version:    report.Version,
		Scenario:   "demo",
		StartedAt:  time.Now(),
		EndedAt:    time.Now().Add(time.Second),
		Throughput: 10,
		Summary: metrics.AggregateSummary{
			Overall: metrics.ToolSummary{
				Latency: metrics.Summary{
					Count: 100,
					P95:   20 * time.Millisecond,
					P99:   40 * time.Millisecond,
				},
				OK:       100,
				ErrorPct: 0,
			},
			PerTool:  map[string]metrics.ToolSummary{},
			ToolKeys: []string{},
		},
	}
	b, _ := json.Marshal(rf)
	return b
}

func regressedRunJSON() []byte {
	rf := &report.RunFile{
		Version:    report.Version,
		Scenario:   "demo",
		StartedAt:  time.Now(),
		EndedAt:    time.Now().Add(time.Second),
		Throughput: 10,
		Summary: metrics.AggregateSummary{
			Overall: metrics.ToolSummary{
				Latency: metrics.Summary{
					Count: 100,
					P95:   50 * time.Millisecond, // 150% up
					P99:   80 * time.Millisecond,
				},
				OK: 100,
			},
			PerTool:  map[string]metrics.ToolSummary{},
			ToolKeys: []string{},
		},
	}
	b, _ := json.Marshal(rf)
	return b
}
