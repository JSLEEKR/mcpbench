package compare

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/JSLEEKR/mcpbench/internal/metrics"
)

func TestParseThresholdRelative(t *testing.T) {
	th, err := ParseThreshold("+20%")
	if err != nil {
		t.Fatal(err)
	}
	if th.Kind != ThresholdKindRelative {
		t.Fatal("kind")
	}
	if th.Value-0.2 > 1e-9 || 0.2-th.Value > 1e-9 {
		t.Fatalf("value %f", th.Value)
	}
}

func TestParseThresholdNegative(t *testing.T) {
	th, err := ParseThreshold("-5%")
	if err != nil {
		t.Fatal(err)
	}
	if th.Value > 0 {
		t.Fatalf("value %f", th.Value)
	}
}

func TestParseThresholdAbsoluteMS(t *testing.T) {
	th, err := ParseThreshold("+10ms")
	if err != nil {
		t.Fatal(err)
	}
	if th.Kind != ThresholdKindAbsoluteMS {
		t.Fatal("kind")
	}
	if th.Value != 10 {
		t.Fatalf("value %f", th.Value)
	}
}

func TestParseThresholdPercentagePoint(t *testing.T) {
	th, err := ParseThreshold("+2pp")
	if err != nil {
		t.Fatal(err)
	}
	if th.Kind != ThresholdKindPercentagePoint {
		t.Fatal("kind")
	}
	if th.Value != 2 {
		t.Fatalf("value %f", th.Value)
	}
}

func TestParseThresholdErrors(t *testing.T) {
	cases := []string{"", "20%", "+abc%", "20", "+five", "+20xx"}
	for _, c := range cases {
		t.Run(c, func(t *testing.T) {
			_, err := ParseThreshold(c)
			if err == nil {
				t.Fatal("expected error")
			}
		})
	}
}

func makeRunFile(tools map[string]metrics.ToolSummary, overall metrics.ToolSummary) *RunFile {
	keys := make([]string, 0, len(tools))
	for k := range tools {
		keys = append(keys, k)
	}
	return &RunFile{
		Version:  "v0",
		Scenario: "t",
		Summary: metrics.AggregateSummary{
			Overall:  overall,
			PerTool:  tools,
			ToolKeys: keys,
		},
	}
}

func makeToolSummary(p50, p95, p99 time.Duration, errPct float64, count int) metrics.ToolSummary {
	return metrics.ToolSummary{
		Latency: metrics.Summary{
			Count: count, SampleCount: count,
			P50: p50, P95: p95, P99: p99,
			Min: p50, Max: p99, Mean: p50,
		},
		ErrorPct: errPct,
	}
}

func TestCompareIdenticalRunsNoRegression(t *testing.T) {
	sum := makeToolSummary(10*time.Millisecond, 20*time.Millisecond, 40*time.Millisecond, 0, 100)
	baseline := makeRunFile(map[string]metrics.ToolSummary{"a": sum}, sum)
	candidate := makeRunFile(map[string]metrics.ToolSummary{"a": sum}, sum)
	rpt := Compare(baseline, candidate, Default())
	if rpt.Regressed {
		t.Fatalf("unexpected regression: %s", rpt.SummaryNote)
	}
}

func TestComparePassBelowThreshold(t *testing.T) {
	a := makeToolSummary(10*time.Millisecond, 100*time.Millisecond, 200*time.Millisecond, 1, 100)
	b := makeToolSummary(11*time.Millisecond, 110*time.Millisecond, 220*time.Millisecond, 1, 100) // +10% p95
	baseline := makeRunFile(map[string]metrics.ToolSummary{"x": a}, a)
	candidate := makeRunFile(map[string]metrics.ToolSummary{"x": b}, b)
	rpt := Compare(baseline, candidate, Default()) // default +20%
	if rpt.Regressed {
		t.Fatalf("expected pass, got: %+v", rpt)
	}
}

