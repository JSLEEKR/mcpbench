// Package metrics implements per-tool latency reservoirs, error counters, and
// aggregate summaries used by the reporter and compare subcommand.
package metrics

import (
	goerrors "errors"
	"math"
	"sort"
	"sync"
	"time"

	mcperrors "github.com/JSLEEKR/mcpbench/internal/errors"
)

// DefaultReservoirSize is the default number of latency samples retained per
// tool. With a 1M ring buffer the p99 is accurate up to ~3M requests per run
// (within ±0.5%). Users can lower this with NewReservoir for memory-tight
// scenarios.
const DefaultReservoirSize = 1_000_000

// Reservoir stores up to Size samples; when full, new samples replace old
// slots in round-robin order. This is a fixed-window approximation — the test
// suite documents expected behaviour.
type Reservoir struct {
	mu      sync.Mutex
	samples []time.Duration
	size    int
	count   int // total observations (can exceed size)
	next    int // next write index
}

// NewReservoir constructs a reservoir with the specified capacity. size must
// be positive; negative or zero inputs are treated as 1.
func NewReservoir(size int) *Reservoir {
	if size <= 0 {
		size = 1
	}
	return &Reservoir{
		samples: make([]time.Duration, 0, size),
		size:    size,
	}
}

// Insert adds a duration sample.
func (r *Reservoir) Insert(d time.Duration) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.samples) < r.size {
		r.samples = append(r.samples, d)
	} else {
		r.samples[r.next] = d
		r.next = (r.next + 1) % r.size
	}
	r.count++
}

// Count returns the total number of samples observed.
func (r *Reservoir) Count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.count
}

// Len returns the number of samples currently retained.
func (r *Reservoir) Len() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.samples)
}

// Snapshot returns a finalized summary. The reservoir is not mutated.
func (r *Reservoir) Snapshot() Summary {
	r.mu.Lock()
	// Copy then release the lock so percentile sort is off the hot path.
	samples := make([]time.Duration, len(r.samples))
	copy(samples, r.samples)
	count := r.count
	r.mu.Unlock()

	return summarize(samples, count)
}

// Summary is the finalized statistics for one reservoir.
type Summary struct {
	Count  int           `json:"count"`
	Min    time.Duration `json:"min"`
	Max    time.Duration `json:"max"`
	Mean   time.Duration `json:"mean"`
	StdDev time.Duration `json:"stddev"`
	P50    time.Duration `json:"p50"`
	P90    time.Duration `json:"p90"`
	P95    time.Duration `json:"p95"`
	P99    time.Duration `json:"p99"`
	// SampleCount is the number of samples retained (may be less than Count
	// when the reservoir overflowed).
	SampleCount int `json:"sample_count"`
}

func summarize(samples []time.Duration, count int) Summary {
	s := Summary{Count: count, SampleCount: len(samples)}
	if len(samples) == 0 {
		return s
	}
	sorted := make([]time.Duration, len(samples))
	copy(sorted, samples)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })
	s.Min = sorted[0]
	s.Max = sorted[len(sorted)-1]
	var sum int64
	for _, d := range sorted {
		sum += int64(d)
	}
	mean := float64(sum) / float64(len(sorted))
	s.Mean = time.Duration(mean)
	var sq float64
	for _, d := range sorted {
		diff := float64(d) - mean
		sq += diff * diff
	}
	s.StdDev = time.Duration(math.Sqrt(sq / float64(len(sorted))))
	s.P50 = percentile(sorted, 0.50)
	s.P90 = percentile(sorted, 0.90)
	s.P95 = percentile(sorted, 0.95)
	s.P99 = percentile(sorted, 0.99)
	return s
}

// percentile returns the requested p-quantile from sorted-ascending samples
// using the nearest-rank method (no interpolation). p must be in [0, 1].
func percentile(sortedAsc []time.Duration, p float64) time.Duration {
	if len(sortedAsc) == 0 {
		return 0
	}
	if p <= 0 {
		return sortedAsc[0]
	}
	if p >= 1 {
		return sortedAsc[len(sortedAsc)-1]
	}
	// Nearest rank: ceil(p * N).
	rank := int(math.Ceil(p * float64(len(sortedAsc))))
	if rank < 1 {
		rank = 1
	}
	if rank > len(sortedAsc) {
		rank = len(sortedAsc)
	}
	return sortedAsc[rank-1]
}

// ToolStats aggregates all metrics for a single tool name.
type ToolStats struct {
	Name      string
	Reservoir *Reservoir
	errCounts map[mcperrors.Category]int64
	okCount   int64
	rpcCodes  map[int]int64
	mu        sync.Mutex
}

// NewToolStats creates a ToolStats with an empty reservoir of the specified
// size.
func NewToolStats(name string, reservoirSize int) *ToolStats {
	return &ToolStats{
		Name:      name,
		Reservoir: NewReservoir(reservoirSize),
		errCounts: map[mcperrors.Category]int64{},
		rpcCodes:  map[int]int64{},
	}
}

