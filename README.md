# mcpbench

[![Go Version](https://img.shields.io/badge/go-1.22-00ADD8?style=for-the-badge&logo=go)](https://go.dev)
[![License: MIT](https://img.shields.io/badge/License-MIT-yellow.svg?style=for-the-badge)](LICENSE)
[![Build](https://img.shields.io/badge/build-passing-brightgreen?style=for-the-badge)](#tests)
[![Tests](https://img.shields.io/badge/tests-235%20passing-brightgreen?style=for-the-badge)](#tests)
[![MCP](https://img.shields.io/badge/protocol-MCP%20JSON--RPC%202.0-blueviolet?style=for-the-badge)](https://modelcontextprotocol.io)

> Protocol-aware load tester and benchmarker for MCP (Model Context Protocol) servers.
> Concurrent JSON-RPC 2.0 over stdio or HTTP/SSE, per-tool p50/p95/p99 latency,
> scenario YAML, and a `compare` subcommand for CI regression gating.

mcpbench answers the question every MCP server author eventually asks:
**"How many requests per second can this server sustain at p99 < 200 ms?"**

It does so the way the server actually gets used — by speaking JSON-RPC 2.0,
tracking per-tool-call latency distributions, handling both stdio subprocesses
and HTTP/SSE endpoints, and producing machine-readable output for CI gates.

## Why This Exists

Generic HTTP benchmarkers (`hey`, `wrk`, `vegeta`, `bombardier`) cannot:

- Speak JSON-RPC 2.0 over an MCP server's stdio transport (newline-framed
  requests against a spawned subprocess).
- Aggregate latency per tool name (`read_file` p95 vs `search` p95).
- Distinguish JSON-RPC errors (`code: -32601`) from transport timeouts.
- Template tool arguments per iteration (`{"pattern": "page-{{.Iter}}"}`).
- Survive SSE streaming responses that return multiple events per request.

mcpbench is purpose-built for MCP servers: it understands `tools/call`
semantics, demultiplexes concurrent JSON-RPC correlation IDs over a single
stdio pipe, parses `text/event-stream` responses, and breaks down errors by
category (timeout / jsonrpc / transport / template).

The design tracks the portfolio gap in MCP tooling:

| Tool                | Lifecycle phase             |
|---------------------|------------------------------|
| `mcptest`           | Functional correctness       |
| `mcpaudit`          | Static security audit        |
| `mcprouter`         | Routing / load-balancing     |
| `mcptrace`          | Distributed tracing          |
| **`mcpbench`**      | **Dynamic load + latency**   |

## Install

### From source (Go 1.22+)

```bash
go install github.com/JSLEEKR/mcpbench/cmd/mcpbench@latest
```

### Clone and build

```bash
git clone https://github.com/JSLEEKR/mcpbench
cd mcpbench
go build -o mcpbench ./cmd/mcpbench
./mcpbench version
```

### Static binary (Linux/macOS release profile)

```bash
CGO_ENABLED=0 go build -ldflags="-s -w" -o mcpbench ./cmd/mcpbench
```

## Quickstart — stdio MCP server

Assume you have a node-based MCP server at `./dist/server.js` that exposes
`read_file`, `search`, and `list_dir` tools. Create a scenario:

```yaml
# scenario.yml
name: awesome-mcp-server-hot-path
transport:
  type: stdio
  cmd: node
  args: ["./dist/server.js"]
  env:
    LOG_LEVEL: warn
workload:
  rate: 100          # QPS target (open-model)
  duration: 30s
  warmup: 5s         # excluded from metrics
  cooldown: 2s       # drain window
  timeout: 5s        # per-call timeout
tools:
  - name: read_file
    weight: 3
    args:
      path: "/tmp/a.txt"
  - name: search
    weight: 1
    args:
      pattern: "iter-{{.Iter}}"
      max_results: 10
  - name: list_dir
    weight: 1
    args:
      path: "/"
```

Then run:

```bash
mcpbench run --scenario scenario.yml --silent-server
```

Human-readable output:

```
scenario    awesome-mcp-server-hot-path
started     2026-04-17T10:00:00Z
ended       2026-04-17T10:00:30Z
duration    30s
throughput  98.4 req/s

TOOL         COUNT  P50    P95    P99    MEAN   ERR%  ERRORS
__overall__  2951   18ms   42ms   88ms   22ms   0.07  jsonrpc=2
read_file    1771   15ms   35ms   71ms   18ms   0.00  -
search       590    28ms   68ms   110ms  33ms   0.34  jsonrpc=2
list_dir     590    22ms   45ms   79ms   24ms   0.00  -
```

## Quickstart — HTTP/SSE endpoint

For an MCP server exposed as an HTTP service (common for hosted deployments):

```yaml
name: hosted-mcp
transport:
  type: http
  url: https://mcp.example.com/rpc
  headers:
    Authorization: "Bearer ${MCP_TOKEN}"
workload:
  concurrency: 16
  requests: 1000
  timeout: 10s
tools:
  - name: query
    args:
      sql: "SELECT {{.Iter}}"
```

```bash
MCP_TOKEN=secret mcpbench run --scenario hosted.yml --allow-sse
```

## Scenario YAML Reference

```yaml
name: string              # required — scenario identifier
transport:
  type: stdio | http | sse # required
  # stdio-only:
  cmd:  string             # subprocess binary
  args: [string]           # arguments
  env:  {KEY: value}       # additional env (merged with os.Environ)
  # http/sse-only:
  url:     string          # https:// or http://
  headers: {Key: value}
workload:
  rate:        float       # target QPS (open-model)
  concurrency: int         # worker count (closed-model)
  duration:    duration    # e.g., "30s", "5m" — mutually exclusive with requests
  requests:    int         # total request count
  warmup:      duration    # discarded from metrics
  cooldown:    duration    # drain window after measurement
  think_time:  duration    # delay per worker between calls
  timeout:     duration    # default per-call timeout
tools:
  - name:    string        # MCP tool name (passed as params.name to tools/call)
    weight:  int           # relative selection weight (default 1)
    args:    map           # tools/call arguments — may contain templates
    timeout: duration      # per-tool override
```

### Templating

String values inside `tools[].args` are rendered with Go's `text/template`:

| Variable   | Meaning                              |
|------------|---------------------------------------|
| `.Iter`    | Monotonic request counter (1, 2, 3…) |
| `.Env.X`   | Environment variable `X`             |
| `.Rand`    | Random float in [0, 1)               |

Missing variables produce a template error (recorded separately, not a
transport error).

## Output Formats

```bash
mcpbench run --scenario s.yml --output human         # default
mcpbench run --scenario s.yml --output json          # machine-readable
mcpbench run --scenario s.yml --output csv           # spreadsheet-friendly
mcpbench run --scenario s.yml --output prom          # Prometheus text exposition
mcpbench run --scenario s.yml --output-file run.json # write to file
```

### JSON schema (top-level)

```json
{
  "version": "mcpbench/v1",
  "scenario": "…",
  "started_at": "2026-04-17T10:00:00Z",
  "ended_at":   "2026-04-17T10:00:30Z",
  "throughput_per_sec": 98.4,
  "summary": {
    "overall":  { "name":"__overall__", "latency":{…}, "ok":…, "errors":…, "error_pct":… },
    "per_tool": { "read_file": {…}, "search": {…} },
    "tool_keys":["read_file", "search"]
  }
}
```

### Prometheus exposition

```
# TYPE mcpbench_requests_total counter
# TYPE mcpbench_latency_seconds gauge
mcpbench_requests_total{scenario="hot-path",tool="read_file",status="ok"} 1771
mcpbench_latency_seconds{scenario="hot-path",tool="read_file",quantile="0.95"} 0.035
mcpbench_throughput_per_second{scenario="hot-path"} 98.4
```

Drop into a Prometheus textfile collector or `pushgateway` to ingest straight
from CI.

## `mcpbench compare` — CI Regression Gating

Run twice — once against your baseline, once against the candidate — then gate
CI on the diff:

```bash
mcpbench run --scenario s.yml --output json --output-file baseline.json
# … make a change …
mcpbench run --scenario s.yml --output json --output-file candidate.json

mcpbench compare baseline.json candidate.json \
    --threshold-p95=+20% \
    --threshold-p99=+25% \
    --threshold-error=+2pp
echo "exit code: $?"   # 0 = clean, 1 = regression
```

Threshold syntax:

| Suffix | Meaning                         | Example  |
|--------|---------------------------------|----------|
| `%`    | Relative change                 | `+20%`   |
| `ms`   | Absolute millisecond delta      | `+10ms`  |
| `pp`   | Percentage-point (error rate)   | `+2pp`   |

Human output:

```
mcpbench compare — REGRESSION: one or more thresholds exceeded
----------------------------------------------------------------
__overall__  p95 42.00ms → 58.00ms (+38.1%)  p99 88.00ms → 95.00ms (+8.0%)  err 0.07% → 0.12% (+0.05pp)  FAIL
  ! p95: +38.1% > +20.0%
read_file    p95 35.00ms → 55.00ms (+57.1%)  p99 71.00ms → 88.00ms (+23.9%)  err 0.00% → 0.00% (+0.00pp)  FAIL
  ! p95: +57.1% > +20.0%
search       p95 68.00ms → 66.00ms (-2.9%)   p99 110ms → 108ms (-1.8%)      err 0.34% → 0.20% (-0.14pp)  OK
```

JSON mode (`--output json`) is friendlier for CI script parsing.

## Architecture

```
+------------------------------------------------------------------+
|                          mcpbench CLI                            |
|  (cobra root: run | compare | version | scenario-validate)      |
+------------------------------------------------------------------+
                     |
                     v
+------------------------------------------------------------------+
|                       Scenario Loader                            |
|  (YAML parse, template, weight normalize, warmup/cooldown split) |
+------------------------------------------------------------------+
                     |
                     v
+------------------------------------------------------------------+
|                         Orchestrator                             |
|  (duration/N-requests loop, rate limiter, worker pool)           |
+------------------------------------------------------------------+
                     |
        +------------+------------+
        v                         v
+------------------+      +------------------+
|  Stdio Transport |      |  HTTP Transport  |
|  (subprocess,    |      |  (REST + SSE,    |
|   line framing)  |      |   net/http)      |
+------------------+      +------------------+
                     |
                     v
+------------------------------------------------------------------+
|                       Metrics Aggregator                         |
|  (latency reservoir, per-tool buckets, error categorizer)        |
+------------------------------------------------------------------+
                     |
                     v
+------------------------------------------------------------------+
|                            Reporter                              |
|  (table | json | csv | prom)                                    |
+------------------------------------------------------------------+
```

### Stdio transport (subprocess lifecycle)

mcpbench spawns the server with `exec.CommandContext`, writes newline-delimited
JSON-RPC requests to stdin, and reads responses from stdout. A single reader
goroutine demultiplexes responses by `id` field into per-request channels,
allowing many concurrent `Call`s to share one stdin pipe safely.

Shutdown sends `SIGTERM` and waits 5 seconds (configurable via `StdioConfig.
ShutdownGrace`) before falling back to `SIGKILL`.

### Error categorization

Every observation lands in exactly one bucket:

| Category    | Meaning                                                   |
|-------------|------------------------------------------------------------|
| `ok`        | Successful `tools/call` with no body-level error           |
| `timeout`   | Context deadline exceeded (per-call timeout fired)         |
| `jsonrpc`   | Server returned a JSON-RPC `error` field                   |
| `transport` | Subprocess died, connection refused, non-2xx HTTP, etc.    |
| `template`  | Tool args template failed to render                        |
| `other`     | Unclassified                                               |

The `errors` column in the human output and the `breakdown` field in JSON
output both break errors down by category.

## CLI Reference

```
mcpbench run                  Run a load test
  --scenario FILE             (required)
  --transport TYPE            override scenario transport (stdio|http)
  --spawn CMD                 override stdio subprocess ("node server.js")
  --url URL                   override http endpoint
  --rate N                    target QPS (open-model)
  --concurrency N             worker count (closed-model)
  --duration DUR              run duration (mutually exclusive with --requests)
  --requests N                total request count
  --warmup DUR                excluded from metrics
  --cooldown DUR              drain window
  --timeout DUR               default per-call timeout
  --think-time DUR            per-worker delay between calls
  --output FORMAT             human|json|csv|prom
  --output-file FILE
  --silent-server             suppress spawned subprocess stderr
  --seed N                    deterministic tool selection (0 = random)
  --reservoir-size N          latency sample buffer per tool (default 1M)
  --allow-sse                 parse text/event-stream responses

mcpbench compare BASELINE.json CANDIDATE.json
  --threshold-p95 VAL         default +20%
  --threshold-p99 VAL         default +25%
  --threshold-error VAL       default +2pp
  --output FORMAT             text|json

mcpbench scenario-validate FILE
  (parse + schema-check only — exit 0 on valid)

mcpbench version
```

## Tests

235 tests across transport (stdio + HTTP + SSE), scenario loading, metrics
aggregation, compare / regression, CLI integration, and end-to-end flows
driven by a real JSON-RPC stdio subprocess.

```bash
go test ./...             # all tests
go test -race ./...       # with race detector
go test -cover ./...      # coverage
```

## Design Trade-offs

- **Latency reservoir**: fixed-window ring buffer (default 1M samples). At very
  high request counts the p99 is a recent approximation, not a true global p99.
  Acceptable for CI gating; documented in-tree.
- **Single host**: no distributed coordinator. One mcpbench process drives one
  target server. Sufficient for almost every MCP server's real workload.
- **No WebSocket**: stdio and HTTP/SSE cover 100% of reference MCP SDKs.
- **Reservoir sort on finalize**: percentile computation happens once at report
  time, not in the hot path.

## Contributing

Contributions welcome. Please:

1. Add a test (or tests) alongside any code change.
2. Keep dependencies minimal (stdlib first; cobra + yaml are the only big deps).
3. `go vet ./...` and `go test -race ./...` must pass.
4. For new features, update the scenario YAML reference and this README.

## Compatibility

- Go: 1.22 or newer.
- Platforms: Linux, macOS. Windows stdio subprocess tests are not currently
  covered; the code uses `syscall.SIGTERM` which is portable to Windows via
  `os.Interrupt` at the cost of graceful termination fidelity.
- MCP protocol: JSON-RPC 2.0 per the current reference SDKs (Python, TypeScript,
  Go) as of 2026-04.

## License

MIT © 2026 JSLEEKR. See [LICENSE](LICENSE).

## See Also

- [mcptest](https://github.com/JSLEEKR/mcptest) — functional testing for MCP servers
- [mcpaudit](https://github.com/JSLEEKR/mcpaudit) — static security audit
- [mcprouter](https://github.com/JSLEEKR/mcprouter) — MCP request router
- [mcptrace-rs](https://github.com/JSLEEKR/mcptrace-rs) — distributed tracing

Together, these cover the MCP lifecycle: design → test → audit → route →
trace → **bench**.