func TestCompareFailOverThreshold(t *testing.T) {
	a := makeToolSummary(10*time.Millisecond, 100*time.Millisecond, 200*time.Millisecond, 1, 100)
	b := makeToolSummary(11*time.Millisecond, 150*time.Millisecond, 250*time.Millisecond, 1, 100) // +50% p95
	baseline := makeRunFile(map[string]metrics.ToolSummary{"x": a}, a)
	candidate := makeRunFile(map[string]metrics.ToolSummary{"x": b}, b)
	rpt := Compare(baseline, candidate, Default())
	if !rpt.Regressed {
		t.Fatal("expected regression")
	}
	if len(rpt.PerTool[0].Reasons) == 0 {
		t.Fatal("no reasons")
	}
}

func TestCompareErrorRateRegression(t *testing.T) {
	a := makeToolSummary(10*time.Millisecond, 20*time.Millisecond, 40*time.Millisecond, 1.0, 100)
	b := makeToolSummary(10*time.Millisecond, 20*time.Millisecond, 40*time.Millisecond, 5.0, 100) // +4pp err
	baseline := makeRunFile(map[string]metrics.ToolSummary{"x": a}, a)
	candidate := makeRunFile(map[string]metrics.ToolSummary{"x": b}, b)
	rpt := Compare(baseline, candidate, Default()) // default +2pp error
	if !rpt.Regressed {
		t.Fatal("expected error regression")
	}
}

func TestCompareErrorRateBelowThreshold(t *testing.T) {
	a := makeToolSummary(10*time.Millisecond, 20*time.Millisecond, 40*time.Millisecond, 1.0, 100)
	b := makeToolSummary(10*time.Millisecond, 20*time.Millisecond, 40*time.Millisecond, 2.0, 100) // +1pp
	baseline := makeRunFile(map[string]metrics.ToolSummary{"x": a}, a)
	candidate := makeRunFile(map[string]metrics.ToolSummary{"x": b}, b)
	rpt := Compare(baseline, candidate, Default())
	if rpt.Regressed {
		t.Fatal("did not expect regression")
	}
}

func TestCompareAbsoluteMSThreshold(t *testing.T) {
	a := makeToolSummary(1*time.Millisecond, 2*time.Millisecond, 3*time.Millisecond, 0, 100)
	b := makeToolSummary(1*time.Millisecond, 15*time.Millisecond, 3*time.Millisecond, 0, 100) // +13ms
	baseline := makeRunFile(map[string]metrics.ToolSummary{"x": a}, a)
	candidate := makeRunFile(map[string]metrics.ToolSummary{"x": b}, b)
	th := Threshold{Kind: ThresholdKindAbsoluteMS, Value: 10}
	cfg := Config{P95Threshold: &th, PerTool: true}
	rpt := Compare(baseline, candidate, cfg)
	if !rpt.Regressed {
		t.Fatal("expected ms regression")
	}
}

func TestCompareUniqueTools(t *testing.T) {
	sum := makeToolSummary(10*time.Millisecond, 20*time.Millisecond, 40*time.Millisecond, 0, 100)
	baseline := makeRunFile(map[string]metrics.ToolSummary{"a": sum, "b": sum}, sum)
	candidate := makeRunFile(map[string]metrics.ToolSummary{"b": sum, "c": sum}, sum)
	rpt := Compare(baseline, candidate, Default())
	if len(rpt.UniqueToA) != 1 || rpt.UniqueToA[0] != "a" {
		t.Fatalf("uniqueA = %v", rpt.UniqueToA)
	}
	if len(rpt.UniqueToB) != 1 || rpt.UniqueToB[0] != "c" {
		t.Fatalf("uniqueB = %v", rpt.UniqueToB)
	}
}

func TestCompareOverallRegressesEvenIfNoPerTool(t *testing.T) {
	a := makeToolSummary(10*time.Millisecond, 100*time.Millisecond, 200*time.Millisecond, 0, 100)
	b := makeToolSummary(11*time.Millisecond, 150*time.Millisecond, 220*time.Millisecond, 0, 100)
	baseline := makeRunFile(map[string]metrics.ToolSummary{}, a)
	candidate := makeRunFile(map[string]metrics.ToolSummary{}, b)
	rpt := Compare(baseline, candidate, Default())
	if !rpt.Regressed {
		t.Fatal("expected overall regression")
	}
}

