// Package compare diffs two mcpbench RunResults and applies regression
// thresholds for CI gating.
package compare

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/JSLEEKR/mcpbench/internal/metrics"
)

// ThresholdKind enumerates threshold varieties accepted by the CLI.
type ThresholdKind int

const (
	// ThresholdKindRelative represents a relative change (e.g. "+20%").
	ThresholdKindRelative ThresholdKind = iota
	// ThresholdKindAbsoluteMS represents a duration delta in milliseconds
	// (e.g. "+10ms").
	ThresholdKindAbsoluteMS
	// ThresholdKindPercentagePoint represents a percentage-point delta (e.g.
	// "+2pp") — used for error rate deltas.
	ThresholdKindPercentagePoint
)

// Threshold is a parsed comparison threshold.
type Threshold struct {
	Kind  ThresholdKind
	Value float64 // interpretation depends on Kind
}

// ParseThreshold reads a threshold string such as "+20%", "-5%", "+10ms",
// "+2pp" and returns a parsed Threshold.
func ParseThreshold(s string) (Threshold, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return Threshold{}, fmt.Errorf("threshold: empty string")
	}
	// Must start with + or -.
	if s[0] != '+' && s[0] != '-' {
		return Threshold{}, fmt.Errorf("threshold: %q must start with + or -", s)
	}
	switch {
	case strings.HasSuffix(s, "%"):
		num, err := strconv.ParseFloat(s[:len(s)-1], 64)
		if err != nil {
			return Threshold{}, fmt.Errorf("threshold: bad number in %q: %w", s, err)
		}
		return Threshold{Kind: ThresholdKindRelative, Value: num / 100.0}, nil
	case strings.HasSuffix(s, "pp"):
		num, err := strconv.ParseFloat(s[:len(s)-2], 64)
		if err != nil {
			return Threshold{}, fmt.Errorf("threshold: bad number in %q: %w", s, err)
		}
		return Threshold{Kind: ThresholdKindPercentagePoint, Value: num}, nil
	case strings.HasSuffix(s, "ms"):
		num, err := strconv.ParseFloat(s[:len(s)-2], 64)
		if err != nil {
			return Threshold{}, fmt.Errorf("threshold: bad number in %q: %w", s, err)
		}
		return Threshold{Kind: ThresholdKindAbsoluteMS, Value: num}, nil
	default:
		return Threshold{}, fmt.Errorf("threshold: %q missing suffix (%%|ms|pp)", s)
	}
}

// LoadRun reads a serialized mcpbench RunResult from disk.
func LoadRun(path string) (*RunFile, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("compare: read %s: %w", path, err)
	}
	var rf RunFile
	if err := json.Unmarshal(data, &rf); err != nil {
		return nil, fmt.Errorf("compare: parse %s: %w", path, err)
	}
	return &rf, nil
}

// RunFile mirrors the JSON structure emitted by the `run` subcommand; see
// internal/report.
type RunFile struct {
	Version    string                   `json:"version"`
	Scenario   string                   `json:"scenario"`
	StartedAt  time.Time                `json:"started_at"`
	EndedAt    time.Time                `json:"ended_at"`
	Throughput float64                  `json:"throughput_per_sec"`
	Summary    metrics.AggregateSummary `json:"summary"`
}

// Config controls the comparison behaviour.
type Config struct {
	P95Threshold   *Threshold
	P99Threshold   *Threshold
	ErrorThreshold *Threshold // must be KindPercentagePoint
	// PerTool, when true, applies thresholds to per-tool entries as well as
	// overall. Default true.
	PerTool bool
}

// Default returns sensible defaults for a CI gate.
func Default() Config {
	t := Threshold{Kind: ThresholdKindRelative, Value: 0.20}
	t99 := Threshold{Kind: ThresholdKindRelative, Value: 0.25}
	errT := Threshold{Kind: ThresholdKindPercentagePoint, Value: 2.0}
	return Config{P95Threshold: &t, P99Threshold: &t99, ErrorThreshold: &errT, PerTool: true}
}

// Diff is the resulting per-tool diff.
type Diff struct {
	Tool            string  `json:"tool"`
	BaselineCount   int     `json:"baseline_count"`
	CandidateCount  int     `json:"candidate_count"`
	BaselineP95     float64 `json:"baseline_p95_ms"`
	CandidateP95    float64 `json:"candidate_p95_ms"`
	DeltaP95Rel     float64 `json:"delta_p95_rel"`
	DeltaP95MS      float64 `json:"delta_p95_ms"`
	BaselineP99     float64 `json:"baseline_p99_ms"`
	CandidateP99    float64 `json:"candidate_p99_ms"`
	DeltaP99Rel     float64 `json:"delta_p99_rel"`
	DeltaP99MS      float64 `json:"delta_p99_ms"`
	BaselineErrPct  float64 `json:"baseline_err_pct"`
	CandidateErrPct float64 `json:"candidate_err_pct"`
	DeltaErrPP      float64 `json:"delta_err_pp"`
	Regressed       bool    `json:"regressed"`
	Reasons         []string `json:"reasons,omitempty"`
}

// Report is the full comparison output.
type Report struct {
	Overall      Diff     `json:"overall"`
	PerTool      []Diff   `json:"per_tool"`
	UniqueToA    []string `json:"only_in_baseline,omitempty"`
	UniqueToB    []string `json:"only_in_candidate,omitempty"`
	Regressed    bool     `json:"regressed"`
	SummaryNote  string   `json:"summary"`
}

