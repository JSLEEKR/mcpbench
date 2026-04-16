package orchestrator

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	mcperrors "github.com/JSLEEKR/mcpbench/internal/errors"
	"github.com/JSLEEKR/mcpbench/internal/metrics"
	"github.com/JSLEEKR/mcpbench/internal/scenario"
	"github.com/JSLEEKR/mcpbench/internal/transport"
)

// stubTransport is a hand-rolled Transport used to avoid spawning subprocesses
// during orchestrator unit tests.
type stubTransport struct {
	handler func(method string, params any) ([]byte, error)
	calls   atomic.Int64
}

func (s *stubTransport) Call(ctx context.Context, method string, params any) ([]byte, error) {
	s.calls.Add(1)
	if s.handler == nil {
		return []byte(`{"jsonrpc":"2.0","id":1,"result":{"ok":true}}`), nil
	}
	return s.handler(method, params)
}
func (s *stubTransport) Close() error { return nil }

var _ transport.Transport = (*stubTransport)(nil)

func makeScenario(w scenario.Workload, tools []scenario.ToolCall) *scenario.Scenario {
	return &scenario.Scenario{
		Name:      "test",
		Transport: scenario.TransportConfig{Type: "stdio", Cmd: "x"},
		Workload:  w,
		Tools:     tools,
	}
}

func TestOrchestratorRequestsBudget(t *testing.T) {
	s := makeScenario(
		scenario.Workload{Requests: 50, Concurrency: 4},
		[]scenario.ToolCall{{Name: "ping", Weight: 1}},
	)
	agg := metrics.NewAggregator(200)
	tr := &stubTransport{}
	_, err := Run(context.Background(), Config{Scenario: s, Transport: tr, Aggregator: agg, Seed: 1})
	if err != nil {
		t.Fatal(err)
	}
	if got := agg.Tool("ping").OKCount(); got != 50 {
		t.Fatalf("ok = %d", got)
	}
	if got := tr.calls.Load(); got != 50 {
		t.Fatalf("calls = %d", got)
	}
}

func TestOrchestratorDurationBudget(t *testing.T) {
	s := makeScenario(
		scenario.Workload{Duration: 200 * time.Millisecond, Concurrency: 4},
		[]scenario.ToolCall{{Name: "ping", Weight: 1}},
	)
	agg := metrics.NewAggregator(10000)
	tr := &stubTransport{}
	start := time.Now()
	_, err := Run(context.Background(), Config{Scenario: s, Transport: tr, Aggregator: agg, Seed: 1})
	elapsed := time.Since(start)
	if err != nil {
		t.Fatal(err)
	}
	if elapsed < 200*time.Millisecond {
		t.Fatalf("too short: %v", elapsed)
	}
	if elapsed > 500*time.Millisecond {
		t.Fatalf("too long: %v", elapsed)
	}
	if agg.Tool("ping").OKCount() < 10 {
		t.Fatalf("too few calls: %d", agg.Tool("ping").OKCount())
	}
}

func TestOrchestratorRateLimited(t *testing.T) {
	s := makeScenario(
		scenario.Workload{Rate: 50, Duration: 300 * time.Millisecond},
		[]scenario.ToolCall{{Name: "ping", Weight: 1}},
	)
	agg := metrics.NewAggregator(10000)
	tr := &stubTransport{}
	_, err := Run(context.Background(), Config{Scenario: s, Transport: tr, Aggregator: agg, Seed: 1})
	if err != nil {
		t.Fatal(err)
	}
	// 50 qps * 0.3s = 15. Allow 7..25 for scheduler jitter.
	got := agg.Tool("ping").OKCount()
	if got < 7 || got > 25 {
		t.Fatalf("count = %d", got)
	}
}