func TestWriteText(t *testing.T) {
	sum := makeToolSummary(10*time.Millisecond, 20*time.Millisecond, 40*time.Millisecond, 0, 100)
	baseline := makeRunFile(map[string]metrics.ToolSummary{"a": sum}, sum)
	candidate := makeRunFile(map[string]metrics.ToolSummary{"a": sum}, sum)
	rpt := Compare(baseline, candidate, Default())
	var buf bytes.Buffer
	rpt.WriteText(&buf)
	if !strings.Contains(buf.String(), "OK") {
		t.Fatalf("out = %s", buf.String())
	}
}

func TestWriteJSON(t *testing.T) {
	sum := makeToolSummary(10*time.Millisecond, 20*time.Millisecond, 40*time.Millisecond, 0, 100)
	baseline := makeRunFile(map[string]metrics.ToolSummary{"a": sum}, sum)
	candidate := makeRunFile(map[string]metrics.ToolSummary{"a": sum}, sum)
	rpt := Compare(baseline, candidate, Default())
	var buf bytes.Buffer
	if err := rpt.WriteJSON(&buf); err != nil {
		t.Fatal(err)
	}
	var back map[string]any
	if err := json.Unmarshal(buf.Bytes(), &back); err != nil {
		t.Fatalf("invalid json: %v\n%s", err, buf.String())
	}
	if back["regressed"] != false {
		t.Fatal("regressed field")
	}
}

func TestLoadRunFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "run.json")
	sum := makeToolSummary(10*time.Millisecond, 20*time.Millisecond, 40*time.Millisecond, 0, 100)
	rf := makeRunFile(map[string]metrics.ToolSummary{"a": sum}, sum)
	data, _ := json.Marshal(rf)
	_ = os.WriteFile(path, data, 0o600)
	loaded, err := LoadRun(path)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Summary.Overall.Latency.P95 != 20*time.Millisecond {
		t.Fatalf("p95 = %v", loaded.Summary.Overall.Latency.P95)
	}
}

func TestLoadRunFileMissing(t *testing.T) {
	_, err := LoadRun("/definitely/not/here.json")
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestLoadRunFileInvalid(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "bad.json")
	_ = os.WriteFile(path, []byte("not json"), 0o600)
	_, err := LoadRun(path)
	if err == nil {
		t.Fatal("expected parse error")
	}
}

func TestPerToolRegressedPropagatesToOverall(t *testing.T) {
	good := makeToolSummary(10*time.Millisecond, 20*time.Millisecond, 40*time.Millisecond, 0, 100)
	bad := makeToolSummary(10*time.Millisecond, 200*time.Millisecond, 400*time.Millisecond, 0, 100)
	baseline := makeRunFile(map[string]metrics.ToolSummary{"good": good, "bad": good}, good)
	candidate := makeRunFile(map[string]metrics.ToolSummary{"good": good, "bad": bad}, good)
	rpt := Compare(baseline, candidate, Default())
	if !rpt.Regressed {
		t.Fatal("expected aggregate regression")
	}
}

func TestDefaultConfigEntries(t *testing.T) {
	cfg := Default()
	if cfg.P95Threshold == nil || cfg.P99Threshold == nil || cfg.ErrorThreshold == nil {
		t.Fatal("default missing thresholds")
	}
	if !cfg.PerTool {
		t.Fatal("default PerTool")
	}
}

func TestDisablePerTool(t *testing.T) {
	good := makeToolSummary(10*time.Millisecond, 20*time.Millisecond, 40*time.Millisecond, 0, 100)
	baseline := makeRunFile(map[string]metrics.ToolSummary{"a": good}, good)
	candidate := makeRunFile(map[string]metrics.ToolSummary{"a": good}, good)
	cfg := Default()
	cfg.PerTool = false
	rpt := Compare(baseline, candidate, cfg)
	if len(rpt.PerTool) != 0 {
		t.Fatal("per-tool should be disabled")
	}
}

func TestDiffZeroBaseline(t *testing.T) {
	// A tool with zero baseline p95 should not divide by zero.
	a := makeToolSummary(0, 0, 0, 0, 0)
	b := makeToolSummary(1*time.Millisecond, 10*time.Millisecond, 20*time.Millisecond, 0, 10)
	baseline := makeRunFile(map[string]metrics.ToolSummary{"x": a}, a)
	candidate := makeRunFile(map[string]metrics.ToolSummary{"x": b}, b)
	rpt := Compare(baseline, candidate, Default())
	_ = rpt // not crashing is the win
}