// Compare builds a Report by diffing baseline against candidate using cfg.
func Compare(baseline, candidate *RunFile, cfg Config) Report {
	rpt := Report{}
	rpt.Overall = diffTool("__overall__", baseline.Summary.Overall, candidate.Summary.Overall, cfg)
	if rpt.Overall.Regressed {
		rpt.Regressed = true
	}

	if cfg.PerTool {
		aKeys := map[string]struct{}{}
		for _, k := range baseline.Summary.ToolKeys {
			aKeys[k] = struct{}{}
		}
		bKeys := map[string]struct{}{}
		for _, k := range candidate.Summary.ToolKeys {
			bKeys[k] = struct{}{}
		}
		// Tools in both.
		shared := make([]string, 0)
		for k := range aKeys {
			if _, ok := bKeys[k]; ok {
				shared = append(shared, k)
			}
		}
		sort.Strings(shared)
		for _, k := range shared {
			d := diffTool(k, baseline.Summary.PerTool[k], candidate.Summary.PerTool[k], cfg)
			rpt.PerTool = append(rpt.PerTool, d)
			if d.Regressed {
				rpt.Regressed = true
			}
		}
		for k := range aKeys {
			if _, ok := bKeys[k]; !ok {
				rpt.UniqueToA = append(rpt.UniqueToA, k)
			}
		}
		for k := range bKeys {
			if _, ok := aKeys[k]; !ok {
				rpt.UniqueToB = append(rpt.UniqueToB, k)
			}
		}
		sort.Strings(rpt.UniqueToA)
		sort.Strings(rpt.UniqueToB)
	}

	if rpt.Regressed {
		rpt.SummaryNote = "REGRESSION: one or more thresholds exceeded"
	} else {
		rpt.SummaryNote = "OK: no regressions"
	}
	return rpt
}

func diffTool(name string, a, b metrics.ToolSummary, cfg Config) Diff {
	d := Diff{
		Tool:            name,
		BaselineCount:   a.Latency.Count,
		CandidateCount:  b.Latency.Count,
		BaselineP95:     ms(a.Latency.P95),
		CandidateP95:    ms(b.Latency.P95),
		BaselineP99:     ms(a.Latency.P99),
		CandidateP99:    ms(b.Latency.P99),
		BaselineErrPct:  a.ErrorPct,
		CandidateErrPct: b.ErrorPct,
	}
	d.DeltaP95MS = d.CandidateP95 - d.BaselineP95
	d.DeltaP99MS = d.CandidateP99 - d.BaselineP99
	if d.BaselineP95 > 0 {
		d.DeltaP95Rel = d.DeltaP95MS / d.BaselineP95
	}
	if d.BaselineP99 > 0 {
		d.DeltaP99Rel = d.DeltaP99MS / d.BaselineP99
	}
	d.DeltaErrPP = d.CandidateErrPct - d.BaselineErrPct

	check := func(label string, th *Threshold, ms, rel, pp float64) {
		if th == nil {
			return
		}
		switch th.Kind {
		case ThresholdKindRelative:
			if rel > th.Value {
				d.Regressed = true
				d.Reasons = append(d.Reasons, fmt.Sprintf("%s: +%.1f%% > +%.1f%%", label, rel*100, th.Value*100))
			}
		case ThresholdKindAbsoluteMS:
			if ms > th.Value {
				d.Regressed = true
				d.Reasons = append(d.Reasons, fmt.Sprintf("%s: +%.2fms > +%.2fms", label, ms, th.Value))
			}
		case ThresholdKindPercentagePoint:
			if pp > th.Value {
				d.Regressed = true
				d.Reasons = append(d.Reasons, fmt.Sprintf("%s: +%.2fpp > +%.2fpp", label, pp, th.Value))
			}
		}
	}

	check("p95", cfg.P95Threshold, d.DeltaP95MS, d.DeltaP95Rel, 0)
	check("p99", cfg.P99Threshold, d.DeltaP99MS, d.DeltaP99Rel, 0)
	check("error", cfg.ErrorThreshold, 0, 0, d.DeltaErrPP)

	return d
}

func ms(d time.Duration) float64 {
	return float64(d) / float64(time.Millisecond)
}

// WriteText writes a human-readable comparison report.
func (r Report) WriteText(w io.Writer) {
	fmt.Fprintf(w, "mcpbench compare — %s\n", r.SummaryNote)
	fmt.Fprintln(w, strings.Repeat("-", 64))
	writeDiffLine(w, r.Overall)
	for _, d := range r.PerTool {
		writeDiffLine(w, d)
	}
	if len(r.UniqueToA) > 0 {
		fmt.Fprintf(w, "only in baseline: %s\n", strings.Join(r.UniqueToA, ", "))
	}
	if len(r.UniqueToB) > 0 {
		fmt.Fprintf(w, "only in candidate: %s\n", strings.Join(r.UniqueToB, ", "))
	}
}

func writeDiffLine(w io.Writer, d Diff) {
	status := "OK"
	if d.Regressed {
		status = "FAIL"
	}
	fmt.Fprintf(w, "%-20s  p95 %.2fms → %.2fms (%+.1f%%)  p99 %.2fms → %.2fms (%+.1f%%)  err %.2f%% → %.2f%% (%+.2fpp)  %s\n",
		d.Tool,
		d.BaselineP95, d.CandidateP95, d.DeltaP95Rel*100,
		d.BaselineP99, d.CandidateP99, d.DeltaP99Rel*100,
		d.BaselineErrPct, d.CandidateErrPct, d.DeltaErrPP,
		status,
	)
	if len(d.Reasons) > 0 {
		fmt.Fprintf(w, "  ! %s\n", strings.Join(d.Reasons, "; "))
	}
}

// WriteJSON writes the report as indented JSON.
func (r Report) WriteJSON(w io.Writer) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(r)
}