func TestOrchestratorHonorsCancellation(t *testing.T) {
	s := makeScenario(
		scenario.Workload{Requests: 10_000, Concurrency: 4},
		[]scenario.ToolCall{{Name: "ping", Weight: 1}},
	)
	tr := &stubTransport{handler: func(method string, params any) ([]byte, error) {
		time.Sleep(10 * time.Millisecond)
		return []byte(`{"jsonrpc":"2.0","id":1,"result":{}}`), nil
	}}
	agg := metrics.NewAggregator(10000)
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, _ = Run(ctx, Config{Scenario: s, Transport: tr, Aggregator: agg, Seed: 1})
	if elapsed := time.Since(start); elapsed > 500*time.Millisecond {
		t.Fatalf("did not honor cancel, elapsed = %v", elapsed)
	}
}

func TestOrchestratorTransportFailure(t *testing.T) {
	s := makeScenario(
		scenario.Workload{Requests: 20, Concurrency: 2},
		[]scenario.ToolCall{{Name: "ping", Weight: 1}},
	)
	tr := &stubTransport{handler: func(method string, params any) ([]byte, error) {
		return nil, &mcperrors.TransportError{Inner: errors.New("boom")}
	}}
	agg := metrics.NewAggregator(200)
	_, err := Run(context.Background(), Config{Scenario: s, Transport: tr, Aggregator: agg, Seed: 1})
	if err != nil {
		t.Fatal(err)
	}
	if agg.Tool("ping").ErrorCount() != 20 {
		t.Fatalf("errs = %d", agg.Tool("ping").ErrorCount())
	}
}

func TestOrchestratorJSONRPCErrorInBody(t *testing.T) {
	s := makeScenario(
		scenario.Workload{Requests: 10, Concurrency: 2},
		[]scenario.ToolCall{{Name: "ping", Weight: 1}},
	)
	tr := &stubTransport{handler: func(method string, params any) ([]byte, error) {
		return []byte(`{"jsonrpc":"2.0","id":1,"error":{"code":-32601,"message":"nope"}}`), nil
	}}
	agg := metrics.NewAggregator(200)
	_, err := Run(context.Background(), Config{Scenario: s, Transport: tr, Aggregator: agg, Seed: 1})
	if err != nil {
		t.Fatal(err)
	}
	if agg.Tool("ping").OKCount() != 0 {
		t.Fatalf("ok = %d", agg.Tool("ping").OKCount())
	}
	if agg.Tool("ping").ErrorCount() != 10 {
		t.Fatalf("errs = %d", agg.Tool("ping").ErrorCount())
	}
}

func TestOrchestratorMixedWeights(t *testing.T) {
	s := makeScenario(
		scenario.Workload{Requests: 200, Concurrency: 4},
		[]scenario.ToolCall{{Name: "a", Weight: 1}, {Name: "b", Weight: 3}},
	)
	agg := metrics.NewAggregator(500)
	tr := &stubTransport{}
	_, err := Run(context.Background(), Config{Scenario: s, Transport: tr, Aggregator: agg, Seed: 1})
	if err != nil {
		t.Fatal(err)
	}
	a := agg.Tool("a").OKCount()
	b := agg.Tool("b").OKCount()
	if a+b != 200 {
		t.Fatalf("total = %d", a+b)
	}
	if b < 2*a {
		t.Fatalf("b %d should be ~3x a %d", b, a)
	}
}

func TestOrchestratorTemplating(t *testing.T) {
	var seen []string
	tr := &stubTransport{handler: func(method string, params any) ([]byte, error) {
		raw, _ := json.Marshal(params)
		seen = append(seen, string(raw))
		return []byte(`{"jsonrpc":"2.0","id":1,"result":{}}`), nil
	}}
	s := makeScenario(
		scenario.Workload{Requests: 5, Concurrency: 1},
		[]scenario.ToolCall{{Name: "search", Weight: 1, Args: map[string]any{"q": "page-{{.Iter}}"}}},
	)
	agg := metrics.NewAggregator(10)
	if _, err := Run(context.Background(), Config{Scenario: s, Transport: tr, Aggregator: agg, Seed: 1}); err != nil {
		t.Fatal(err)
	}
	for _, s := range seen {
		if !containsAny(s, []string{"page-1", "page-2", "page-3", "page-4", "page-5"}) {
			t.Fatalf("bad templating: %s", s)
		}
	}
}

