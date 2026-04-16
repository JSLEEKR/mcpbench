// Package orchestrator drives the load generation: it combines a Transport
// with a Scenario picker and feeds observations into a metrics.Aggregator.
package orchestrator

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math/rand"
	"os"
	"runtime"
	"sync"
	"sync/atomic"
	"time"

	mcperrors "github.com/JSLEEKR/mcpbench/internal/errors"
	"github.com/JSLEEKR/mcpbench/internal/jsonrpc"
	"github.com/JSLEEKR/mcpbench/internal/metrics"
	"github.com/JSLEEKR/mcpbench/internal/scenario"
	"github.com/JSLEEKR/mcpbench/internal/transport"
)

// Config bundles the orchestrator's runtime parameters.
type Config struct {
	Scenario      *scenario.Scenario
	Transport     transport.Transport
	Aggregator    *metrics.Aggregator
	Seed          int64
	ReservoirSize int
	// EnvSnapshot is passed to templating as .Env. Nil means use os.Environ.
	EnvSnapshot map[string]string
}

// Run executes the scenario's workload. It blocks until duration elapses,
// requests are exhausted, ctx is cancelled, or an unrecoverable error occurs.
func Run(ctx context.Context, cfg Config) (time.Duration, error) {
	if cfg.Scenario == nil {
		return 0, fmt.Errorf("orchestrator: scenario required")
	}
	if cfg.Transport == nil {
		return 0, fmt.Errorf("orchestrator: transport required")
	}
	if cfg.Aggregator == nil {
		cfg.Aggregator = metrics.NewAggregator(cfg.ReservoirSize)
	}
	if cfg.EnvSnapshot == nil {
		cfg.EnvSnapshot = snapshotEnv()
	}

	w := cfg.Scenario.Workload
	picker := scenario.NewPicker(cfg.Scenario, cfg.Seed)
	// Picker is not safe for concurrent use, so guard with a mutex. The
	// critical section is tiny.
	var pickerMu sync.Mutex
	nextTool := func() *scenario.ToolCall {
		pickerMu.Lock()
		defer pickerMu.Unlock()
		return picker.Next()
	}

	workers := effectiveWorkers(w)
	measureStart, err := runPhases(ctx, cfg, nextTool, workers)
	return measureStart, err
}

func effectiveWorkers(w scenario.Workload) int {
	switch {
	case w.Concurrency > 0:
		return w.Concurrency
	case w.Rate > 0:
		// Ensure enough headroom to absorb target qps. At 1ms latency we need
		// qps workers; we cap to 4x runtime CPUs to avoid explosion.
		wanted := int(w.Rate)
		if wanted < 1 {
			wanted = 1
		}
		cap := runtime.NumCPU() * 4
		if wanted > cap {
			wanted = cap
		}
		return wanted
	default:
		return 1
	}
}

type job struct {
	iter int64
}

func runPhases(ctx context.Context, cfg Config, nextTool func() *scenario.ToolCall, workers int) (time.Duration, error) {
	// Optionally run warmup: identical dispatch, but results recorded in a
	// throwaway aggregator.
	var warmupAgg *metrics.Aggregator
	if cfg.Scenario.Workload.Warmup > 0 {
		warmupAgg = metrics.NewAggregator(1024)
		warmupCtx, cancel := context.WithTimeout(ctx, cfg.Scenario.Workload.Warmup)
		if err := runPhase(warmupCtx, cfg, nextTool, workers, warmupAgg, cfg.Scenario.Workload.Warmup, 0); err != nil &&
			!errors.Is(err, context.DeadlineExceeded) &&
			!errors.Is(err, context.Canceled) {
			cancel()
			return 0, err
		}
		cancel()
	}

	// Measurement phase.
	startMeasure := time.Now()
	var measureErr error
	durationBudget := cfg.Scenario.Workload.Duration
	requestsBudget := cfg.Scenario.Workload.Requests
	if durationBudget > 0 {
		measureCtx, cancel := context.WithTimeout(ctx, durationBudget)
		measureErr = runPhase(measureCtx, cfg, nextTool, workers, cfg.Aggregator, durationBudget, 0)
		cancel()
	} else {
		measureErr = runPhase(ctx, cfg, nextTool, workers, cfg.Aggregator, 0, requestsBudget)
	}
	elapsed := time.Since(startMeasure)
	if measureErr != nil && !errors.Is(measureErr, context.DeadlineExceeded) && !errors.Is(measureErr, context.Canceled) {
		return elapsed, measureErr
	}

	// Cooldown phase: just sleep so in-flight requests on other transports
	// can drain (for stdio transport this is a no-op because Call blocks on
	// its response).
	if cfg.Scenario.Workload.Cooldown > 0 {
		cdCtx, cancel := context.WithTimeout(ctx, cfg.Scenario.Workload.Cooldown)
		<-cdCtx.Done()
		cancel()
	}
	_ = warmupAgg // kept to silence unused warnings; we intentionally discard
	return elapsed, nil
}

