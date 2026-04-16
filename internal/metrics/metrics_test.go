package metrics

import (
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	mcperrors "github.com/JSLEEKR/mcpbench/internal/errors"
)

func TestReservoirEmptySnapshot(t *testing.T) {
	r := NewReservoir(10)
	s := r.Snapshot()
	if s.Count != 0 {
		t.Fatal(s.Count)
	}
	if s.SampleCount != 0 {
		t.Fatal(s.SampleCount)
	}
}

func TestReservoirInsertAndCount(t *testing.T) {
	r := NewReservoir(10)
	for i := 0; i < 5; i++ {
		r.Insert(time.Duration(i) * time.Millisecond)
	}
	if r.Count() != 5 {
		t.Fatal(r.Count())
	}
	if r.Len() != 5 {
		t.Fatal(r.Len())
	}
}

func TestReservoirOverflowBehaviour(t *testing.T) {
	r := NewReservoir(3)
	for i := 0; i < 10; i++ {
		r.Insert(time.Duration(i) * time.Millisecond)
	}
	if r.Count() != 10 {
		t.Fatalf("count = %d", r.Count())
	}
	if r.Len() != 3 {
		t.Fatalf("len = %d", r.Len())
	}
	s := r.Snapshot()
	if s.Count != 10 {
		t.Fatal("summary count wrong")
	}
	if s.SampleCount != 3 {
		t.Fatal("summary sample count wrong")
	}
}

func TestReservoirRejectsBadSize(t *testing.T) {
	r := NewReservoir(0)
	if r.size != 1 {
		t.Fatalf("size = %d", r.size)
	}
	r2 := NewReservoir(-5)
	if r2.size != 1 {
		t.Fatalf("size = %d", r2.size)
	}
}

func TestSummaryPercentilesBasic(t *testing.T) {
	r := NewReservoir(100)
	for i := 1; i <= 100; i++ {
		r.Insert(time.Duration(i) * time.Millisecond)
	}
	s := r.Snapshot()
	if s.Min != time.Millisecond {
		t.Fatalf("min = %v", s.Min)
	}
	if s.Max != 100*time.Millisecond {
		t.Fatalf("max = %v", s.Max)
	}
	if s.P50 != 50*time.Millisecond {
		t.Fatalf("p50 = %v", s.P50)
	}
	if s.P95 != 95*time.Millisecond {
		t.Fatalf("p95 = %v", s.P95)
	}
	if s.P99 != 99*time.Millisecond {
		t.Fatalf("p99 = %v", s.P99)
	}
}

func TestSummaryMeanAndStdDev(t *testing.T) {
	r := NewReservoir(10)
	for _, v := range []int{10, 20, 30, 40, 50} {
		r.Insert(time.Duration(v) * time.Millisecond)
	}
	s := r.Snapshot()
	if s.Mean != 30*time.Millisecond {
		t.Fatalf("mean = %v", s.Mean)
	}
	if s.StdDev <= 0 {
		t.Fatalf("stddev = %v", s.StdDev)
	}
}

func TestSummarySingleSample(t *testing.T) {
	r := NewReservoir(5)
	r.Insert(42 * time.Millisecond)
	s := r.Snapshot()
	if s.Min != s.Max || s.Min != 42*time.Millisecond {
		t.Fatalf("min/max = %v/%v", s.Min, s.Max)
	}
	if s.P50 != 42*time.Millisecond {
		t.Fatal("p50 off")
	}
	if s.StdDev != 0 {
		t.Fatalf("stddev = %v", s.StdDev)
	}
}

func TestPercentileBoundaries(t *testing.T) {
	sorted := []time.Duration{1, 2, 3, 4, 5}
	if got := percentile(sorted, 0); got != 1 {
		t.Fatalf("p0 = %v", got)
	}
	if got := percentile(sorted, 1); got != 5 {
		t.Fatalf("p100 = %v", got)
	}
	if got := percentile(nil, 0.5); got != 0 {
		t.Fatalf("empty = %v", got)
	}
	// Negative p treated as 0
	if got := percentile(sorted, -0.1); got != 1 {
		t.Fatalf("neg p = %v", got)
	}
	// >1 treated as max
	if got := percentile(sorted, 1.1); got != 5 {
		t.Fatalf(">1 p = %v", got)
	}
}