// Record a single observation.
func (s *ToolStats) Record(dur time.Duration, err error) {
	cat := mcperrors.Classify(err)
	s.Reservoir.Insert(dur)
	s.mu.Lock()
	defer s.mu.Unlock()
	if cat == mcperrors.CategoryOK {
		s.okCount++
		return
	}
	s.errCounts[cat]++
	if cat == mcperrors.CategoryJSONRPC && err != nil {
		// Use errors.As so wrapped JSON-RPC errors (e.g.
		// fmt.Errorf("calling tool: %w", NewJSONRPCError(-32000, "x")))
		// still contribute to the per-code histogram. A direct type
		// assertion would silently drop these.
		var rpcErr mcperrors.JSONRPCError
		if goerrors.As(err, &rpcErr) {
			s.rpcCodes[rpcErr.RPCCode()]++
		}
	}
}

// OKCount returns the number of successful requests.
func (s *ToolStats) OKCount() int64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.okCount
}

// ErrorCount returns total errors across all categories.
func (s *ToolStats) ErrorCount() int64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	var n int64
	for _, v := range s.errCounts {
		n += v
	}
	return n
}

// ErrorBreakdown returns a copy of per-category counts.
func (s *ToolStats) ErrorBreakdown() map[mcperrors.Category]int64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make(map[mcperrors.Category]int64, len(s.errCounts))
	for k, v := range s.errCounts {
		out[k] = v
	}
	return out
}

// RPCErrorCodes returns a copy of the count-by-code map for jsonrpc errors.
func (s *ToolStats) RPCErrorCodes() map[int]int64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make(map[int]int64, len(s.rpcCodes))
	for k, v := range s.rpcCodes {
		out[k] = v
	}
	return out
}

// ToolSummary finalizes a ToolStats into a printable record.
type ToolSummary struct {
	Name      string                      `json:"name"`
	Latency   Summary                     `json:"latency"`
	OK        int64                       `json:"ok"`
	Errors    int64                       `json:"errors"`
	ErrorPct  float64                     `json:"error_pct"`
	Breakdown map[mcperrors.Category]int64 `json:"breakdown"`
	RPCCodes  map[int]int64                `json:"rpc_codes"`
}

// Summarize produces a finalized, serialization-ready snapshot.
func (s *ToolStats) Summarize() ToolSummary {
	latency := s.Reservoir.Snapshot()
	s.mu.Lock()
	ok := s.okCount
	breakdown := make(map[mcperrors.Category]int64, len(s.errCounts))
	for k, v := range s.errCounts {
		breakdown[k] = v
	}
	codes := make(map[int]int64, len(s.rpcCodes))
	for k, v := range s.rpcCodes {
		codes[k] = v
	}
	s.mu.Unlock()
	var totErr int64
	for _, v := range breakdown {
		totErr += v
	}
	total := ok + totErr
	var pct float64
	if total > 0 {
		pct = 100 * float64(totErr) / float64(total)
	}
	return ToolSummary{
		Name:      s.Name,
		Latency:   latency,
		OK:        ok,
		Errors:    totErr,
		ErrorPct:  pct,
		Breakdown: breakdown,
		RPCCodes:  codes,
	}
}

// Aggregator holds a global reservoir plus per-tool stats.
type Aggregator struct {
	mu            sync.RWMutex
	tools         map[string]*ToolStats
	global        *ToolStats
	reservoirSize int
}

// NewAggregator creates a new aggregator with the specified per-reservoir
// capacity.
func NewAggregator(reservoirSize int) *Aggregator {
	if reservoirSize <= 0 {
		reservoirSize = DefaultReservoirSize
	}
	return &Aggregator{
		tools:         map[string]*ToolStats{},
		global:        NewToolStats("__overall__", reservoirSize),
		reservoirSize: reservoirSize,
	}
}

// Record a single observation against both the per-tool and global reservoir.
func (a *Aggregator) Record(tool string, dur time.Duration, err error) {
	a.mu.RLock()
	ts, ok := a.tools[tool]
	a.mu.RUnlock()
	if !ok {
		a.mu.Lock()
		if ts, ok = a.tools[tool]; !ok {
			ts = NewToolStats(tool, a.reservoirSize)
			a.tools[tool] = ts
		}
		a.mu.Unlock()
	}
	ts.Record(dur, err)
	a.global.Record(dur, err)
}

// Global returns the global overall stats.
func (a *Aggregator) Global() *ToolStats { return a.global }

// Tool returns the stats for a single tool, or nil if unknown.
func (a *Aggregator) Tool(name string) *ToolStats {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.tools[name]
}

// ToolNames returns a sorted slice of tool names tracked.
func (a *Aggregator) ToolNames() []string {
	a.mu.RLock()
	defer a.mu.RUnlock()
	names := make([]string, 0, len(a.tools))
	for n := range a.tools {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}

// SummarizeAll produces a finalized view of all tools plus overall.
func (a *Aggregator) SummarizeAll() AggregateSummary {
	a.mu.RLock()
	names := make([]string, 0, len(a.tools))
	for n := range a.tools {
		names = append(names, n)
	}
	tools := make(map[string]ToolSummary, len(a.tools))
	for _, n := range names {
		tools[n] = a.tools[n].Summarize()
	}
	a.mu.RUnlock()
	sort.Strings(names)
	return AggregateSummary{
		Overall:  a.global.Summarize(),
		PerTool:  tools,
		ToolKeys: names,
	}
}

// AggregateSummary is the full finalized report fed to formatters and compare.
type AggregateSummary struct {
	Overall  ToolSummary             `json:"overall"`
	PerTool  map[string]ToolSummary  `json:"per_tool"`
	ToolKeys []string                `json:"tool_keys"`
}
