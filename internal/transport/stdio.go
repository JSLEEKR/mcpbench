package transport

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sync"
	"syscall"
	"time"

	mcperrors "github.com/JSLEEKR/mcpbench/internal/errors"
	"github.com/JSLEEKR/mcpbench/internal/jsonrpc"
)

// StdioConfig holds subprocess launch parameters.
type StdioConfig struct {
	// Cmd is the binary to execute.
	Cmd string
	// Args are the arguments passed to the subprocess.
	Args []string
	// Env holds additional environment variables (merged with os.Environ).
	Env map[string]string
	// Silent, when true, discards the subprocess stderr instead of forwarding.
	Silent bool
	// ShutdownGrace is the time allowed between SIGTERM and SIGKILL during
	// Close. Defaults to 5 seconds when zero.
	ShutdownGrace time.Duration
	// MaxLineBytes overrides the bufio.Scanner buffer size. Defaults to 16
	// MiB. Oversized responses are reported as transport errors.
	MaxLineBytes int
}

// Stdio is a subprocess-backed JSON-RPC transport speaking newline-delimited
// frames.
type Stdio struct {
	cfg      StdioConfig
	cmd      *exec.Cmd
	stdin    io.WriteCloser
	writeMu  sync.Mutex
	ids      *jsonrpc.IDPool
	pending  sync.Map // int64 -> chan pendingResult
	closeCh  chan struct{}
	closed   bool
	closeMu  sync.Mutex
	waitDone chan error
	readerWG sync.WaitGroup
	readErr  error
}

type pendingResult struct {
	raw []byte
	err error
}

// StartStdio launches the subprocess and returns a ready Stdio transport.
func StartStdio(ctx context.Context, cfg StdioConfig) (*Stdio, error) {
	if cfg.Cmd == "" {
		return nil, fmt.Errorf("stdio: cmd required")
	}
	if cfg.ShutdownGrace == 0 {
		cfg.ShutdownGrace = 5 * time.Second
	}
	if cfg.MaxLineBytes <= 0 {
		cfg.MaxLineBytes = 16 * 1024 * 1024
	}

	cmd := exec.CommandContext(ctx, cfg.Cmd, cfg.Args...)
	cmd.Env = mergeEnv(cfg.Env)
	stdinPipe, err := cmd.StdinPipe()
	if err != nil {
		return nil, fmt.Errorf("stdio: stdin pipe: %w", err)
	}
	stdoutPipe, err := cmd.StdoutPipe()
	if err != nil {
		_ = stdinPipe.Close()
		return nil, fmt.Errorf("stdio: stdout pipe: %w", err)
	}
	if cfg.Silent {
		cmd.Stderr = io.Discard
	} else {
		cmd.Stderr = os.Stderr
	}

	if err := cmd.Start(); err != nil {
		_ = stdinPipe.Close()
		return nil, &mcperrors.TransportError{Inner: fmt.Errorf("stdio: start %q: %w", cfg.Cmd, err)}
	}

	t := &Stdio{
		cfg:      cfg,
		cmd:      cmd,
		stdin:    stdinPipe,
		ids:      jsonrpc.NewIDPool(),
		closeCh:  make(chan struct{}),
		waitDone: make(chan error, 1),
	}
	t.readerWG.Add(1)
	go t.readLoop(stdoutPipe)
	go func() {
		t.waitDone <- cmd.Wait()
	}()
	return t, nil
}

func mergeEnv(extra map[string]string) []string {
	base := os.Environ()
	if len(extra) == 0 {
		return base
	}
	out := make([]string, 0, len(base)+len(extra))
	out = append(out, base...)
	for k, v := range extra {
		out = append(out, k+"="+v)
	}
	return out
}

func (s *Stdio) readLoop(r io.ReadCloser) {
	defer s.readerWG.Done()
	defer r.Close()
	scanner := bufio.NewScanner(r)
	buf := make([]byte, 0, 64*1024)
	scanner.Buffer(buf, s.cfg.MaxLineBytes)
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}
		// Copy — scanner reuses the buffer.
		cp := make([]byte, len(line))
		copy(cp, line)
		s.deliver(cp)
	}
	err := scanner.Err()
	if err == nil {
		err = io.EOF
	}
	s.readErr = err
	// Wake any still-waiting callers.
	s.pending.Range(func(key, val any) bool {
		ch := val.(chan pendingResult)
		select {
		case ch <- pendingResult{err: &mcperrors.TransportError{Inner: err}}:
		default:
		}
		s.pending.Delete(key)
		return true
	})
}

func (s *Stdio) deliver(line []byte) {
	var peek struct {
		ID int64 `json:"id"`
	}
	if err := json.Unmarshal(line, &peek); err != nil {
		// Not a recognizable JSON object; drop.
		return
	}
	val, ok := s.pending.LoadAndDelete(peek.ID)
	if !ok {
		// Unknown id — ignore (server notifications, out-of-band frames).
		return
	}
	ch := val.(chan pendingResult)
	select {
	case ch <- pendingResult{raw: line}:
	default:
	}
}

// Call issues a JSON-RPC call over stdio.
func (s *Stdio) Call(ctx context.Context, method string, params any) ([]byte, error) {
	s.closeMu.Lock()
	if s.closed {
		s.closeMu.Unlock()
		return nil, &mcperrors.TransportError{Inner: fmt.Errorf("stdio: transport closed")}
	}
	s.closeMu.Unlock()

	id := s.ids.Next()
	req := jsonrpc.NewRequest(id, method, params)
	body, err := req.Marshal()
	if err != nil {
		return nil, &mcperrors.TransportError{Inner: fmt.Errorf("stdio: marshal: %w", err)}
	}
	body = append(body, '\n')

	ch := make(chan pendingResult, 1)
	s.pending.Store(id, ch)

	s.writeMu.Lock()
	if _, err := s.stdin.Write(body); err != nil {
		s.writeMu.Unlock()
		s.pending.Delete(id)
		return nil, &mcperrors.TransportError{Inner: fmt.Errorf("stdio: write: %w", err)}
	}
	s.writeMu.Unlock()

	select {
	case res := <-ch:
		return res.raw, res.err
	case <-ctx.Done():
		s.pending.Delete(id)
		return nil, &mcperrors.TimeoutError{Inner: ctx.Err()}
	case <-s.closeCh:
		s.pending.Delete(id)
		return nil, &mcperrors.TransportError{Inner: fmt.Errorf("stdio: closed mid-call")}
	}
}

// Close cleanly terminates the subprocess.
func (s *Stdio) Close() error {
	s.closeMu.Lock()
	if s.closed {
		s.closeMu.Unlock()
		return nil
	}
	s.closed = true
	close(s.closeCh)
	s.closeMu.Unlock()

	_ = s.stdin.Close()

	// Attempt graceful termination first.
	if s.cmd.Process != nil {
		_ = s.cmd.Process.Signal(syscall.SIGTERM)
	}

	timer := time.NewTimer(s.cfg.ShutdownGrace)
	defer timer.Stop()
	select {
	case <-s.waitDone:
	case <-timer.C:
		if s.cmd.Process != nil {
			_ = s.cmd.Process.Kill()
		}
		<-s.waitDone
	}
	s.readerWG.Wait()
	return nil
}

// ReadError returns the error that caused the reader goroutine to exit, if
// any. Useful for diagnostics in tests.
func (s *Stdio) ReadError() error { return s.readErr }