func TestReservoirConcurrentInsert(t *testing.T) {
	r := NewReservoir(1000)
	const n = 500
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			r.Insert(time.Duration(i) * time.Microsecond)
		}(i)
	}
	wg.Wait()
	if r.Count() != n {
		t.Fatalf("count = %d", r.Count())
	}
}

func TestToolStatsRecord(t *testing.T) {
	ts := NewToolStats("read", 100)
	ts.Record(10*time.Millisecond, nil)
	ts.Record(20*time.Millisecond, &mcperrors.TimeoutError{})
	ts.Record(30*time.Millisecond, mcperrors.NewJSONRPCError(-32601, "nope"))
	ts.Record(15*time.Millisecond, &mcperrors.TransportError{Inner: errors.New("eof")})
	if ts.OKCount() != 1 {
		t.Fatalf("ok = %d", ts.OKCount())
	}
	if ts.ErrorCount() != 3 {
		t.Fatalf("errs = %d", ts.ErrorCount())
	}
	br := ts.ErrorBreakdown()
	if br[mcperrors.CategoryTimeout] != 1 {
		t.Fatalf("timeout = %d", br[mcperrors.CategoryTimeout])
	}
	if br[mcperrors.CategoryJSONRPC] != 1 {
		t.Fatalf("jsonrpc = %d", br[mcperrors.CategoryJSONRPC])
	}
	if br[mcperrors.CategoryTransport] != 1 {
		t.Fatalf("transport = %d", br[mcperrors.CategoryTransport])
	}
	codes := ts.RPCErrorCodes()
	if codes[-32601] != 1 {
		t.Fatalf("rpc code = %d", codes[-32601])
	}
}

func TestToolStatsSummarize(t *testing.T) {
	ts := NewToolStats("search", 100)
	for i := 0; i < 100; i++ {
		var err error
		if i%10 == 0 {
			err = &mcperrors.TimeoutError{}
		}
		ts.Record(time.Duration(i)*time.Millisecond, err)
	}
	sum := ts.Summarize()
	if sum.Name != "search" {
		t.Fatalf("name %s", sum.Name)
	}
	if sum.OK != 90 {
		t.Fatalf("ok = %d", sum.OK)
	}
	if sum.Errors != 10 {
		t.Fatalf("errs = %d", sum.Errors)
	}
	if sum.ErrorPct < 9.9 || sum.ErrorPct > 10.1 {
		t.Fatalf("pct = %f", sum.ErrorPct)
	}
	if sum.Latency.P50 == 0 {
		t.Fatal("p50 zero")
	}
}

func TestToolStatsSummarizeNoRequests(t *testing.T) {
	ts := NewToolStats("empty", 10)
	sum := ts.Summarize()
	if sum.OK != 0 {
		t.Fatal("ok != 0")
	}
	if sum.Errors != 0 {
		t.Fatal("errs != 0")
	}
	if sum.ErrorPct != 0 {
		t.Fatal("pct != 0")
	}
}

func TestAggregatorRecord(t *testing.T) {
	a := NewAggregator(100)
	a.Record("read", 5*time.Millisecond, nil)
	a.Record("read", 10*time.Millisecond, nil)
	a.Record("search", 20*time.Millisecond, &mcperrors.TimeoutError{})
	names := a.ToolNames()
	if len(names) != 2 {
		t.Fatalf("names = %v", names)
	}
	if names[0] != "read" || names[1] != "search" {
		t.Fatalf("sorted wrong: %v", names)
	}
	if a.Tool("read").OKCount() != 2 {
		t.Fatal("read ok count")
	}
	if a.Tool("search").ErrorCount() != 1 {
		t.Fatal("search err count")
	}
	if a.Global().ErrorCount() != 1 {
		t.Fatal("global err count")
	}
	if a.Tool("unknown") != nil {
		t.Fatal("unknown should be nil")
	}
}

func TestAggregatorSummarizeAll(t *testing.T) {
	a := NewAggregator(100)
	for i := 0; i < 50; i++ {
		a.Record("read", time.Duration(i)*time.Millisecond, nil)
	}
	for i := 0; i < 20; i++ {
		var err error
		if i%5 == 0 {
			err = mcperrors.NewJSONRPCError(-1, "x")
		}
		a.Record("write", time.Duration(i)*time.Millisecond, err)
	}
	sum := a.SummarizeAll()
	if len(sum.ToolKeys) != 2 {
		t.Fatal("tool keys")
	}
	if sum.PerTool["read"].OK != 50 {
		t.Fatal("read ok")
	}
	if sum.PerTool["write"].Errors != 4 {
		t.Fatalf("write errs = %d", sum.PerTool["write"].Errors)
	}
	if sum.Overall.OK+sum.Overall.Errors != 70 {
		t.Fatalf("overall total = %d", sum.Overall.OK+sum.Overall.Errors)
	}
}

