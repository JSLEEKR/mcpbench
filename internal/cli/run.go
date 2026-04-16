package cli

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"github.com/JSLEEKR/mcpbench/internal/metrics"
	"github.com/JSLEEKR/mcpbench/internal/orchestrator"
	"github.com/JSLEEKR/mcpbench/internal/report"
	"github.com/JSLEEKR/mcpbench/internal/scenario"
	"github.com/JSLEEKR/mcpbench/internal/transport"
)

// runFlags holds the parsed flag values for the run command.
type runFlags struct {
	scenarioPath  string
	transport     string
	spawn         string
	url           string
	rate          float64
	concurrency   int
	duration      time.Duration
	requests      int
	warmup        time.Duration
	cooldown      time.Duration
	timeout       time.Duration
	thinkTime     time.Duration
	output        string
	outputFile    string
	silentServer  bool
	seed          int64
	reservoirSize int
	allowSSE      bool
}

// NewRunCmd builds the `mcpbench run` subcommand.
func NewRunCmd() *cobra.Command {
	f := &runFlags{}
	cmd := &cobra.Command{
		Use:   "run",
		Short: "Run a load test against an MCP server",
		RunE: func(cmd *cobra.Command, args []string) error {
			return runExecute(cmd.OutOrStdout(), cmd.ErrOrStderr(), f)
		},
	}
	cmd.Flags().StringVar(&f.scenarioPath, "scenario", "", "path to scenario YAML (required)")
	cmd.Flags().StringVar(&f.transport, "transport", "", "override transport type (stdio|http)")
	cmd.Flags().StringVar(&f.spawn, "spawn", "", "override subprocess command for stdio transport")
	cmd.Flags().StringVar(&f.url, "url", "", "override HTTP endpoint for http transport")
	cmd.Flags().Float64Var(&f.rate, "rate", 0, "target QPS (open-model). 0 = use scenario value")
	cmd.Flags().IntVar(&f.concurrency, "concurrency", 0, "worker count (closed-model). 0 = use scenario value")
	cmd.Flags().DurationVar(&f.duration, "duration", 0, "run duration (mutually exclusive with --requests)")
	cmd.Flags().IntVar(&f.requests, "requests", 0, "total request count")
	cmd.Flags().DurationVar(&f.warmup, "warmup", 0, "warmup duration excluded from metrics")
	cmd.Flags().DurationVar(&f.cooldown, "cooldown", 0, "cooldown duration after measurement")
	cmd.Flags().DurationVar(&f.timeout, "timeout", 0, "per-call timeout")
	cmd.Flags().DurationVar(&f.thinkTime, "think-time", 0, "delay between calls per worker")
	cmd.Flags().StringVar(&f.output, "output", "human", "output format: human|json|csv|prom")
	cmd.Flags().StringVar(&f.outputFile, "output-file", "", "write output to file instead of stdout")
	cmd.Flags().BoolVar(&f.silentServer, "silent-server", false, "suppress subprocess stderr")
	cmd.Flags().Int64Var(&f.seed, "seed", 0, "deterministic tool-selection seed (0 = random)")
	cmd.Flags().IntVar(&f.reservoirSize, "reservoir-size", metrics.DefaultReservoirSize, "per-tool latency reservoir size")
	cmd.Flags().BoolVar(&f.allowSSE, "allow-sse", false, "parse text/event-stream responses")
	return cmd
}

func runExecute(stdout, stderr io.Writer, f *runFlags) error {
	if strings.TrimSpace(f.scenarioPath) == "" {
		return fmt.Errorf("--scenario is required")
	}
	if f.duration > 0 && f.requests > 0 {
		return fmt.Errorf("--duration and --requests are mutually exclusive")
	}

	s, err := scenario.Load(f.scenarioPath)
	if err != nil {
		return err
	}
	if err := applyCLIOverrides(s, f); err != nil {
		return err
	}

	// Validate after overrides.
	if err := s.Validate(); err != nil {
		return err
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt, syscall.SIGTERM)
	go func() {
		<-sig
		fmt.Fprintln(stderr, "mcpbench: interrupt received, draining...")
		cancel()
	}()
	defer signal.Stop(sig)

	tr, err := buildTransport(ctx, s, f)
	if err != nil {
		return err
	}
	defer tr.Close()

	agg := metrics.NewAggregator(f.reservoirSize)
	started := time.Now()
	elapsed, err := orchestrator.Run(ctx, orchestrator.Config{
		Scenario:      s,
		Transport:     tr,
		Aggregator:    agg,
		Seed:          f.seed,
		ReservoirSize: f.reservoirSize,
	})
	if err != nil {
		return err
	}
	ended := started.Add(elapsed)

	summary := agg.SummarizeAll()
	throughput := float64(summary.Overall.Latency.Count) / seconds(elapsed)
	rf := &report.RunFile{
		Version:    report.Version,
		Scenario:   s.Name,
		StartedAt:  started,
		EndedAt:    ended,
		Throughput: throughput,
		Summary:    summary,
	}

	format, err := report.ParseFormat(f.output)
	if err != nil {
		return err
	}

	out := stdout
	if f.outputFile != "" {
		fh, err := os.Create(f.outputFile)
		if err != nil {
			return fmt.Errorf("create output file: %w", err)
		}
		defer fh.Close()
		out = fh
	}
	return report.Write(out, format, rf)
}