func TestOrchestratorTemplateErrorCategory(t *testing.T) {
	tr := &stubTransport{}
	s := makeScenario(
		scenario.Workload{Requests: 3, Concurrency: 1},
		[]scenario.ToolCall{{Name: "x", Weight: 1, Args: map[string]any{"bad": "{{.Env.NOPE}}"}}},
	)
	agg := metrics.NewAggregator(10)
	if _, err := Run(context.Background(), Config{Scenario: s, Transport: tr, Aggregator: agg, Seed: 1, EnvSnapshot: map[string]string{}}); err != nil {
		t.Fatal(err)
	}
	br := agg.Tool("x").ErrorBreakdown()
	if br[mcperrors.CategoryTemplate] != 3 {
		t.Fatalf("template errs = %d", br[mcperrors.CategoryTemplate])
	}
}

func TestOrchestratorWarmupExcluded(t *testing.T) {
	tr := &stubTransport{}
	s := makeScenario(
		scenario.Workload{Duration: 100 * time.Millisecond, Warmup: 100 * time.Millisecond, Concurrency: 2},
		[]scenario.ToolCall{{Name: "x", Weight: 1}},
	)
	agg := metrics.NewAggregator(1000)
	_, err := Run(context.Background(), Config{Scenario: s, Transport: tr, Aggregator: agg, Seed: 1})
	if err != nil {
		t.Fatal(err)
	}
	// Warmup runs dispatch without recording to agg, so agg counts should be
	// less than total tr.calls.
	if agg.Tool("x").OKCount() == 0 {
		t.Fatal("measurement phase recorded nothing")
	}
	if int64(agg.Tool("x").OKCount()) >= tr.calls.Load() {
		t.Fatalf("agg=%d vs total=%d", agg.Tool("x").OKCount(), tr.calls.Load())
	}
}

func TestOrchestratorRequiresScenario(t *testing.T) {
	_, err := Run(context.Background(), Config{})
	if err == nil {
		t.Fatal("expected scenario error")
	}
}

func TestOrchestratorRequiresTransport(t *testing.T) {
	s := makeScenario(scenario.Workload{Requests: 1, Concurrency: 1}, []scenario.ToolCall{{Name: "a"}})
	_, err := Run(context.Background(), Config{Scenario: s})
	if err == nil {
		t.Fatal("expected transport error")
	}
}

func TestOrchestratorDefaultsAggregator(t *testing.T) {
	s := makeScenario(scenario.Workload{Requests: 2, Concurrency: 1}, []scenario.ToolCall{{Name: "a", Weight: 1}})
	tr := &stubTransport{}
	if _, err := Run(context.Background(), Config{Scenario: s, Transport: tr, Seed: 1, ReservoirSize: 100}); err != nil {
		t.Fatal(err)
	}
}