func TestAggregatorDefaultReservoirSize(t *testing.T) {
	a := NewAggregator(0)
	if a.reservoirSize != DefaultReservoirSize {
		t.Fatalf("size = %d", a.reservoirSize)
	}
}

func TestAggregatorConcurrentRecord(t *testing.T) {
	a := NewAggregator(10_000)
	const workers = 16
	const per = 500
	var wg sync.WaitGroup
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			for i := 0; i < per; i++ {
				a.Record("t", time.Duration(i)*time.Microsecond, nil)
			}
		}(w)
	}
	wg.Wait()
	if got := a.Tool("t").OKCount(); got != workers*per {
		t.Fatalf("got = %d", got)
	}
	if got := a.Global().OKCount(); got != workers*per {
		t.Fatalf("global = %d", got)
	}
}

func TestStressPercentileAtLarge(t *testing.T) {
	r := NewReservoir(10_000)
	for i := 1; i <= 10_000; i++ {
		r.Insert(time.Duration(i) * time.Microsecond)
	}
	s := r.Snapshot()
	if s.P50 != 5000*time.Microsecond {
		t.Fatalf("p50 = %v", s.P50)
	}
	if s.P95 != 9500*time.Microsecond {
		t.Fatalf("p95 = %v", s.P95)
	}
	if s.P99 != 9900*time.Microsecond {
		t.Fatalf("p99 = %v", s.P99)
	}
}

func TestReservoirSnapshotDoesNotMutate(t *testing.T) {
	r := NewReservoir(5)
	for i := 1; i <= 5; i++ {
		r.Insert(time.Duration(i) * time.Millisecond)
	}
	_ = r.Snapshot()
	if r.Len() != 5 {
		t.Fatalf("len = %d", r.Len())
	}
	if r.Count() != 5 {
		t.Fatalf("count = %d", r.Count())
	}
	// Samples should still be in insertion order (not sorted in place).
	if r.samples[0] != 1*time.Millisecond {
		t.Fatalf("mutated: %v", r.samples)
	}
}

func TestToolStatsNilError(t *testing.T) {
	ts := NewToolStats("x", 10)
	ts.Record(1*time.Millisecond, nil)
	if ts.OKCount() != 1 {
		t.Fatal("ok")
	}
	if len(ts.ErrorBreakdown()) != 0 {
		t.Fatal("non-empty breakdown")
	}
}

// TestToolStatsRecordUnwrapsWrappedJSONRPC is a regression test for a bug
// where ToolStats.Record used a direct type assertion to extract the RPC
// code, which silently failed for wrapped errors (e.g. a transport returning
// `fmt.Errorf("tool %s: %w", name, NewJSONRPCError(...))`). Classify() uses
// errors.As, so such wrapped errors were counted under jsonrpc in the error
// breakdown — but the per-code histogram (`rpc_codes`) was empty for them.
// After the fix, both Classify and the per-code accounting agree.
func TestToolStatsRecordUnwrapsWrappedJSONRPC(t *testing.T) {
	ts := NewToolStats("wrap", 10)
	inner := mcperrors.NewJSONRPCError(-32000, "server error")
	wrapped := fmt.Errorf("calling tool foo: %w", inner)
	ts.Record(5*time.Millisecond, wrapped)

	if got := ts.ErrorBreakdown()[mcperrors.CategoryJSONRPC]; got != 1 {
		t.Fatalf("breakdown jsonrpc = %d, want 1 (Classify already handles %%w)", got)
	}
	codes := ts.RPCErrorCodes()
	if codes[-32000] != 1 {
		t.Fatalf("rpc_codes[-32000] = %d, want 1 — wrapped JSON-RPC errors were being silently dropped", codes[-32000])
	}
}

func TestSummaryCountVsSampleCount(t *testing.T) {
	r := NewReservoir(5)
	for i := 0; i < 20; i++ {
		r.Insert(time.Millisecond)
	}
	s := r.Snapshot()
	if s.Count != 20 {
		t.Fatalf("count %d", s.Count)
	}
	if s.SampleCount != 5 {
		t.Fatalf("sample %d", s.SampleCount)
	}
}
