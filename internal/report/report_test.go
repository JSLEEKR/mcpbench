package report

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
	"time"

	mcperrors "github.com/JSLEEKR/mcpbench/internal/errors"
	"github.com/JSLEEKR/mcpbench/internal/metrics"
)

func sampleRun() *RunFile {
	a := metrics.ToolSummary{
		Name: "read",
		Latency: metrics.Summary{
			Count: 100, SampleCount: 100,
			P50: 5 * time.Millisecond, P95: 15 * time.Millisecond, P99: 30 * time.Millisecond,
			Mean: 8 * time.Millisecond, Min: 1 * time.Millisecond, Max: 35 * time.Millisecond,
		},
		OK: 99, Errors: 1, ErrorPct: 1,
		Breakdown: map[mcperrors.Category]int64{mcperrors.CategoryTimeout: 1},
	}
	b := metrics.ToolSummary{
		Name: "search",
		Latency: metrics.Summary{
			Count: 50, SampleCount: 50,
			P50: 10 * time.Millisecond, P95: 25 * time.Millisecond, P99: 40 * time.Millisecond,
			Mean: 12 * time.Millisecond, Min: 2 * time.Millisecond, Max: 42 * time.Millisecond,
		},
		OK: 50, Errors: 0, ErrorPct: 0,
	}
	overall := metrics.ToolSummary{
		Name: "__overall__",
		Latency: metrics.Summary{
			Count: 150, SampleCount: 150,
			P50: 7 * time.Millisecond, P95: 20 * time.Millisecond, P99: 35 * time.Millisecond,
			Mean: 10 * time.Millisecond, Min: 1 * time.Millisecond, Max: 42 * time.Millisecond,
		},
		OK: 149, Errors: 1, ErrorPct: 100.0 / 150,
		Breakdown: map[mcperrors.Category]int64{mcperrors.CategoryTimeout: 1},
	}
	return &RunFile{
		Version:    Version,
		Scenario:   "demo",
		StartedAt:  time.Date(2026, 4, 17, 10, 0, 0, 0, time.UTC),
		EndedAt:    time.Date(2026, 4, 17, 10, 0, 5, 0, time.UTC),
		Throughput: 30,
		Summary: metrics.AggregateSummary{
			Overall:  overall,
			PerTool:  map[string]metrics.ToolSummary{"read": a, "search": b},
			ToolKeys: []string{"read", "search"},
		},
	}
}

func TestParseFormat(t *testing.T) {
	cases := map[string]Format{
		"human":      FormatHuman,
		"":           FormatHuman,
		"JSON":       FormatJSON,
		"csv":        FormatCSV,
		"prom":       FormatPrometheus,
		"prometheus": FormatPrometheus,
	}
	for in, want := range cases {
		got, err := ParseFormat(in)
		if err != nil {
			t.Fatal(in, err)
		}
		if got != want {
			t.Fatalf("%s: got %s want %s", in, got, want)
		}
	}
	if _, err := ParseFormat("bogus"); err == nil {
		t.Fatal("expected error")
	}
}

func TestWriteHuman(t *testing.T) {
	var buf bytes.Buffer
	if err := WriteHuman(&buf, sampleRun()); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	for _, want := range []string{"scenario", "demo", "throughput", "TOOL", "read", "search", "__overall__", "timeout=1"} {
		if !strings.Contains(out, want) {
			t.Fatalf("missing %q in:\n%s", want, out)
		}
	}
}

func TestWriteJSON(t *testing.T) {
	var buf bytes.Buffer
	if err := WriteJSON(&buf, sampleRun()); err != nil {
		t.Fatal(err)
	}
	var back RunFile
	if err := json.Unmarshal(buf.Bytes(), &back); err != nil {
		t.Fatalf("invalid json: %v\n%s", err, buf.String())
	}
	if back.Scenario != "demo" {
		t.Fatal("scenario")
	}
	if back.Summary.Overall.Latency.P95 != 20*time.Millisecond {
		t.Fatalf("p95 = %v", back.Summary.Overall.Latency.P95)
	}
}

func TestWriteCSV(t *testing.T) {
	var buf bytes.Buffer
	if err := WriteCSV(&buf, sampleRun()); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	if !strings.HasPrefix(out, "tool,count,") {
		t.Fatalf("header wrong: %s", out)
	}
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if len(lines) < 4 {
		t.Fatalf("lines = %d", len(lines))
	}
}