func seconds(d time.Duration) float64 {
	if d <= 0 {
		return 1 // avoid div-by-zero in throughput calc for aborted runs
	}
	return d.Seconds()
}

func applyCLIOverrides(s *scenario.Scenario, f *runFlags) error {
	if f.transport != "" {
		s.Transport.Type = f.transport
	}
	if f.spawn != "" {
		// Parse "node server.js" into cmd + args. parseSpawn handles quoted
		// arguments (single and double) and backslash escapes so that paths
		// with spaces (e.g. `C:\Program Files\node\node.exe`) and quoted
		// arguments (e.g. `node "my file.js"`) are honored. Using
		// strings.Fields here would silently split those on whitespace, which
		// broke every Windows user with a non-trivial install path.
		parts, err := parseSpawn(f.spawn)
		if err != nil {
			return fmt.Errorf("--spawn: %w", err)
		}
		if len(parts) > 0 {
			s.Transport.Cmd = parts[0]
			s.Transport.Args = parts[1:]
		}
	}
	if f.url != "" {
		s.Transport.URL = f.url
	}
	if f.rate > 0 {
		s.Workload.Rate = f.rate
	}
	if f.concurrency > 0 {
		s.Workload.Concurrency = f.concurrency
	}
	if f.duration > 0 {
		s.Workload.Duration = f.duration
		s.Workload.Requests = 0
	}
	if f.requests > 0 {
		s.Workload.Requests = f.requests
		s.Workload.Duration = 0
	}
	if f.warmup > 0 {
		s.Workload.Warmup = f.warmup
	}
	if f.cooldown > 0 {
		s.Workload.Cooldown = f.cooldown
	}
	if f.timeout > 0 {
		s.Workload.Timeout = f.timeout
	}
	if f.thinkTime > 0 {
		s.Workload.ThinkTime = f.thinkTime
	}
	return nil
}

// parseSpawn is a minimal shlex-style splitter used exclusively by --spawn. It
// splits on unquoted whitespace, honors single and double quotes (neither of
// which interpret further escapes inside themselves — matching POSIX single-
// quote semantics, and treating double quotes the same way since we don't do
// variable expansion), and supports backslash escapes OUTSIDE quotes so that
// users can also write `/tmp/my\ dir/testmock` in addition to
// `'/tmp/my dir/testmock'`. Returns an error on an unterminated quote.
//
// We intentionally do NOT implement shell metacharacter handling (redirects,
// pipes, variable expansion): --spawn is handed directly to exec.Command, not
// to `sh -c`, so there is no shell to honor those anyway and failing loudly is
// better than silently accepting shell syntax that never fires.
func parseSpawn(s string) ([]string, error) {
	var parts []string
	var cur strings.Builder
	inQuote := byte(0) // 0 = not in quote, '\'' or '"' when inside
	escaped := false
	emitted := false
	for i := 0; i < len(s); i++ {
		c := s[i]
		if escaped {
			cur.WriteByte(c)
			escaped = false
			emitted = true
			continue
		}
		if inQuote != 0 {
			if c == inQuote {
				inQuote = 0
				emitted = true
				continue
			}
			cur.WriteByte(c)
			continue
		}
		switch c {
		case '\\':
			escaped = true
		case '\'', '"':
			inQuote = c
			emitted = true
		case ' ', '\t':
			if emitted {
				parts = append(parts, cur.String())
				cur.Reset()
				emitted = false
			}
		default:
			cur.WriteByte(c)
			emitted = true
		}
	}
	if inQuote != 0 {
		return nil, fmt.Errorf("unterminated %c quote", inQuote)
	}
	if escaped {
		return nil, fmt.Errorf("trailing backslash")
	}
	if emitted {
		parts = append(parts, cur.String())
	}
	return parts, nil
}

func buildTransport(ctx context.Context, s *scenario.Scenario, f *runFlags) (transport.Transport, error) {
	kind, err := transport.ParseKind(strings.ToLower(s.Transport.Type))
	if err != nil {
		return nil, err
	}
	switch kind {
	case transport.KindStdio:
		return transport.StartStdio(ctx, transport.StdioConfig{
			Cmd:    s.Transport.Cmd,
			Args:   s.Transport.Args,
			Env:    s.Transport.Env,
			Silent: f.silentServer,
		})
	case transport.KindHTTP, transport.KindSSE:
		allow := f.allowSSE || kind == transport.KindSSE
		return transport.NewHTTP(transport.HTTPConfig{
			URL:      s.Transport.URL,
			Headers:  s.Transport.Headers,
			AllowSSE: allow,
		})
	default:
		return nil, fmt.Errorf("unsupported transport %q", s.Transport.Type)
	}
}