func TestOrchestratorHTTPTransportSmoke(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"jsonrpc":"2.0","id":%v,"result":{}}`, body["id"])
	}))
	defer srv.Close()
	tr, _ := transport.NewHTTP(transport.HTTPConfig{URL: srv.URL})
	defer tr.Close()
	s := makeScenario(scenario.Workload{Requests: 10, Concurrency: 2}, []scenario.ToolCall{{Name: "a", Weight: 1}})
	agg := metrics.NewAggregator(100)
	if _, err := Run(context.Background(), Config{Scenario: s, Transport: tr, Aggregator: agg, Seed: 1}); err != nil {
		t.Fatal(err)
	}
	if hits.Load() != 10 {
		t.Fatalf("hits = %d", hits.Load())
	}
}

func TestOrchestratorEffectiveWorkersRespectsConcurrency(t *testing.T) {
	got := effectiveWorkers(scenario.Workload{Concurrency: 7, Rate: 1000})
	if got != 7 {
		t.Fatal(got)
	}
}

func TestOrchestratorEffectiveWorkersRateFallback(t *testing.T) {
	got := effectiveWorkers(scenario.Workload{Rate: 100})
	if got <= 0 {
		t.Fatal(got)
	}
}

func TestOrchestratorEffectiveWorkersDefault(t *testing.T) {
	got := effectiveWorkers(scenario.Workload{})
	if got != 1 {
		t.Fatal(got)
	}
}

// TestOrchestratorWarmupSurvivesCancel is a regression test: if the caller
// cancels the parent context during the warmup phase, runPhases used to
// return context.Canceled as a fatal error (it only filtered
// DeadlineExceeded). The measurement phase already filtered both; warmup
// should too so that Ctrl-C during warmup drops us into the normal drain
// path instead of bubbling up as an error.
func TestOrchestratorWarmupSurvivesCancel(t *testing.T) {
	tr := &stubTransport{handler: func(method string, params any) ([]byte, error) {
		time.Sleep(10 * time.Millisecond)
		return []byte(`{"jsonrpc":"2.0","id":1,"result":{}}`), nil
	}}
	s := makeScenario(
		scenario.Workload{
			Warmup:      500 * time.Millisecond,
			Duration:    500 * time.Millisecond,
			Concurrency: 2,
		},
		[]scenario.ToolCall{{Name: "x", Weight: 1}},
	)
	agg := metrics.NewAggregator(100)
	ctx, cancel := context.WithCancel(context.Background())
	// Cancel mid-warmup.
	go func() {
		time.Sleep(50 * time.Millisecond)
		cancel()
	}()
	_, err := Run(ctx, Config{Scenario: s, Transport: tr, Aggregator: agg, Seed: 1})
	if err != nil {
		t.Fatalf("warmup cancel should be absorbed, got: %v", err)
	}
}

// TestSeedProducesDeterministicRand is a regression test: the orchestrator
// used to pull .Rand from the unseeded global math/rand, so two runs with the
// same --seed emitted different .Rand values in the args template. Callers
// who rely on reproducible load (e.g. golden-output CI gates) saw flakes. Fix
// threads a seeded rand source through every tctx.Rand read.
func TestSeedProducesDeterministicRand(t *testing.T) {
	run := func(seed int64) []string {
		var mu sync.Mutex
		var seen []string
		tr := &stubTransport{handler: func(method string, params any) ([]byte, error) {
			raw, _ := json.Marshal(params)
			mu.Lock()
			seen = append(seen, string(raw))
			mu.Unlock()
			return []byte(`{"jsonrpc":"2.0","id":1,"result":{}}`), nil
		}}
		s := makeScenario(
			scenario.Workload{Requests: 5, Concurrency: 1},
			[]scenario.ToolCall{{Name: "x", Weight: 1, Args: map[string]any{"r": "{{.Rand}}"}}},
		)
		agg := metrics.NewAggregator(10)
		if _, err := Run(context.Background(), Config{Scenario: s, Transport: tr, Aggregator: agg, Seed: seed}); err != nil {
			t.Fatal(err)
		}
		return seen
	}
	a := run(42)
	b := run(42)
	if len(a) != len(b) {
		t.Fatalf("len diverge: %d vs %d", len(a), len(b))
	}
	for i := range a {
		if a[i] != b[i] {
			t.Fatalf("--seed did not make .Rand deterministic at iteration %d:\n  a=%s\n  b=%s", i, a[i], b[i])
		}
	}
}

func containsAny(s string, subs []string) bool {
	for _, sub := range subs {
		if len(s) > 0 && len(sub) > 0 && (len(s) >= len(sub)) {
			if indexOf(s, sub) >= 0 {
				return true
			}
		}
	}
	return false
}

func indexOf(s, sub string) int {
	return stringIndex(s, sub)
}

func stringIndex(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}