func TestWriteCSVToolWithComma(t *testing.T) {
	rf := sampleRun()
	rf.Summary.PerTool["weird,name"] = rf.Summary.PerTool["read"]
	rf.Summary.ToolKeys = append(rf.Summary.ToolKeys, "weird,name")
	var buf bytes.Buffer
	if err := WriteCSV(&buf, rf); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(buf.String(), `"weird,name"`) {
		t.Fatalf("csv did not escape comma:\n%s", buf.String())
	}
}

func TestWritePrometheus(t *testing.T) {
	var buf bytes.Buffer
	if err := WritePrometheus(&buf, sampleRun()); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	for _, want := range []string{
		"# TYPE mcpbench_requests_total",
		`mcpbench_requests_total{scenario="demo",tool="__overall__",status="ok"}`,
		`mcpbench_latency_seconds{scenario="demo",tool="read",quantile="0.95"}`,
		`mcpbench_throughput_per_second{scenario="demo"}`,
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("missing %q in:\n%s", want, out)
		}
	}
}

func TestWritePrometheusSanitizesQuotes(t *testing.T) {
	rf := sampleRun()
	rf.Scenario = `evil"scn`
	var buf bytes.Buffer
	if err := WritePrometheus(&buf, rf); err != nil {
		t.Fatal(err)
	}
	// Quotes must be escaped (we replace with underscore).
	if strings.Contains(buf.String(), `evil"scn`) {
		t.Fatal("unsanitized quote")
	}
}

func TestWriteDispatch(t *testing.T) {
	rf := sampleRun()
	for _, f := range []Format{FormatHuman, FormatJSON, FormatCSV, FormatPrometheus} {
		t.Run(string(f), func(t *testing.T) {
			var buf bytes.Buffer
			if err := Write(&buf, f, rf); err != nil {
				t.Fatal(err)
			}
			if buf.Len() == 0 {
				t.Fatal("empty")
			}
		})
	}
}

func TestWriteUnknownFormat(t *testing.T) {
	var buf bytes.Buffer
	if err := Write(&buf, "xyz", sampleRun()); err == nil {
		t.Fatal("expected error")
	}
}

func TestFmtErrBreakdownEmpty(t *testing.T) {
	if got := fmtErrBreakdown(nil); got != "-" {
		t.Fatal(got)
	}
	if got := fmtErrBreakdown(map[mcperrors.Category]int64{}); got != "-" {
		t.Fatal(got)
	}
}

func TestFmtErrBreakdownAllZero(t *testing.T) {
	if got := fmtErrBreakdown(map[mcperrors.Category]int64{mcperrors.CategoryTimeout: 0}); got != "-" {
		t.Fatal(got)
	}
}

func TestFmtDurZero(t *testing.T) {
	if got := fmtDur(0); got != "0" {
		t.Fatal(got)
	}
}

func TestSanitizeLabel(t *testing.T) {
	if sanitizeLabel("a\"b") != "a_b" {
		t.Fatal("quote")
	}
	if sanitizeLabel("a\nb") != "a_b" {
		t.Fatal("newline")
	}
	if sanitizeLabel("a\\b") != "a_b" {
		t.Fatal("backslash")
	}
	if sanitizeLabel("simple") != "simple" {
		t.Fatal("unchanged")
	}
}

// TestSanitizeLabelStripsCR is a regression test: sanitizeLabel used to pass
// through '\r' unchanged, so a scenario (or tool) name containing a carriage
// return injected a bare CR into the Prometheus exposition stream. Most
// Prometheus parsers treat CR as a line terminator, splitting one metric line
// into two syntactically invalid lines and failing the scrape. Replace CR
// with '_' alongside the other forbidden characters.
func TestSanitizeLabelStripsCR(t *testing.T) {
	if got := sanitizeLabel("bad\rname"); got != "bad_name" {
		t.Fatalf("CR not stripped: %q", got)
	}
}

// TestWritePrometheusSanitizesCR is the end-to-end counterpart of the
// sanitizer unit test: no '\r' must survive into the rendered exposition
// document regardless of the scenario or tool name content.
func TestWritePrometheusSanitizesCR(t *testing.T) {
	rf := sampleRun()
	rf.Scenario = "bad\rscenario"
	var buf bytes.Buffer
	if err := WritePrometheus(&buf, rf); err != nil {
		t.Fatal(err)
	}
	if strings.ContainsRune(buf.String(), '\r') {
		t.Fatalf("CR leaked into Prometheus exposition:\n%s", buf.String())
	}
}