// runPhase fires load under either duration OR requests budget. Exactly one
// must be non-zero.
func runPhase(ctx context.Context, cfg Config, nextTool func() *scenario.ToolCall, workers int, agg *metrics.Aggregator, duration time.Duration, requests int) error {
	jobs := make(chan job, workers*2)
	var wg sync.WaitGroup

	// Workers.
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go worker(ctx, cfg, nextTool, jobs, agg, &wg)
	}

	// Generator: produces jobs under the chosen rate / count / duration.
	genErr := generate(ctx, cfg.Scenario.Workload, jobs, duration, requests)
	close(jobs)
	wg.Wait()
	return genErr
}

func worker(ctx context.Context, cfg Config, nextTool func() *scenario.ToolCall, jobs <-chan job, agg *metrics.Aggregator, wg *sync.WaitGroup) {
	defer wg.Done()
	for {
		select {
		case <-ctx.Done():
			return
		case j, ok := <-jobs:
			if !ok {
				return
			}
			executeOne(ctx, cfg, nextTool, j, agg)
		}
	}
}

func executeOne(ctx context.Context, cfg Config, nextTool func() *scenario.ToolCall, j job, agg *metrics.Aggregator) {
	tool := nextTool()
	timeout := cfg.Scenario.CallTimeout(tool)
	callCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	tctx := scenario.TemplateContext{
		Iter: j.iter,
		Env:  cfg.EnvSnapshot,
		Rand: rand.Float64(),
	}
	args, err := scenario.RenderArgs(tool.Args, tctx)
	if err != nil {
		agg.Record(tool.Name, 0, err)
		return
	}
	params := jsonrpc.ToolCallParams{Name: tool.Name, Arguments: args}

	start := time.Now()
	raw, err := cfg.Transport.Call(callCtx, "tools/call", params)
	elapsed := time.Since(start)

	if err != nil {
		agg.Record(tool.Name, elapsed, err)
		return
	}
	// Even on 2xx, the response body may carry a JSON-RPC error.
	if rpcErr := extractRPCError(raw); rpcErr != nil {
		agg.Record(tool.Name, elapsed, rpcErr)
		return
	}
	agg.Record(tool.Name, elapsed, nil)

	if w := cfg.Scenario.Workload.ThinkTime; w > 0 {
		select {
		case <-ctx.Done():
		case <-time.After(w):
		}
	}
}

func extractRPCError(raw []byte) error {
	if len(raw) == 0 {
		return nil
	}
	var resp struct {
		Error *struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(raw, &resp); err != nil {
		// Not fatal; skip.
		return nil
	}
	if resp.Error != nil {
		return mcperrors.NewJSONRPCError(resp.Error.Code, resp.Error.Message)
	}
	return nil
}

func generate(ctx context.Context, w scenario.Workload, jobs chan<- job, duration time.Duration, requests int) error {
	var iter atomic.Int64

	deadline := time.Time{}
	if duration > 0 {
		deadline = time.Now().Add(duration)
	}
	rate := w.Rate
	var ticker *time.Ticker
	if rate > 0 {
		interval := time.Duration(float64(time.Second) / rate)
		if interval <= 0 {
			interval = time.Microsecond
		}
		ticker = time.NewTicker(interval)
		defer ticker.Stop()
	}

	emit := func() bool {
		i := iter.Add(1)
		select {
		case jobs <- job{iter: i}:
			return true
		case <-ctx.Done():
			return false
		}
	}

	for {
		if requests > 0 && int(iter.Load()) >= requests {
			return nil
		}
		if !deadline.IsZero() && time.Now().After(deadline) {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		if ticker != nil {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-ticker.C:
				if !emit() {
					return ctx.Err()
				}
			}
		} else {
			// Closed-model: push as fast as workers can accept.
			if !emit() {
				return ctx.Err()
			}
		}
	}
}

func snapshotEnv() map[string]string {
	env := os.Environ()
	out := make(map[string]string, len(env))
	for _, kv := range env {
		for i := 0; i < len(kv); i++ {
			if kv[i] == '=' {
				out[kv[:i]] = kv[i+1:]
				break
			}
		}
	}
	return out
}
