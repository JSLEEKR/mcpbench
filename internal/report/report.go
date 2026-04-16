// Package report renders aggregate summaries into human, json, csv, and
// prometheus text-exposition formats.
package report

import (
	"encoding/csv"
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strings"
	"text/tabwriter"
	"time"

	mcperrors "github.com/JSLEEKR/mcpbench/internal/errors"
	"github.com/JSLEEKR/mcpbench/internal/metrics"
)

// Format enumerates supported output formats.
type Format string

// Known formats.
const (
	FormatHuman      Format = "human"
	FormatJSON       Format = "json"
	FormatCSV        Format = "csv"
	FormatPrometheus Format = "prom"
)

// ParseFormat normalizes a user-provided format name.
func ParseFormat(s string) (Format, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "human", "", "text", "table":
		return FormatHuman, nil
	case "json":
		return FormatJSON, nil
	case "csv":
		return FormatCSV, nil
	case "prom", "prometheus":
		return FormatPrometheus, nil
	default:
		return "", fmt.Errorf("report: unknown format %q", s)
	}
}

// RunFile is the JSON-serialized representation of a completed run.
// Keep in sync with internal/compare.RunFile.
type RunFile struct {
	Version    string                   `json:"version"`
	Scenario   string                   `json:"scenario"`
	StartedAt  time.Time                `json:"started_at"`
	EndedAt    time.Time                `json:"ended_at"`
	Throughput float64                  `json:"throughput_per_sec"`
	Summary    metrics.AggregateSummary `json:"summary"`
}

// Version is the current on-disk format version for RunFile JSON.
const Version = "mcpbench/v1"

// Write dispatches to the format-specific writer.
func Write(w io.Writer, format Format, rf *RunFile) error {
	switch format {
	case FormatHuman:
		return WriteHuman(w, rf)
	case FormatJSON:
		return WriteJSON(w, rf)
	case FormatCSV:
		return WriteCSV(w, rf)
	case FormatPrometheus:
		return WritePrometheus(w, rf)
	default:
		return fmt.Errorf("report: unsupported format %q", format)
	}
}

// WriteHuman renders a tabular human-readable report.
func WriteHuman(w io.Writer, rf *RunFile) error {
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintf(tw, "scenario\t%s\n", rf.Scenario)
	if !rf.StartedAt.IsZero() {
		fmt.Fprintf(tw, "started\t%s\n", rf.StartedAt.UTC().Format(time.RFC3339))
	}
	if !rf.EndedAt.IsZero() {
		fmt.Fprintf(tw, "ended\t%s\n", rf.EndedAt.UTC().Format(time.RFC3339))
	}
	if rf.EndedAt.After(rf.StartedAt) {
		fmt.Fprintf(tw, "duration\t%s\n", rf.EndedAt.Sub(rf.StartedAt).Round(time.Millisecond))
	}
	fmt.Fprintf(tw, "throughput\t%.1f req/s\n", rf.Throughput)
	fmt.Fprintln(tw, "")
	fmt.Fprintln(tw, "TOOL\tCOUNT\tP50\tP95\tP99\tMEAN\tERR%\tERRORS")
	writeToolRow(tw, "__overall__", rf.Summary.Overall)
	for _, k := range rf.Summary.ToolKeys {
		writeToolRow(tw, k, rf.Summary.PerTool[k])
	}
	return tw.Flush()
}

func writeToolRow(w io.Writer, name string, s metrics.ToolSummary) {
	fmt.Fprintf(w, "%s\t%d\t%s\t%s\t%s\t%s\t%.2f\t%s\n",
		name,
		s.Latency.Count,
		fmtDur(s.Latency.P50),
		fmtDur(s.Latency.P95),
		fmtDur(s.Latency.P99),
		fmtDur(s.Latency.Mean),
		s.ErrorPct,
		fmtErrBreakdown(s.Breakdown),
	)
}

func fmtDur(d time.Duration) string {
	if d == 0 {
		return "0"
	}
	return d.Round(time.Microsecond).String()
}

func fmtErrBreakdown(br map[mcperrors.Category]int64) string {
	if len(br) == 0 {
		return "-"
	}
	keys := make([]string, 0, len(br))
	for k := range br {
		keys = append(keys, string(k))
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		v := br[mcperrors.Category(k)]
		if v > 0 {
			parts = append(parts, fmt.Sprintf("%s=%d", k, v))
		}
	}
	if len(parts) == 0 {
		return "-"
	}
	return strings.Join(parts, ",")
}

