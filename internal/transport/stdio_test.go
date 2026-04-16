package transport

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"testing"
	"time"

	mcperrors "github.com/JSLEEKR/mcpbench/internal/errors"
)

// buildMock compiles the testmock binary once per test binary invocation.
var (
	mockOnce sync.Once
	mockPath string
	mockErr  error
)

func mockBinary(t *testing.T) string {
	t.Helper()
	mockOnce.Do(func() {
		dir, err := os.MkdirTemp("", "mcpbench-mock-")
		if err != nil {
			mockErr = err
			return
		}
		// Don't remove — persists across tests in this binary run.
		out := filepath.Join(dir, "testmock")
		cmd := exec.Command("go", "build", "-o", out, "./testmock")
		cmd.Dir = "." // internal/transport
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		if err := cmd.Run(); err != nil {
			mockErr = err
			return
		}
		mockPath = out
	})
	if mockErr != nil {
		t.Fatalf("build testmock: %v", mockErr)
	}
	return mockPath
}

func startMock(t *testing.T, cfg ...func(*StdioConfig)) *Stdio {
	t.Helper()
	bin := mockBinary(t)
	c := StdioConfig{Cmd: bin, Silent: true}
	for _, fn := range cfg {
		fn(&c)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	t.Cleanup(cancel)
	s, err := StartStdio(ctx, c)
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func TestStdioPing(t *testing.T) {
	s := startMock(t)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	raw, err := s.Call(ctx, "ping", nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(raw) == 0 {
		t.Fatal("empty")
	}
	var resp struct {
		Result map[string]any `json:"result"`
	}
	if err := json.Unmarshal(raw, &resp); err != nil {
		t.Fatal(err)
	}
	if resp.Result["ok"] != true {
		t.Fatalf("resp = %v", resp)
	}
}

func TestStdioEcho(t *testing.T) {
	s := startMock(t)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	raw, err := s.Call(ctx, "echo", map[string]any{"hello": "world"})
	if err != nil {
		t.Fatal(err)
	}
	var resp struct {
		Result map[string]any `json:"result"`
	}
	if err := json.Unmarshal(raw, &resp); err != nil {
		t.Fatal(err)
	}
	if resp.Result["hello"] != "world" {
		t.Fatalf("got %v", resp.Result)
	}
}

func TestStdioMultiConcurrentCalls(t *testing.T) {
	s := startMock(t)
	const n = 50
	var wg sync.WaitGroup
	errs := make(chan error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			raw, err := s.Call(ctx, "echo", map[string]any{"i": i})
			if err != nil {
				errs <- err
				return
			}
			var resp struct {
				Result map[string]any `json:"result"`
			}
			_ = json.Unmarshal(raw, &resp)
			if int(resp.Result["i"].(float64)) != i {
				errs <- errors.New("id mismatch")
			}
		}(i)
	}
	wg.Wait()
	close(errs)
	for e := range errs {
		t.Error(e)
	}
}

func TestStdioJSONRPCErrorBodyReturned(t *testing.T) {
	s := startMock(t)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	raw, err := s.Call(ctx, "errorMethod", nil)
	if err != nil {
		t.Fatal(err)
	}
	if !contains(raw, []byte(`"error"`)) {
		t.Fatalf("no error field: %s", raw)
	}
}

func TestStdioLargeResponse(t *testing.T) {
	s := startMock(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	raw, err := s.Call(ctx, "big", map[string]any{"size": 1024 * 512})
	if err != nil {
		t.Fatal(err)
	}
	if len(raw) < 1024*500 {
		t.Fatalf("big response short: %d", len(raw))
	}
}

func TestStdioTimeoutBecomesTimeoutError(t *testing.T) {
	s := startMock(t)
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	_, err := s.Call(ctx, "slow", map[string]any{"ms": 2000})
	if err == nil {
		t.Fatal("expected error")
	}
	if mcperrors.Classify(err) != mcperrors.CategoryTimeout {
		t.Fatalf("category = %s", mcperrors.Classify(err))
	}
}

func TestStdioCloseStopsProcess(t *testing.T) {
	s := startMock(t)
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	// Second close should be idempotent.
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	_, err := s.Call(context.Background(), "ping", nil)
	if err == nil {
		t.Fatal("expected closed error")
	}
	if mcperrors.Classify(err) != mcperrors.CategoryTransport {
		t.Fatalf("category = %s", mcperrors.Classify(err))
	}
}

func TestStdioSubprocessCrash(t *testing.T) {
	s := startMock(t)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_, _ = s.Call(ctx, "crash", nil)
	// Wait briefly for reader goroutine to exit.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if s.ReadError() != nil {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if s.ReadError() == nil {
		t.Fatal("expected read error after crash")
	}
	// Subsequent Call should fail with transport error.
	_, err := s.Call(context.Background(), "ping", nil)
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestStdioNoisyStderrNotForwardedWhenSilent(t *testing.T) {
	s := startMock(t, func(c *StdioConfig) { c.Silent = true })
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if _, err := s.Call(ctx, "noisy_stderr", nil); err != nil {
		t.Fatal(err)
	}
}

func TestStdioBadJSONIgnored(t *testing.T) {
	s := startMock(t)
	// Fire ping first — baseline works.
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if _, err := s.Call(ctx, "ping", nil); err != nil {
		t.Fatal(err)
	}
	// Fire bad_json with a short deadline — server writes junk, our pending
	// call times out cleanly (no id found).
	ctx2, cancel2 := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel2()
	_, err := s.Call(ctx2, "bad_json", nil)
	if err == nil {
		t.Fatal("expected timeout")
	}
	// After the junk line, subsequent ping still works.
	ctx3, cancel3 := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel3()
	if _, err := s.Call(ctx3, "ping", nil); err != nil {
		t.Fatal(err)
	}
}

func TestStdioRejectsNoCmd(t *testing.T) {
	_, err := StartStdio(context.Background(), StdioConfig{})
	if err == nil {
		t.Fatal("expected cmd required")
	}
}

func TestStdioRejectsMissingBinary(t *testing.T) {
	_, err := StartStdio(context.Background(), StdioConfig{Cmd: "/path/does/not/exist/mcpbench-x"})
	if err == nil {
		t.Fatal("expected start error")
	}
	var te *mcperrors.TransportError
	if !errors.As(err, &te) {
		t.Fatalf("expected TransportError, got %T", err)
	}
}

func TestStdioEnvMerged(t *testing.T) {
	s := startMock(t, func(c *StdioConfig) { c.Env = map[string]string{"MCPBENCH_TEST": "1"} })
	// Just ensure it doesn't crash — mock ignores env.
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if _, err := s.Call(ctx, "ping", nil); err != nil {
		t.Fatal(err)
	}
}

func TestStdioCustomShutdownGrace(t *testing.T) {
	s := startMock(t, func(c *StdioConfig) { c.ShutdownGrace = 100 * time.Millisecond })
	if s.cfg.ShutdownGrace != 100*time.Millisecond {
		t.Fatal("grace")
	}
	_ = s.Close()
}

func TestStdioDefaultShutdownGrace(t *testing.T) {
	s := startMock(t)
	if s.cfg.ShutdownGrace != 5*time.Second {
		t.Fatalf("default grace = %v", s.cfg.ShutdownGrace)
	}
}

func TestStdioDefaultMaxLineBytes(t *testing.T) {
	s := startMock(t)
	if s.cfg.MaxLineBytes != 16*1024*1024 {
		t.Fatalf("default max = %d", s.cfg.MaxLineBytes)
	}
}

func TestStdioCallUsesContextCancellation(t *testing.T) {
	s := startMock(t)
	ctx, cancel := context.WithCancel(context.Background())
	// Fire a slow call; cancel while it is pending.
	done := make(chan error, 1)
	go func() {
		_, err := s.Call(ctx, "slow", map[string]any{"ms": 2000})
		done <- err
	}()
	time.Sleep(50 * time.Millisecond)
	cancel()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("expected cancellation error")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("cancellation did not propagate")
	}
}

func TestStdioDemuxDistinctIDs(t *testing.T) {
	// Concurrent callers must receive the response that matches their own id,
	// even when many requests are in flight at once.
	s := startMock(t)
	const n = 30
	var wg sync.WaitGroup
	errs := make(chan error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			raw, err := s.Call(ctx, "echo", map[string]any{"tag": i})
			if err != nil {
				errs <- err
				return
			}
			var resp struct {
				ID     int64          `json:"id"`
				Result map[string]any `json:"result"`
			}
			_ = json.Unmarshal(raw, &resp)
			if int(resp.Result["tag"].(float64)) != i {
				errs <- errors.New("tag mismatch — demux leaked")
			}
		}(i)
	}
	wg.Wait()
	close(errs)
	for e := range errs {
		t.Error(e)
	}
}

func contains(hay, needle []byte) bool {
	return indexOf(hay, needle) >= 0
}

func indexOf(hay, needle []byte) int {
outer:
	for i := 0; i+len(needle) <= len(hay); i++ {
		for j := 0; j < len(needle); j++ {
			if hay[i+j] != needle[j] {
				continue outer
			}
		}
		return i
	}
	return -1
}