// WriteJSON writes the full run as indented JSON.
func WriteJSON(w io.Writer, rf *RunFile) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(rf)
}

// WriteCSV writes a CSV with one row per tool (plus a synthetic __overall__).
func WriteCSV(w io.Writer, rf *RunFile) error {
	cw := csv.NewWriter(w)
	defer cw.Flush()
	if err := cw.Write([]string{"tool", "count", "p50_ms", "p95_ms", "p99_ms", "mean_ms", "error_pct", "ok", "errors"}); err != nil {
		return err
	}
	if err := cw.Write(csvRow("__overall__", rf.Summary.Overall)); err != nil {
		return err
	}
	for _, k := range rf.Summary.ToolKeys {
		if err := cw.Write(csvRow(k, rf.Summary.PerTool[k])); err != nil {
			return err
		}
	}
	cw.Flush()
	return cw.Error()
}

func csvRow(name string, s metrics.ToolSummary) []string {
	return []string{
		name,
		fmt.Sprintf("%d", s.Latency.Count),
		fmt.Sprintf("%.3f", ms(s.Latency.P50)),
		fmt.Sprintf("%.3f", ms(s.Latency.P95)),
		fmt.Sprintf("%.3f", ms(s.Latency.P99)),
		fmt.Sprintf("%.3f", ms(s.Latency.Mean)),
		fmt.Sprintf("%.3f", s.ErrorPct),
		fmt.Sprintf("%d", s.OK),
		fmt.Sprintf("%d", s.Errors),
	}
}

func ms(d time.Duration) float64 {
	return float64(d) / float64(time.Millisecond)
}

// WritePrometheus emits a text-exposition document compatible with
// prometheus scrape and most TSDBs' textfile import.
func WritePrometheus(w io.Writer, rf *RunFile) error {
	scn := sanitizeLabel(rf.Scenario)
	fmt.Fprintln(w, "# TYPE mcpbench_requests_total counter")
	fmt.Fprintln(w, "# TYPE mcpbench_latency_seconds gauge")
	fmt.Fprintln(w, "# TYPE mcpbench_throughput_per_second gauge")
	fmt.Fprintln(w, "# TYPE mcpbench_error_ratio gauge")

	emit := func(tool string, s metrics.ToolSummary) {
		toolLabel := sanitizeLabel(tool)
		fmt.Fprintf(w, `mcpbench_requests_total{scenario="%s",tool="%s",status="ok"} %d`+"\n", scn, toolLabel, s.OK)
		fmt.Fprintf(w, `mcpbench_requests_total{scenario="%s",tool="%s",status="error"} %d`+"\n", scn, toolLabel, s.Errors)
		fmt.Fprintf(w, `mcpbench_latency_seconds{scenario="%s",tool="%s",quantile="0.5"} %g`+"\n", scn, toolLabel, seconds(s.Latency.P50))
		fmt.Fprintf(w, `mcpbench_latency_seconds{scenario="%s",tool="%s",quantile="0.95"} %g`+"\n", scn, toolLabel, seconds(s.Latency.P95))
		fmt.Fprintf(w, `mcpbench_latency_seconds{scenario="%s",tool="%s",quantile="0.99"} %g`+"\n", scn, toolLabel, seconds(s.Latency.P99))
		fmt.Fprintf(w, `mcpbench_error_ratio{scenario="%s",tool="%s"} %g`+"\n", scn, toolLabel, s.ErrorPct/100)
	}
	emit("__overall__", rf.Summary.Overall)
	for _, k := range rf.Summary.ToolKeys {
		emit(k, rf.Summary.PerTool[k])
	}
	fmt.Fprintf(w, `mcpbench_throughput_per_second{scenario="%s"} %g`+"\n", scn, rf.Throughput)
	return nil
}

func seconds(d time.Duration) float64 {
	return d.Seconds()
}

func sanitizeLabel(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		switch {
		// Replace characters that the Prometheus text-exposition grammar
		// forbids inside a double-quoted label value (", \, LF) *and* CR,
		// which is still a line terminator — leaving a CR in the output
		// would split one exposition line into two syntactically invalid
		// ones and cause scrape failures.
		case r == '"', r == '\\', r == '\n', r == '\r':
			b.WriteByte('_')
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}
